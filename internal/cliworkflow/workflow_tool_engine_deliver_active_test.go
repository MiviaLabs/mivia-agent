package cliworkflow

import (
	"context"
	"testing"
	"time"
)

// setDeliverActiveWait lowers WorkflowResolutionLockWait to wait for one
// test, restoring it afterwards. It uses a longer bound than
// ShortenWorkflowResolutionLockWaitForTest's 50ms so a real (fast, local) delivery
// attempt cannot be mistaken for the wait-bound timeout in the elapsed-time
// assertions below.
func setDeliverActiveWait(t *testing.T, wait time.Duration) {
	t.Helper()
	previous := WorkflowResolutionLockWait
	WorkflowResolutionLockWait = wait
	t.Cleanup(func() { WorkflowResolutionLockWait = previous })
}

// TestSessionDeliverWaitsForActiveDoneThenProceeds pins the delivery_pending
// gate in sessionWorkflowEngine.Deliver (workflow_tool_engine_ops.go:242-251):
// when an active in-process run goroutine is still recorded for the run, an
// already delivery_pending run's Deliver call waits on active.done instead of
// immediately contending for the execution flock. When the goroutine finishes
// (closes done) well inside the wait bound, Deliver proceeds promptly rather
// than sleeping out the whole bound.
func TestSessionDeliverWaitsForActiveDoneThenProceeds(t *testing.T) {
	const wait = 400 * time.Millisecond
	setDeliverActiveWait(t, wait)
	root, storePath, config, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)

	e := NewSessionWorkflowEngine(root, config)
	done := make(chan struct{})
	e.mu.Lock()
	e.active[runID] = &sessionActiveRun{done: done}
	e.mu.Unlock()
	timer := time.AfterFunc(20*time.Millisecond, func() { close(done) })
	defer timer.Stop()

	// Observe the active.done gate's own resolution time directly, not total
	// Deliver duration: the latter also includes executeWorkflowDeliver's
	// ledger/git I/O, which is unbounded under a loaded machine and previously
	// made this assertion flaky (observed 1.27s in CI against a 400ms bound)
	// for a reason unrelated to the gate behavior being tested.
	waitCh := make(chan time.Duration, 1)
	previousObserved := sessionDeliverActiveWaitObserved
	sessionDeliverActiveWaitObserved = func(d time.Duration) { waitCh <- d }
	t.Cleanup(func() { sessionDeliverActiveWaitObserved = previousObserved })

	if _, err := e.Deliver(context.Background(), runID, true); err != nil {
		t.Fatalf("Deliver = %v", err)
	}

	// waitCh receiving a value at all is the proof that Deliver actually
	// entered the select on active.done rather than skipping the gate
	// entirely - that does not need a duration bound. A duration lower bound
	// was tried and removed: the 20ms close(done) timer starts at test setup,
	// before Deliver's preceding ledger read, so under load that read alone
	// can consume most of the 20ms budget before the select is even entered
	// (observed 7.28ms remaining in CI) without the gate having been skipped.
	var gateWait time.Duration
	select {
	case gateWait = <-waitCh:
	default:
		t.Fatal("active.done gate was never observed; want Deliver to select on active.done")
	}

	// The upper bound is still meaningful: it distinguishes "reacted to
	// active.done closing" from "fell through the full wait timeout" (the
	// scenario TestSessionDeliverFallsThroughAfterActiveDoneTimeout covers).
	if gateWait >= wait {
		t.Fatalf("gate wait = %s, want Deliver to proceed once active.done closed, well under the %s wait bound", gateWait, wait)
	}
}

// TestSessionDeliverFallsThroughAfterActiveDoneTimeout pins that Deliver does
// not wait forever for an active run goroutine: when done never closes,
// Deliver still falls through after WorkflowResolutionLockWait and attempts
// delivery instead of hanging.
func TestSessionDeliverFallsThroughAfterActiveDoneTimeout(t *testing.T) {
	const wait = 200 * time.Millisecond
	setDeliverActiveWait(t, wait)
	root, storePath, config, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)

	e := NewSessionWorkflowEngine(root, config)
	done := make(chan struct{}) // never closed
	e.mu.Lock()
	e.active[runID] = &sessionActiveRun{done: done}
	e.mu.Unlock()

	start := time.Now()
	if _, err := e.Deliver(context.Background(), runID, true); err != nil {
		t.Fatalf("Deliver = %v", err)
	}
	elapsed := time.Since(start)

	if elapsed < wait {
		t.Fatalf("elapsed = %s, want Deliver to wait at least the %s bound before falling through", elapsed, wait)
	}
}
