package demoharness

import "github.com/MiviaLabs/mivia-agent/internal/uikit/ports"

// Pending delivers approval requests raised by an in-progress turn's
// tool.pending event, one at a time.
func (h *Harness) Pending() <-chan ports.ApprovalRequest { return h.pendingCh }

// Resolve answers the pending request named by id. A Resolve call for
// an id with no registered wait (already resolved, or never raised) is
// a silent no-op: the UI only ever resolves what it was shown.
func (h *Harness) Resolve(id string, decision ports.Decision) {
	h.mu.Lock()
	ch, ok := h.waiting[id]
	if ok {
		delete(h.waiting, id)
	}
	h.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- decision:
	default:
		// The buffered slot is already full or the waiter gave up
		// (cancel raced Resolve); either way there is nothing left to
		// deliver to.
	}
}

// registerWait opens a one-slot wait for id's decision.
func (h *Harness) registerWait(id string) chan ports.Decision {
	ch := make(chan ports.Decision, 1)
	h.mu.Lock()
	h.waiting[id] = ch
	h.mu.Unlock()
	return ch
}

// dropWait cancels a still-open wait for id, e.g. because the turn was
// cancelled before a decision arrived.
func (h *Harness) dropWait(id string) {
	h.mu.Lock()
	delete(h.waiting, id)
	h.mu.Unlock()
}
