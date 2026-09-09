package coordinator

import (
	"context"
	"testing"
)

// The RunHandle accessors that a caller may reach with no handle at all.
//
// Every one of them is documented "nil-receiver safe", and that claim is
// load-bearing rather than decorative: the coordinator hands back a nil
// *RunHandle on several admission refusals (a duplicate spawn that lost the
// race, a run this process does not own), and inspection surfaces -
// inspect_agents, the panel actor-permit check, the referral parker - call
// these accessors on whatever they were handed. A nil dereference there is a
// panic in the host process over a run that simply is not there.
//
// The nil answer also has to be the SAFE one, not merely non-panicking:
//
//   - policy() must be NoRetry, so an absent handle never re-dispatches work.
//   - LocalActor() must be false, so an absent handle never claims this
//     process owns execution and never consumes an actor permit.
//   - isNonInteractiveParent() must be false, so a parked question is not
//     declined on behalf of a parent that was never asked.
//   - mustFailInterrupted() must be false, so an absent handle never fails
//     tasks that recovery could still requeue.
//   - poolContext() must be a live context, never nil: every caller passes it
//     straight into a dispatch, and a nil context panics inside the pool.
//
// The non-nil half of each case is asserted in the same test, because an
// accessor that returned the nil answer unconditionally would satisfy the nil
// half alone and is exactly the regression this pins.

func TestRunHandleAccessorsOnANilHandleReturnTheSafeAnswer(t *testing.T) {
	var h *RunHandle

	if got := h.policy(); got != NoRetry {
		t.Errorf("nil handle policy() = %#v, want NoRetry: an absent run must never be re-dispatched", got)
	}
	if h.LocalActor() {
		t.Error("nil handle LocalActor() = true; an absent run must not claim this process executes it")
	}
	if h.isNonInteractiveParent() {
		t.Error("nil handle isNonInteractiveParent() = true; an absent run must not decline its child's question")
	}
	if h.mustFailInterrupted() {
		t.Error("nil handle mustFailInterrupted() = true; an absent run must not fail tasks recovery could requeue")
	}
	if ctx := h.poolContext(); ctx == nil {
		t.Fatal("nil handle poolContext() = nil; every caller dispatches with it and a nil context panics in the pool")
	} else if err := ctx.Err(); err != nil {
		t.Errorf("nil handle poolContext().Err() = %v, want a live context", err)
	}
}

func TestRunHandleAccessorsReportTheStoredValues(t *testing.T) {
	policy := RetryPolicy{MaxRetries: 2}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	//nolint:containedctx // the handle stores the pool context by design; poolContext() is its accessor.
	h := &RunHandle{
		retryPolicy:          policy,
		localActor:           true,
		nonInteractiveParent: true,
		failInterrupted:      true,
		poolCtx:              ctx,
	}

	if got := h.policy(); got != policy {
		t.Errorf("policy() = %#v, want the stored %#v", got, policy)
	}
	if !h.LocalActor() {
		t.Error("LocalActor() = false for a handle stamped localActor=true")
	}
	if !h.isNonInteractiveParent() {
		t.Error("isNonInteractiveParent() = false for a handle stamped nonInteractiveParent=true")
	}
	if !h.mustFailInterrupted() {
		t.Error("mustFailInterrupted() = false for a handle stamped failInterrupted=true; " +
			"recovery would requeue tasks the run policy asked to fail")
	}
	if got := h.poolContext(); got != ctx {
		t.Errorf("poolContext() = %v, want the stored pool context", got)
	}

	// A handle that exists but never had a pool context (created before
	// executeRun installs one) still has to answer with a usable context
	// rather than the nil field.
	empty := &RunHandle{}
	if got := empty.poolContext(); got == nil || got.Err() != nil {
		t.Errorf("poolContext() on a handle with no pool context = %v, want a live background context", got)
	}
	if got := empty.policy(); got != (RetryPolicy{}) {
		t.Errorf("policy() on a bare handle = %#v, want the zero policy it stores", got)
	}
	if empty.LocalActor() || empty.isNonInteractiveParent() || empty.mustFailInterrupted() {
		t.Error("a bare handle reported one of localActor/nonInteractiveParent/failInterrupted as true")
	}
}

// TestRunHandleAccessorsAreSafeUnderConcurrentWrites drives every locking
// accessor against a concurrent writer of the same fields. The comment on
// isNonInteractiveParent states the reason the reads take h.mu at all -
// ParkQuestion runs on a pool worker while executeResumedRun rewrites poolCtx
// - so the lock is a contract, not an ornament, and -race proves it.
func TestRunHandleAccessorsAreSafeUnderConcurrentWrites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &RunHandle{}

	writes := make(chan struct{})
	go func() {
		defer close(writes)
		for i := 0; i < 200; i++ {
			h.mu.Lock()
			h.poolCtx = ctx
			h.localActor = i%2 == 0
			h.nonInteractiveParent = i%2 == 0
			h.failInterrupted = i%2 == 0
			h.retryPolicy = RetryPolicy{MaxRetries: i % 3}
			h.mu.Unlock()
		}
	}()
	for i := 0; i < 200; i++ {
		_ = h.policy()
		_ = h.LocalActor()
		_ = h.isNonInteractiveParent()
		_ = h.mustFailInterrupted()
		if got := h.poolContext(); got == nil {
			t.Fatal("poolContext() returned nil while a writer was installing one")
		}
	}
	<-writes
}
