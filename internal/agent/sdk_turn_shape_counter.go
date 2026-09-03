package agent

import (
	"sync"
)

// turnShapeCounter is the per-Run shared budget. One instance is
// created per RunAgentLoopOnce call and referenced by every wrapped
// tool, so sequential calls see the bytes their siblings spent.
// Under parallel dispatch (MaxConcurrentTools > 1), an internal
// broadcast channel serializes shaping in call index order so shaping
// remains deterministic (F4).
//
// The signal is a channel that the broadcaster swaps on every wake
// and the abort path closes once: a waiter that re-enters the wait
// after waking from a normal advance re-selects on the new channel
// (the old one is now garbage); a waiter that re-enters after an
// abort/cancel observes the closed channel and exits. This replaces
// sync.Cond.Wait, which has no context awareness and would strand a
// goroutine indefinitely when ctx cancels mid-batch (no SDK
// iteration boundary reaches the host to broadcast it).
type turnShapeCounter struct {
	mu        sync.Mutex
	signal    chan struct{} // closed = abort; swapped = normal advance
	nextIndex int
	charged   int
	// previewReserve mirrors shapeBatch's tailPreviewReserveBytes pool for
	// the SDK's sequential-charging path: a small, turn-size-independent
	// budget so post-exhaustion results get a short preview instead of a
	// bare "kept 0" notice (see zeroBudgetPreviewBytes).
	previewReserve int
	aborted        bool
	closedByAbort  bool // tracks who owns the close of the CURRENT signal channel
	// inFlight counts calls inside turnShapeWrapper.Run, and blocked counts
	// those parked in waitForOrderingSlot. Their difference is the number of
	// calls that can still advance nextIndex, which is how a waiter tells "an
	// earlier call is still working" from "the index I am waiting for will
	// never arrive". The SDK skips whole indices - a plan marked duplicate is
	// never dispatched, and decodeAndRun rejects unknown, denied, and
	// schema-invalid calls before the registry sees them - so waiting on an
	// exact predecessor index is not a safe assumption.
	inFlight int
	blocked  int
}

func newTurnShapeCounter() *turnShapeCounter {
	return &turnShapeCounter{signal: make(chan struct{}), previewReserve: tailPreviewReserveBytes}
}

// broadcast swaps the signal channel. Old waiters see the closed
// channel and re-select; new waiters see the fresh channel.
func (c *turnShapeCounter) broadcast() {
	c.mu.Lock()
	if c.aborted {
		c.mu.Unlock()
		return
	}
	ch := c.signal
	close(ch)
	c.signal = make(chan struct{})
	c.mu.Unlock()
}

// abort closes the signal channel permanently. Subsequent waiters
// observe the closed channel and exit immediately. closedByAbort
// records that THIS abort owns the close, so a racing reset does not
// try to close the same channel twice.
func (c *turnShapeCounter) abort() {
	c.mu.Lock()
	if !c.aborted {
		c.aborted = true
		c.closedByAbort = true
		close(c.signal)
	}
	c.mu.Unlock()
}

// enter records a call as in flight and therefore able to advance the
// ordering gate. Run increments before executing its tool, so a call whose
// tool is still running counts as a predecessor a later index must wait for.
func (c *turnShapeCounter) enter() {
	c.mu.Lock()
	c.inFlight++
	c.mu.Unlock()
}

// leave releases the in-flight slot and opens the ordering gate past this
// call's index whether or not the call shaped anything. Run returns early when
// its tool errors or hands back a non-string body, and those exits used to
// leave the gate shut on an index that had already finished. The broadcast is
// unconditional: waiters gate on the in-flight count as well as on nextIndex,
// and both just changed.
func (c *turnShapeCounter) leave(callIndex int) {
	c.mu.Lock()
	c.inFlight--
	if callIndex >= 0 && callIndex >= c.nextIndex {
		c.nextIndex = callIndex + 1
	}
	c.mu.Unlock()
	c.broadcast()
}
