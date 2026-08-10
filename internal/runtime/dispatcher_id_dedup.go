package runtime

// completeIDKeyed records a result for the ID-keyed completed map and delivers
// it to any ID-keyed waiter. The caller must hold d.mu.
//
// The completed map is read only for calls outside turn-scoped dedup. A
// turn-scoped Tool call uses the step-aware turn bucket instead. ID-keyed
// waiter delivery remains separate for same-ID calls from a later step.
func (d *Dispatcher) completeIDKeyed(req Request, result Result) {
	if req.SkipDedup || d.closed {
		return
	}
	if key, _ := turnDedupKey(req); key == "" {
		d.completed[req.ID] = result
	}
	d.deliverIDWaitersLocked(req.ID, result)
}

// deliverIDWaitersLocked resolves every duplicate waiting for one invocation.
// The caller must hold d.mu.
func (d *Dispatcher) deliverIDWaitersLocked(id string, result Result) {
	waiters := d.waiters[id]
	delete(d.waiters, id)
	deliverWaiters(waiters, result)
}
