package component

import (
	log "github.com/mgutz/logxi/v1"
	"math/rand"
	"net/http/httputil"
	"net/url"
	"reflect"
)

type reqHandler func(req *RequestInput)

// key=peerUrl, value=# of running instances
type remoteNodeCounts map[string]int

type componentHandler struct {
	handler reqHandler
	local   bool
}

func newComponentRing(componentName string, myNodeId string) *componentRing {
	return &componentRing{
		componentName: componentName,
		myNodeId:      myNodeId,
		idx:           0,
		localHandler:  nil,
		localCount:    0,
		handlers:      nil,
		remoteNodes:   nil,
	}
}

type componentRing struct {
	componentName string
	myNodeId      string
	idx           int
	localCount    int
	localHandler  *componentHandler
	handlers      []*componentHandler
	remoteNodes   remoteNodeCounts
}

func (c *componentRing) size() int {
	return len(c.handlers)
}

func (c *componentRing) next(preferLocal bool) reqHandler {
	if len(c.handlers) == 0 {
		return nil
	} else if preferLocal && c.localHandler != nil {
		return c.localHandler.handler
	} else {
		return c.handlers[c.nextIdx()].handler
	}
}

func (c *componentRing) nextIdx() int {
	if c.idx >= len(c.handlers) {
		c.idx = 0
	}
	i := c.idx
	c.idx++
	return i
}

func (c *componentRing) setLocalCount(localCount int, handlerFx reqHandler) {
	if c.localCount == localCount {
		// no-op
		return
	}

	if localCount <= 0 {
		c.localHandler = nil
	} else if c.localHandler == nil {
		c.localHandler = &componentHandler{
			handler: handlerFx,
			local:   true,
		}
	}

	keep := make([]*componentHandler, 0)
	for _, h := range c.handlers {
		if !h.local {
			keep = append(keep, h)
		}
	}
	for i := 0; i < localCount; i++ {
		keep = append(keep, c.localHandler)
	}
	c.localCount = localCount
	c.handlers = keep
	c.countAndShuffle()
}

func (c *componentRing) setRemoteNodes(remoteNodes remoteNodeCounts) {
	if c.remoteNodes != nil && reflect.DeepEqual(c.remoteNodes, remoteNodes) {
		// no change
		return
	}

	for peerUrl, count := range remoteNodes {
		target, err := url.Parse(peerUrl)
		if err == nil {
			proxy := httputil.NewSingleHostReverseProxy(target)
			compName := c.componentName
			handler := func(req *RequestInput) {
				relayPath := req.Req.Header.Get("MAELSTROM-RELAY-PATH")
				if relayPath == "" {
					relayPath = c.myNodeId
				} else {
					relayPath = relayPath + "|" + c.myNodeId
				}
				req.Req.Header.Set("MAELSTROM-COMPONENT", compName)
				req.Req.Header.Set("MAELSTROM-RELAY-PATH", relayPath)
				proxy.ServeHTTP(req.Resp, req.Req)
				req.Done <- true
			}
			compHandler := &componentHandler{
				handler: handler,
				local:   false,
			}
			for i := 0; i < count; i++ {
				c.handlers = append(c.handlers, compHandler)
			}
		} else {
			log.Error("router: cannot create peer url", "err", err, "url", peerUrl)
		}
	}
	c.remoteNodes = remoteNodes
	c.countAndShuffle()
}

func (c *componentRing) countAndShuffle() {
	localCount := 0
	remoteCount := 0
	for _, h := range c.handlers {
		if h.local {
			localCount++
		} else {
			remoteCount++
		}
	}
	c.localCount = localCount
	rand.Shuffle(len(c.handlers), func(i, j int) { c.handlers[i], c.handlers[j] = c.handlers[j], c.handlers[i] })
	log.Info("ring: updated", "local", localCount, "remote", remoteCount, "component", c.componentName)
}
