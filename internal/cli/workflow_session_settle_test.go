package cli

import (
	"context"
	"errors"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// routeRunToSuccess builds the crash window the finding targets: the
// controller persisted the ROUTE (the completion's ToStepID="success") before
// the run-status CAS, so the run row is Status "running" with the derived
// ActiveStepID "success".
func routeRunToSuccess(t *testing.T, ctx context.Context, repo workflowledger.Repository, runID string) {
	t.Helper()
	run := workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending, ActiveStepID: "one"}
	routeRunToSuccessSnapshot(t, ctx, repo, run, []byte("{}"))
}

// routeRunToSuccessSnapshot is routeRunToSuccess with a durable admission
// snapshot, so the settle can consult the run's delivery policy.
func routeRunToSuccessSnapshot(t *testing.T, ctx context.Context, repo workflowledger.Repository, run workflowledger.RunSnapshot, snapshotJSON []byte) {
	t.Helper()
	run.Status = workflowledger.RunStatusPending
	run.ActiveStepID = "one"
	if err := repo.CreateRun(ctx, run, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{RunID: run.RunID, StepID: "one", AttemptNo: 1, AttemptID: "wfa-" + run.RunID}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	created, err := repo.GetStepAttempt(ctx, run.RunID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteStepAttempt(ctx, run.RunID, attempt.AttemptID, created.Version, workflowledger.AttemptOutcome{
		Status:   workflowledger.AttemptStatusSucceeded,
		ToStepID: "success",
	}); err != nil {
		t.Fatal(err)
	}
	after, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ActiveStepID != "success" {
		t.Fatalf("derived active step = %q, want success (route must be durable before the status CAS)", after.ActiveStepID)
	}
	if after.Status != workflowledger.RunStatusRunning {
		t.Fatalf("run status = %q, want running (status CAS not yet recorded)", after.Status)
	}
}

// A plain mid-flight run (no terminal route derived) must settle as failed:
// a controller error must reach the run row. The session engine read the
// controller's cause only to decide whether to deliver, then dropped it: the
// run stayed `running` with no explanation anywhere, so it looked alive and
// was not. The local engine already settled such a run; the two engines
// disagreed.
func TestSessionRunFailureSettlesTheRun(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := workflowledger.RunSnapshot{RunID: "wfr-session-settle", Status: workflowledger.RunStatusPending, ActiveStepID: "one"}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}

	settleSessionRunFailure(repo, run.RunID, errors.New("ledger read: database is locked"))

	after, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %q, want failed: a run whose controller stopped must not look alive", after.Status)
	}
}

// A cancelled run is left alone. Cancel settles the run itself, and a failed
// status written here would race it and win.
func TestSessionRunFailureLeavesACancelledRunAlone(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := workflowledger.RunSnapshot{RunID: "wfr-session-cancel", Status: workflowledger.RunStatusPending, ActiveStepID: "one"}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, _ := repo.GetRun(ctx, run.RunID)
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}

	settleSessionRunFailure(repo, run.RunID, context.Canceled)

	after, _ := repo.GetRun(ctx, run.RunID)
	if after.Status != workflowledger.RunStatusRunning {
		t.Fatalf("run status = %q, want running: cancel owns this run's outcome", after.Status)
	}
}

// A run whose ROUTE reached the success terminal before a storage fault struck
// is finished, not failed: PlanResume derives the terminal route from the
// durable attempt log and the settle must record succeeded, never failed —
// failing a completed run would block delivery of work that is already done.
// (When the workflow has an active delivery policy and the delivery_pending
// CAS was recorded, the run is left parked at delivery_pending; see
// TestSessionRunFailureLeavesDeliveryPendingRunAlone.)
func TestSessionRunFailureSettlesRoutedRunSucceeded(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	runID := "wfr-session-routed-success"
	routeRunToSuccess(t, ctx, repo, runID)

	settleSessionRunFailure(repo, runID, errors.New("ledger write: database is locked"))

	after, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded: the route already finished the run; failing it would block delivery", after.Status)
	}
	if after.ActiveStepID != "success" {
		t.Fatalf("run active step = %q, want success", after.ActiveStepID)
	}
}

