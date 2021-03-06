package evstepfunc

import (
	"bytes"
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sfn"
	"github.com/coopernurse/maelstrom/pkg/common"
	"github.com/coopernurse/maelstrom/pkg/evsource"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	log "github.com/mgutz/logxi/v1"
	"github.com/pkg/errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

func activityArn(sfnClient *sfn.SFN, activityName string) (*string, error) {
	input := &sfn.ListActivitiesInput{}
	for {
		out, err := sfnClient.ListActivities(input)
		if err != nil {
			return nil, err
		}

		for _, act := range out.Activities {
			if act.Name != nil && *act.Name == activityName {
				return act.ActivityArn, nil
			}
		}

		if out.NextToken == nil || *out.NextToken == "" {
			return nil, nil
		}
		input.NextToken = out.NextToken
	}
}

func NewPollCreator(es v1.EventSource, awsSession *session.Session, gateway http.Handler) (evsource.PollCreator, error) {
	sfnClient := sfn.New(awsSession)
	arn, err := activityArn(sfnClient, es.Awsstepfunc.ActivityName)
	if err != nil {
		return nil, errors.Wrap(err, "evstepfunc: unable to lookup existing activity: "+es.Awsstepfunc.ActivityName)
	}
	if arn == nil {
		log.Info("evstepfunc: creating activity", "activityName", es.Awsstepfunc.ActivityName)
		createOut, err := sfnClient.CreateActivity(&sfn.CreateActivityInput{Name: aws.String(es.Awsstepfunc.ActivityName)})
		if err != nil {
			return nil, errors.Wrap(err, "evstepfunc: unable to create activity: "+es.Awsstepfunc.ActivityName)
		}
		arn = createOut.ActivityArn
	} else {
		if log.IsDebug() {
			log.Info("evstepfunc: found existing activity", "activityName", es.Awsstepfunc.ActivityName, "arn", *arn)
		}
	}

	return &StepFuncPollCreator{
		es:        setDefaults(es),
		arn:       arn,
		gateway:   gateway,
		sfnClient: sfnClient,
	}, nil
}

type StepFuncPollCreator struct {
	es        v1.EventSource
	arn       *string
	gateway   http.Handler
	sfnClient *sfn.SFN
}

func (s *StepFuncPollCreator) NewPoller() evsource.Poller {
	poller := &StepFuncPoller{
		arn:         s.arn,
		es:          s.es,
		errSleep:    5 * time.Second,
		gateway:     s.gateway,
		sfnClient:   s.sfnClient,
		getTaskLock: &sync.Mutex{},
	}
	return poller.Run
}

func (s *StepFuncPollCreator) ComponentName() string {
	return s.es.ComponentName
}

func (s *StepFuncPollCreator) RoleIdPrefix() string {
	return fmt.Sprintf("aws-stepfunc-%s", s.es.Name)
}

func (s *StepFuncPollCreator) MaxConcurrency() int {
	return int(s.es.Awsstepfunc.MaxConcurrency)
}

func (s *StepFuncPollCreator) MaxConcurrencyPerPoller() int {
	return int(s.es.Awsstepfunc.ConcurrencyPerPoller)
}

type StepFuncPoller struct {
	arn         *string
	es          v1.EventSource
	errSleep    time.Duration
	gateway     http.Handler
	sfnClient   *sfn.SFN
	getTaskLock *sync.Mutex
}

func (s *StepFuncPoller) Run(ctx context.Context, concurrency int, roleId string) {
	log.Info("evstepfunc: starting poller", "component", s.es.ComponentName, "arn", s.arn,
		"concurrency", concurrency, "roleId", roleId)

	wg := &sync.WaitGroup{}
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go s.workerLoop(ctx, wg)
	}

	wg.Wait()
	log.Info("evstepfunc: poller exiting gracefully", "component", s.es.ComponentName, "roleId", roleId)
}

func (s *StepFuncPoller) getTask(ctx context.Context) *sfn.GetActivityTaskOutput {
	s.getTaskLock.Lock()
	defer s.getTaskLock.Unlock()

	// double check if we're still running - skip poll if we aren't
	select {
	case <-ctx.Done():
		return nil
	default:
		break
	}

	reqCtx, reqCtxCancel := context.WithTimeout(context.Background(), 100*time.Second)
	out, err := s.sfnClient.GetActivityTaskWithContext(reqCtx, &sfn.GetActivityTaskInput{
		ActivityArn: s.arn,
		WorkerName:  nil,
	})
	reqCtxCancel()
	if err != nil {
		logerr := true
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() == request.CanceledErrorCode {
				logerr = false
				log.Warn("evstepfunc: GetActivityTask context canceled", "arn", s.arn)
			}
		}
		if logerr {
			log.Error("evstepfunc: error calling GetActivityTask", "err", err, "arn", s.arn)
			time.Sleep(s.errSleep)
		}
	}
	return out
}

func (s *StepFuncPoller) workerLoop(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		default:
			getTaskOut := s.getTask(ctx)
			if getTaskOut != nil && getTaskOut.Input != nil {
				s.invokeComponent(getTaskOut)
			}
		}
	}
}

func (s *StepFuncPoller) invokeComponent(out *sfn.GetActivityTaskOutput) {
	req, err := http.NewRequest("POST", s.es.Awsstepfunc.Path, bytes.NewBufferString(*out.Input))
	if err != nil {
		log.Error("evstepfunc: http.NewRequest", "err", err, "arn", s.arn)
	} else {
		startTime := common.NowMillis()
		rw := httptest.NewRecorder()
		req.Header.Set("Maelstrom-Component", s.es.ComponentName)
		s.gateway.ServeHTTP(rw, req)
		if log.IsDebug() {
			log.Debug("evstepfunc: req done", "component", s.es.ComponentName, "path", s.es.Awsstepfunc.Path,
				"millis", common.NowMillis()-startTime, "respcode", rw.Code)
		}
		if rw.Code == http.StatusOK {
			_, err = s.sfnClient.SendTaskSuccess(&sfn.SendTaskSuccessInput{
				Output:    aws.String(rw.Body.String()),
				TaskToken: out.TaskToken,
			})
			if err != nil {
				log.Error("evstepfunc: SendTaskSuccess", "err", err, "arn", s.arn)
			}
		} else {
			errStr := common.StrTruncate(rw.Header().Get("step-func-error"), 256)
			causeStr := common.StrTruncate(rw.Header().Get("step-func-cause"), 32768)

			if errStr == "" {
				errStr = fmt.Sprintf("maelstrom_%d", rw.Code)
			}
			if causeStr == "" {
				causeStr = fmt.Sprintf("maelstrom error: %s", err.Error())
			}

			_, err = s.sfnClient.SendTaskFailure(&sfn.SendTaskFailureInput{
				TaskToken: out.TaskToken,
				Error:     aws.String(errStr),
				Cause:     aws.String(causeStr),
			})
			if err != nil {
				log.Error("evstepfunc: SendTaskFailure", "err", err, "arn", s.arn)
			}
		}
	}
}

func setDefaults(es v1.EventSource) v1.EventSource {
	es.Awsstepfunc.MaxConcurrency = common.DefaultInt64(es.Awsstepfunc.MaxConcurrency, 1)
	es.Awsstepfunc.ConcurrencyPerPoller = common.DefaultInt64(es.Awsstepfunc.ConcurrencyPerPoller, 1)
	return es
}
