package maelstrom

//
//import (
//	"context"
//	"github.com/coopernurse/maelstrom/pkg/common"
//	component2 "github.com/coopernurse/maelstrom/pkg/maelstrom/component"
//	v1 "github.com/coopernurse/maelstrom/pkg/v1"
//	docker "github.com/docker/docker/client"
//	log "github.com/mgutz/logxi/v1"
//	"net/url"
//	"strconv"
//	"sync"
//	"time"
//)
//
//func StartContainer(reqCh chan *MaelRequest, dockerClient *docker.Client, component v1.Component,
//	containerId string) (*Container, error) {
//
//	proxy, healthCheckUrl, err := initReverseProxy(dockerClient, component, containerId)
//	if err != nil {
//		return nil, err
//	}
//
//	ctx, cancelFx := context.WithCancel(context.Background())
//	handlerWg := &sync.WaitGroup{}
//	runWg := &sync.WaitGroup{}
//
//	maxConcur := int(component.MaxConcurrency)
//	if maxConcur <= 0 {
//		maxConcur = 5
//	}
//
//	statCh := make(chan time.Duration, maxConcur)
//
//	for i := 0; i < maxConcur; i++ {
//		handlerWg.Add(1)
//		go component2.localRevProxy(reqCh, statCh, proxy, ctx, handlerWg)
//	}
//
//	c := &Container{
//		reqCh:          reqCh,
//		statCh:         statCh,
//		containerId:    containerId,
//		component:      component,
//		healthCheckUrl: healthCheckUrl,
//		handlerWg:      handlerWg,
//		runWg:          runWg,
//		ctx:            ctx,
//		cancel:         cancelFx,
//		lastReqTime:    time.Now(),
//		lock:           &sync.Mutex{},
//		dockerClient:   dockerClient,
//		activity:       []v1.ComponentActivity{},
//	}
//	c.runWg.Add(1)
//	go c.Run()
//
//	return c, nil
//}
//
//type Container struct {
//	reqCh          chan *MaelRequest
//	statCh         chan time.Duration
//	containerId    string
//	component      v1.Component
//	healthCheckUrl *url.URL
//	runWg          *sync.WaitGroup
//	handlerWg      *sync.WaitGroup
//	ctx            context.Context
//	cancel         context.CancelFunc
//	lastReqTime    time.Time
//	totalRequests  int64
//	activity       []v1.ComponentActivity
//	lock           *sync.Mutex
//	dockerClient   *docker.Client
//}
//
//func (c *Container) Stop(reason string) {
//	c.cancel()
//	c.runWg.Wait()
//	err := stopContainer(c.dockerClient, c.containerId, c.component.Name, strconv.Itoa(int(c.component.Version)), reason)
//	if err != nil && !docker.IsErrContainerNotFound(err) && !common.IsErrRemovalInProgress(err) {
//		log.Error("container: error stopping container", "err", err, "containerId", c.containerId[0:8],
//			"component", c.component.Name)
//	}
//}
//
//func (c *Container) HealthCheck() bool {
//	return getUrlOK(c.healthCheckUrl)
//}
//
//func (c *Container) componentInfo() v1.ComponentInfo {
//	c.lock.Lock()
//	info := v1.ComponentInfo{
//		ComponentName:     c.component.Name,
//		MaxConcurrency:    c.component.MaxConcurrency,
//		MemoryReservedMiB: c.component.Docker.ReserveMemoryMiB,
//		LastRequestTime:   common.TimeToMillis(c.lastReqTime),
//		TotalRequests:     c.totalRequests,
//		Activity:          c.activity,
//	}
//	c.lock.Unlock()
//	return info
//}
//
//func (c *Container) bumpReqStats() {
//	c.lock.Lock()
//	c.lastReqTime = time.Now()
//	c.totalRequests++
//	c.lock.Unlock()
//}
//
//func (c *Container) appendActivity(previousTotal int64, rolloverStartTime time.Time, totalDuration time.Duration) int64 {
//	concurrency := float64(totalDuration) / float64(time.Now().Sub(rolloverStartTime))
//	c.lock.Lock()
//	currentTotal := c.totalRequests
//	activity := v1.ComponentActivity{
//		Requests:    currentTotal - previousTotal,
//		Concurrency: concurrency,
//	}
//	c.activity = append([]v1.ComponentActivity{activity}, c.activity...)
//	if len(c.activity) > 10 {
//		c.activity = c.activity[0:10]
//	}
//	c.lock.Unlock()
//	return currentTotal
//}
//
//func (c *Container) Run() {
//
//	healthCheckSecs := c.component.Docker.HttpHealthCheckSeconds
//	if healthCheckSecs <= 0 {
//		healthCheckSecs = 10
//	}
//	healthCheckMaxFailures := c.component.Docker.HttpHealthCheckMaxFailures
//	if healthCheckMaxFailures <= 0 {
//		healthCheckMaxFailures = 1
//	}
//	healthCheckFailures := int64(0)
//
//	healthCheckTicker := time.Tick(time.Duration(healthCheckSecs) * time.Second)
//	concurrencyTicker := time.Tick(time.Second * 20)
//
//	var durationSinceRollover time.Duration
//	rolloverStartTime := time.Now()
//	previousTotalRequests := int64(0)
//
//	done := make(chan bool, 1)
//	go func() {
//		for {
//			select {
//			case dur := <-c.statCh:
//				c.bumpReqStats()
//				durationSinceRollover += dur
//			case <-concurrencyTicker:
//				previousTotalRequests = c.appendActivity(previousTotalRequests, rolloverStartTime, durationSinceRollover)
//				rolloverStartTime = time.Now()
//				durationSinceRollover = 0
//			case <-healthCheckTicker:
//				if getUrlOK(c.healthCheckUrl) {
//					healthCheckFailures = 0
//				} else {
//					healthCheckFailures++
//					if healthCheckFailures >= healthCheckMaxFailures {
//						log.Error("container: health check failed. stopping container", "containerId", c.containerId[0:8],
//							"component", c.component.Name, "failures", healthCheckFailures)
//						go c.Stop("health check failed")
//						healthCheckFailures = 0
//					} else {
//						log.Warn("container: health check failed", "failures", healthCheckFailures,
//							"maxFailures", healthCheckMaxFailures)
//					}
//				}
//			case <-done:
//				log.Info("container: run loop exiting", "containerId", c.containerId[0:8], "component", c.component.Name)
//				return
//			}
//		}
//	}()
//
//	<-c.ctx.Done()
//	log.Info("container: waiting for handlers to finish", "containerId", c.containerId[0:8],
//		"component", c.component.Name)
//	c.handlerWg.Wait()
//	done <- true
//	c.runWg.Done()
//}
//
///////////////////////////////////////////////////////////////
