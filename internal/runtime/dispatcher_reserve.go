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
	if d.closed {
		return reservation{}, errDispatcherClosed
	}
	// The fingerprint check stays FIRST: a reused ID with different input must
	// always error, even when the content would otherwise dedup. A SkipDedup
	// call never reads or writes ID-keyed dedup state, so it is exempt.
	if !req.SkipDedup {
		if previous, ok := d.fingerprints[req.ID]; ok && previous != inputHash {
			return reservation{}, fmt.Errorf("invocation id reused with different input")
		}
	}
	// Dedup reservations (in-flight or completed) return before the budget
	// block: a dedup hit means the tool 'did not run', so it must not consume
	// cumulative budget.
	if res, ok := d.turnDedupReservationLocked(req); ok {
		return res, nil
	}
	// The ID-keyed completed map dedups genuine re-delivery of the SAME call ID
	// for callers outside the turn-scoped dedup (no TurnID). For turn-scoped
	// calls the step-aware content bucket above is the dedup authority: honoring
	// the ID-keyed map here would replay a STEP-1 result for a same-ID re-issue
	// in a later step - exactly the stale-result class the step key kills. A
	// SkipDedup call never reads the map either: it opted out of being answered
	// from a recorded result.
	if key, _ := turnDedupKey(req); key == "" && !req.SkipDedup {
		if previous, ok := d.completed[req.ID]; ok {
			previous.Metadata.Status = "duplicate"
			return reservation{dup: true, waiter: closedResult(previous)}, nil
		}
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
	// A SkipDedup call is never a same-ID duplicate and never joins the
	// ID-keyed dedup state: it skips the active/waiter read AND the
	// registration, so a skipped call cannot collapse onto an in-flight owner
	// and cannot leave an active marker or waiter channel behind.
	if !req.SkipDedup {
		_, res.dup = d.active[req.ID]
		if res.dup {
			res.waiter = make(chan Result, 1)
			d.waiters[req.ID] = append(d.waiters[req.ID], res.waiter)
		}
	}
	if res.handler != nil && !res.dup && !req.SkipDedup {
		d.active[req.ID] = struct{}{}
		d.fingerprints[req.ID] = inputHash
	}
	// The call that will actually execute owns the flight key: an identical call
	// arriving while it runs waits on the in-flight entry instead of reaching
	// the handler. A duplicate (same-ID) reservation never gets here - the
	// owner's entry already exists under the same flight key.
	if !res.dup {
		if key, contentHash := turnDedupKey(req); key != "" {
			d.inFlight[key+"\x00"+contentHash] = &inFlightEntry{owner: req.ID}
		}
	}
	return res, nil
}
