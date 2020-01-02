package router

import (
	"context"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	"github.com/stretchr/testify/assert"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestInitialState(t *testing.T) {
	r := newRouter()
	assert.Equal(t, "foo", r.componentName)
	assert.Equal(t, int64(0), r.inflightReqs)
	assert.Equal(t, int64(0), r.activeHandlers)
	assert.Equal(t, StateOff, r.state)
}

func TestHandlerStart(t *testing.T) {
	r := newRouter()
	ch := r.HandlerStartRemote()
	assert.NotNil(t, ch)
	assert.Equal(t, int64(0), r.inflightReqs)
	assert.Equal(t, int64(1), r.activeHandlers)
	assert.Equal(t, StateOn, r.state)
}

func TestHandlerStop(t *testing.T) {
	r := newRouter()

	// start 2 handlers
	r.HandlerStartRemote()
	r.HandlerStartLocal()
	assert.Equal(t, int64(2), r.activeHandlers)

	// stop 1 - still on
	r.HandlerStop()
	assert.Equal(t, int64(1), r.activeHandlers)
	assert.Equal(t, StateOn, r.state)

	// stop 1 - off
	r.HandlerStop()
	assert.Equal(t, int64(0), r.activeHandlers)
	assert.Equal(t, StateOff, r.state)
}

func TestStateLabels(t *testing.T) {
	assert.Equal(t, "on", StateOn.String())
	assert.Equal(t, "off", StateOff.String())
	assert.Equal(t, "pending", StatePending.String())
}

func TestRouteIncrementsInflightCount(t *testing.T) {
	r := newRouter()
	go r.Route(context.Background(), newReq())
	go r.Route(context.Background(), newReq())
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, int64(2), r.GetInflightReqs())
	runNoOpRemoteHandler(r)
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, int64(0), r.GetInflightReqs())
}

func TestRouteBalancesInflightCount(t *testing.T) {
	reqCount := 500
	r := newRouter()
	assert.Equal(t, int64(0), r.GetInflightReqs())

	// run x requests through router
	wg := &sync.WaitGroup{}
	for i := 0; i < reqCount; i++ {
		wg.Add(1)
		go func() {
			r.Route(context.Background(), newReq())
			wg.Done()
		}()
	}

	// add no-op handler
	runNoOpRemoteHandler(r)

	//  block until all are sent to chan
	wg.Wait()
	r.HandlerStop()
	assert.Equal(t, true, r.DestroyIfIdle())

	// verify inflight is still zero after completion
	assert.Equal(t, int64(0), r.GetInflightReqs())
}

func TestCallsPlacementFuncWhenPending(t *testing.T) {
	startCalls := make(map[string]int)
	startComponentFx := func(componentName string) {
		startCalls[componentName] = startCalls[componentName] + 1
	}
	r := newRouter().WithStartComponentFunc(startComponentFx)
	go r.Route(context.Background(), newReq())
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, StatePending, r.state)
	assert.Equal(t, int64(1), r.inflightReqs)
	assert.Equal(t, map[string]int{r.componentName: 1}, startCalls)
}

func TestRouteLocal(t *testing.T) {
	r := newRouter()

	var localReqs int64
	var remoteReqs int64

	localCh := r.HandlerStartLocal()
	go func() {
		for req := range localCh {
			atomic.AddInt64(&localReqs, 1)
			req.Rw.WriteHeader(200)
		}
	}()
	remoteCh := r.HandlerStartRemote()
	go func() {
		for req := range remoteCh {
			atomic.AddInt64(&remoteReqs, 1)
			req.Rw.WriteHeader(200)
		}
	}()

	// send 20 requests - preferring local
	for i := 0; i < 20; i++ {
		time.Sleep(3 * time.Millisecond)
		req := newReq()
		req.PreferLocal = true
		r.Route(context.Background(), req)
	}
	time.Sleep(3 * time.Millisecond)

	// all 20 should have been handled locally
	assert.Equal(t, int64(20), localReqs)
	assert.Equal(t, int64(0), remoteReqs)

	// send another 20 without prefer local, and no sleep (so local handler will be busy at points)
	for i := 0; i < 20; i++ {
		req := newReq()
		req.PreferLocal = false
		r.Route(context.Background(), req)
	}
	time.Sleep(3 * time.Millisecond)

	// should have some remote reqs
	assert.True(t, remoteReqs > 0)
}

///////////////////////////////////////////////////////

func newRouter() *Router {
	return NewRouter("foo", func(componentName string) {})
}

func newReq() *Request {
	comp := &v1.Component{
		Name:   "foo",
		Docker: &v1.DockerComponent{HttpStartHealthCheckSeconds: 5},
	}
	rw := httptest.NewRecorder()
	req, err := http.NewRequest("GET", "/", nil)
	if err != nil {
		panic(err)
	}
	return NewRequest(req, rw, comp, false)
}

func runNoOpRemoteHandler(r *Router) {
	runNoOpHandler(r.HandlerStartRemote())
}

func runNoOpLocalHandler(r *Router) {
	runNoOpHandler(r.HandlerStartLocal())
}

func runNoOpHandler(reqCh <-chan *Request) {
	go func() {
		for req := range reqCh {
			req.Rw.WriteHeader(200)
		}
	}()
}
