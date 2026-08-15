package coordinator

import "time"

func (c *coordinator) evictHandleAfterTerminal(key string, h *RunHandle) {
	<-h.done
	timer := time.NewTimer(c.handleRetention)
	defer timer.Stop()
	<-timer.C
	c.handlesMu.Lock()
	// Empty-key handles (resumed runs) are never registered in the keyed map,
	// so the keyed delete must be guarded: otherwise an empty key would consult
	// c.handles[""] and could evict an unrelated entry. An empty-key handle
	// evicts only from handlesByRun.
	if key != "" && c.handles[key] == h {
		delete(c.handles, key)
	}
	if h != nil && c.handlesByRun[h.runID] == h {
		delete(c.handlesByRun, h.runID)
	}
	c.handlesMu.Unlock()
}