// A delivery-active run whose route reached success but whose delivery_pending
// CAS was missed must settle at delivery_pending, NEVER succeeded: succeeded
// only exists after publication, so settling there would make `workflow
// deliver` replay a missing delivery record and the PR would never be
// published. PlanResume is a pure ledger read and cannot see the delivery
// policy, so the settle consults the durable admission snapshot, exactly as
// the resume path does (reconcileWorkflowTerminal). The non-delivery twin
// settles at succeeded; see TestSessionRunFailureSettlesRoutedRunSucceeded.
func TestSessionRunFailureSettlesRoutedRunSucceededDeliveryActive(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run, raw := deliveryAgreementFixture(t, "draft")
	run.RunID = "wfr-session-routed-success-delivery"
	routeRunToSuccessSnapshot(t, ctx, repo, run, raw)

	settleSessionRunFailure(repo, run.RunID, errors.New("ledger write: database is locked"))

	after, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending: an active delivery policy must settle at delivery_pending, never succeeded", after.Status)
	}
	if after.ActiveStepID != "success" {
		t.Fatalf("run active step = %q, want success", after.ActiveStepID)
	}
}

// A delivery_pending run belongs to the delivery path, never the failure path.
// This is the delivery-policy crash window: the controller recorded the route
// AND the delivery_pending status CAS before stopping; the settle must leave
// the parked run alone so `workflow deliver` can publish. The old settle tried
// to CAS delivery_pending->failed, which is not a valid edge.
func TestSessionRunFailureLeavesDeliveryPendingRunAlone(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	runID := "wfr-session-delivery-pending"
	routeRunToSuccess(t, ctx, repo, runID)
	running, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, running.Version, workflowledger.RunStatusDeliveryPending, nil); err != nil {
		t.Fatal(err)
	}
	before, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}

	settleSessionRunFailure(repo, runID, errors.New("ledger write: database is locked"))

	after, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending: a delivery-pending run must stay parked for delivery", after.Status)
	}
	if after.Version != before.Version {
		t.Fatalf("run version = %d, want %d: settle must not touch a delivery-pending run", after.Version, before.Version)
	}
}

// A run parked at waiting_approval must stay approvable: the human gate owns
// the outcome, and a failed status written here would strand the approval and
// remove the run from the resume path. Deliberate behaviour change: the old
// settle failed every non-terminal run, including waiting_approval.
func TestSessionRunFailureLeavesWaitingApprovalRunAlone(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := workflowledger.RunSnapshot{RunID: "wfr-session-waiting-approval", Status: workflowledger.RunStatusPending, ActiveStepID: "approve_me"}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	running, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, running.Version, workflowledger.RunStatusWaitingApproval, nil); err != nil {
		t.Fatal(err)
	}

	settleSessionRunFailure(repo, run.RunID, errors.New("ledger read: database is locked"))

	after, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("run status = %q, want waiting_approval: a parked approval must stay approvable", after.Status)
	}
}

// A run that already settled is never overwritten.
func TestSessionRunFailureDoesNotOverwriteATerminalRun(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := workflowledger.RunSnapshot{RunID: "wfr-session-terminal", Status: workflowledger.RunStatusPending, ActiveStepID: "one"}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, _ := repo.GetRun(ctx, run.RunID)
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	running, _ := repo.GetRun(ctx, run.RunID)
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, running.Version, workflowledger.RunStatusSucceeded, nil); err != nil {
		t.Fatal(err)
	}

	settleSessionRunFailure(repo, run.RunID, errors.New("late error"))

	after, _ := repo.GetRun(ctx, run.RunID)
	if after.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded: a settled run must not be overwritten", after.Status)
	}
}
