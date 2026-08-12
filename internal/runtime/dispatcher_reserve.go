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
	if res, ok := d.idKeyedDedupReservationLocked(req); ok {
		return res, nil
	}
	if err := d.chargeBudgetLocked(req); err != nil {
		return reservation{}, err
	}
	return d.ownerReservationLocked(req, inputHash), nil
}

// idKeyedDedupReservationLocked resolves ID-keyed dedup reservations under
// d.mu: a re-delivery of a completed call (outside the turn-scoped dedup) is
// answered from the completed map, and a same-ID call that collapses onto an
// in-flight owner is registered as a waiter. Both return before the budget
// block, matching the turn-scoped dedup invariant: a dedup hit means the tool
// did not run for this request, so it must not consume cumulative budget, and
// the owner is the only charge.
func (d *Dispatcher) idKeyedDedupReservationLocked(req Request) (reservation, bool) {
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
			return reservation{dup: true, waiter: closedResult(previous)}, true
		}
	}
	// The ID-keyed in-flight duplicate detection also returns BEFORE the
	// budget block: a same-ID call that collapses onto an active owner is a
	// dedup hit - the tool did not run for it - so it must not consume
	// cumulative budget, and the owner is the only charge. The gate is
	// !SkipDedup only (NOT turnDedupKey == ""), so a turn-scoped same-ID
	// cross-step duplicate (step bucket miss, active marker hit) keeps
	// collapsing onto the owner as it did before; only the budget charge
	// moves. The real owner still pays in chargeBudgetLocked.
	if !req.SkipDedup {
		if _, ok := d.active[req.ID]; ok {
			waiter := make(chan Result, 1)
			d.waiters[req.ID] = append(d.waiters[req.ID], waiter)
			return reservation{dup: true, waiter: waiter}, true
		}
	}
	return reservation{}, false
}

// chargeBudgetLocked charges the request's cumulative budget under d.mu and
// fails closed when the charge would exceed the policy cap. The budget key
// falls back TurnID -> ParentID -> ID, matching the owner's charge; only a
// real (non-dup) owner reaches this point, so a dedup hit never consumes
// cumulative budget.
func (d *Dispatcher) chargeBudgetLocked(req Request) error {
	budgetKey := req.TurnID
	if budgetKey == "" {
		budgetKey = req.ParentID
	}
	if budgetKey == "" {
		budgetKey = req.ID
	}
	if d.policy.MaxBudget > 0 && d.spent[budgetKey]+req.Budget > d.policy.MaxBudget {
		return fmt.Errorf("cumulative budget exceeded")
	}
	d.spent[budgetKey] += req.Budget
	return nil
}

// ownerReservationLocked resolves the handler, ceiling, and allow-list entry
// for a real (non-dup) reservation under d.mu and registers its dedup state:
// the ID-keyed active marker and fingerprint, plus the turn-scoped in-flight
// flight key when the call participates in the turn-scoped dedup.
func (d *Dispatcher) ownerReservationLocked(req Request, inputHash string) reservation {
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
	// and cannot leave an active marker or waiter channel behind. The dup
	// classification itself happens before the budget block (in
	// idKeyedDedupReservationLocked), so a reservation that reaches this point
	// is a real owner.
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
	return res
}
