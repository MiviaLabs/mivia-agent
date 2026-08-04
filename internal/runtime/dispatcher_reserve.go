package runtime

import "fmt"

// reservation is the handler and policy snapshot resolved for one invocation.
type reservation struct {
	handler Handler
	ceiling int
	allowed bool
	dup     bool
	waiter  chan Result
}

func (d *Dispatcher) reserve(req Request, inputHash string) (reservation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if previous, ok := d.fingerprints[req.ID]; ok && previous != inputHash {
		return reservation{}, fmt.Errorf("invocation id reused with different input")
	}
	if res, ok := d.turnDedupReservationLocked(req); ok {
		return res, nil
	}
	if previous, ok := d.completed[req.ID]; ok {
		previous.Metadata.Status = "duplicate"
		return reservation{dup: true, waiter: closedResult(previous)}, nil
	}
	budgetKey := req.TurnID
	if budgetKey == "" {
		budgetKey = req.ParentID
	}
	if budgetKey == "" {
		budgetKey = req.ID
	}
	if d.policy.MaxBudget > 0 && d.spent[budgetKey]+req.Budget > d.policy.MaxBudget {
		return reservation{}, fmt.Errorf("cumulative budget exceeded")
	}
	d.spent[budgetKey] += req.Budget
	res := reservation{
		handler: d.handlers[req.Kind][req.Name],
		ceiling: d.effectiveCeilingLocked(req.Kind, req.Name),
	}
	if names, ok := d.policy.Allow[req.Kind]; ok {
		res.allowed = names[req.Name]
	}
	_, res.dup = d.active[req.ID]
	res.waiter = d.waiters[req.ID]
	if res.handler != nil && !res.dup {
		d.active[req.ID] = struct{}{}
		d.waiters[req.ID] = make(chan Result, 1)
		d.fingerprints[req.ID] = inputHash
	}
	return res, nil
}
