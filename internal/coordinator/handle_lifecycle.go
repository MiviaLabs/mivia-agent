package coordinator

import "time"

func (c *coordinator) evictHandleAfterTerminal(key string, h *RunHandle) {
	<-h.done
	timer := time.NewTimer(c.handleRetention)
	defer timer.Stop()
	<-timer.C
	c.handlesMu.Lock()
	if c.handles[key] == h {
		delete(c.handles, key)
	}
	if h != nil && c.handlesByRun[h.runID] == h {
		delete(c.handlesByRun, h.runID)
	}
	c.handlesMu.Unlock()
}
