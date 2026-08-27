package cliworkflow

// Session-engine Cancel tests: the refusal gates (terminal idempotency,
// delivery_pending refusal, claim preservation, F13's "do not stop the drive
// a refused cancel points at") and the stop-before-lock ordering that keeps
// a live run cancellable. Split from workflow_tool_engine_test.go to stay
// under the structure policy's per-file line ceiling.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestSessionEngineCancelPreservesDeliveryPendingClaim proves the session
// engine refuses to cancel a delivery_pending run BEFORE any claim mutation:
// a fresh foreign claim (a live deliverer on this or another host) must
// survive the refused cancel. Regression: Cancel cleared the claim before
// controller.CancelRun refused delivery_pending runs, so a refused cancel
// stripped the delivery claim and enabled double-publish.
func TestSessionEngineCancelPreservesDeliveryPendingClaim(t *testing.T) {
	root, storePath, configPath, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, configPath)
	repo := openDeliveryStore(t, storePath)
	if err := repo.ClaimRun(context.Background(), runID, "foreign-cancel-host"); err != nil {
		t.Fatal(err)
	}

	e := NewSessionWorkflowEngine(root, configPath)
	_, err := e.Cancel(context.Background(), runID)
	if err == nil || !strings.Contains(err.Error(), "deliver") {
		t.Fatalf("Cancel of delivery_pending = %v, want delivery refusal", err)
	}
	// The foreign claim must survive the refused cancel.
	if err := repo.ClaimRun(context.Background(), runID, "probe"); !errors.Is(err, workflowledger.ErrClaimHeld) {
		t.Fatalf("claim after refused cancel = %v, want still ErrClaimHeld", err)
	}
	fresh, getErr := repo.GetRun(context.Background(), runID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if fresh.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status after refused cancel = %q, want delivery_pending", fresh.Status)
	}
}

// TestSessionEngineCancelDeliveryPendingDoesNotStopActiveDrive is the
// regression test for F13: Cancel on a delivery_pending run must refuse
// BEFORE stopping any in-process controller for that run_id. Previously
// stopActive ran unconditionally up front, so a refused cancel still
// canceled the session's own live stack-drive goroutine out from under the
// run - the caller sees an error implying no-op, but the drive was killed.
func TestSessionEngineCancelDeliveryPendingDoesNotStopActiveDrive(t *testing.T) {
	root, storePath, configPath, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, configPath)
	repo := openDeliveryStore(t, storePath)
	if err := repo.ClaimRun(context.Background(), runID, "foreign-cancel-host"); err != nil {
		t.Fatal(err)
	}

	e := NewSessionWorkflowEngine(root, configPath)
	stopped := false
	done := make(chan struct{})
	close(done)
	e.active[runID] = &sessionActiveRun{
		cancel:  func() { stopped = true },
		done:    done,
		closeFn: func() {},
	}

	_, err := e.Cancel(context.Background(), runID)
	if err == nil || !strings.Contains(err.Error(), "deliver") {
		t.Fatalf("Cancel of delivery_pending = %v, want delivery refusal", err)
	}
	if stopped {
		t.Fatal("Cancel stopped the in-process controller for a refused delivery_pending cancel")
	}
}

// TestSessionEngineCancelStopsActiveRunUnderItsOwnFlock is the companion
// regression test for the F13 fix: an actively running in-session workflow
// holds the per-run execution flock for its whole lifetime, so Cancel MUST
// stop the run BEFORE acquiring that lock. Acquiring the lock first made
// every cancel of a live run fail with a lock-busy error after the bounded
// wait, without ever reaching stopActive - the run stayed uncancellable.
func TestSessionEngineCancelStopsActiveRunUnderItsOwnFlock(t *testing.T) {
	root, storePath, configPath, _ := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	ctx := context.Background()

	// A running run (no attempts): the state a live controller keeps.
	runID := "wfr-cancel-active"
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "compile"}})
	if err != nil {
		t.Fatal(err)
	}
	snap := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: "two-step", BaseRef: "main",
		Status: workflowledger.RunStatusPending, ActiveStepID: "write",
	}
	if err := repo.CreateRun(ctx, snap, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}

	// Model one actively running in-session workflow exactly as the engine
	// does: the run goroutine holds the per-run execution flock from
	// admission until its context is canceled (stopActive).
	releaseRun := make(chan struct{})
	activeDone := make(chan struct{})
	acquired := make(chan struct{})
	var stopOnce sync.Once
	stopRun := func() { stopOnce.Do(func() { close(releaseRun) }) }
	go func() {
		finish, ferr := BeginWorkflowExecution(root, storePath, runID)
		if ferr != nil {
			close(activeDone)
			return
		}
		close(acquired)
		<-releaseRun
		finish()
		close(activeDone)
	}()
	defer stopRun()
	t.Cleanup(func() { <-activeDone })
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("run goroutine never acquired the execution lock")
	}

	e := NewSessionWorkflowEngine(root, configPath)
	stopped := false
	e.active[runID] = &sessionActiveRun{
		cancel:  func() { stopped = true; stopRun() },
		done:    activeDone,
		closeFn: func() {},
	}
	ShortenWorkflowResolutionLockWaitForTest(t)

	result, err := e.Cancel(ctx, runID)
	if err != nil {
		t.Fatalf("Cancel of an actively running run = %v; want success (stop the run before acquiring the execution lock)", err)
	}
	if !stopped {
		t.Fatal("Cancel never stopped the in-process controller for a run it was going to cancel")
	}
	if result.Status != string(workflowledger.RunStatusCanceled) {
		t.Fatalf("Cancel result status = %q, want canceled", result.Status)
	}
	after, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusCanceled {
		t.Fatalf("run status after cancel = %q, want canceled", after.Status)
	}
}
