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

// releaseIDKeyed removes the owner's active marker and drains every ID-keyed
// waiter that registered after the owner's terminal delivery, in ONE critical
// section. A waiter that saw the marker is answered with the owner's final
// result; a caller that sees no marker becomes a new owner instead. The caller
// must be the owner reservation (non-dup, non-SkipDedup).
func (d *Dispatcher) releaseIDKeyed(req Request, result Result) {
	d.mu.Lock()
	delete(d.active, req.ID)
	d.deliverIDWaitersLocked(req.ID, result)
	d.mu.Unlock()
}
