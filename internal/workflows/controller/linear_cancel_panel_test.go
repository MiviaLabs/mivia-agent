package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// Cancellation matrix item 1: cancel before member dispatch creates no
// nonterminal child and invokes no handler; item 12: the workflow becomes
// canceled only once every intended child is terminal.
func TestCancelRunWithAttemptsWithClaim_PanelBeforeDispatchTombstonesAndSettles(t *testing.T) {
	ctrl, repo, _, attempt, ctx := panelCancelReconcileFixture(t, `{}`, `{}`)
	runner := ctrl.Runner.(*CoordinatorRunner)

	canceled, err := CancelRunWithAttemptsWithClaim(ctx, repo, runner.Coordinator, ctrl.RunID, ctrl.Holder)
	if err != nil {
		t.Fatalf("CancelRunWithAttemptsWithClaim() error = %v", err)
	}
	if len(canceled) != 1 || canceled[0].AttemptID != attempt.AttemptID || canceled[0].Status != workflowledger.AttemptStatusCanceled {
		t.Fatalf("canceled = %+v", canceled)
	}
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusCanceled {
		t.Fatalf("run status = %q, want canceled", run.Status)
	}
	stored, err := repo.GetStepAttempt(ctx, ctrl.RunID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PanelExecution.Phase != workflowledger.PanelPhaseCancelPending {
		t.Fatalf("phase = %q, want cancel_pending", stored.PanelExecution.Phase)
	}
}

// A nil coordinator must fail closed with a clear error for a real panel
// attempt, never orphan its children silently, and never panic.
func TestCancelRunWithAttemptsWithClaim_PanelRequiresCoordinator(t *testing.T) {
	_, repo, _, attempt, ctx := panelCancelReconcileFixture(t, `{}`, `{}`)
	runID := "wfr-cancel" // panelCancelReconcileFixture uses "wfr-cancel"

	if _, err := CancelRunWithAttemptsWithClaim(ctx, repo, nil, runID, "holder-mismatch"); err == nil {
		t.Fatal("expected an error when a panel attempt needs a coordinator and none is supplied")
	}
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status == workflowledger.RunStatusCanceled {
		t.Fatal("a refused panel cancel must never settle the run canceled")
	}
	stored, err := repo.GetStepAttempt(context.Background(), runID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PanelExecution.Phase != workflowledger.PanelPhaseMembersAdmitted {
		t.Fatalf("phase = %q, want unchanged members_admitted", stored.PanelExecution.Phase)
	}
}

// Cancellation matrix item 2: cancel during members cancels every active
// member run, waiting for it to reach a terminal state before the workflow
// reports canceled.
func TestCancelRunWithAttemptsWithClaim_PanelCancelsLiveMember(t *testing.T) {
	ctrl, repo, _, attempt, ctx := panelCancelReconcileFixture(t, `{}`, `{}`)
	runner := ctrl.Runner.(*CoordinatorRunner)
	panel := workflowledger.NewPanelCoordinator(ctrl.RunID, runner.Coordinator, repo)

	handle, err := panel.EnsureMember(ctx, attempt.AttemptID, "security")
	if err != nil {
		t.Fatalf("EnsureMember() error = %v", err)
	}
	if !handle.LocalActor() {
		t.Fatal("expected the member to become a local actor")
	}

	canceled, err := CancelRunWithAttemptsWithClaim(ctx, repo, runner.Coordinator, ctrl.RunID, ctrl.Holder)
	if err != nil {
		t.Fatalf("CancelRunWithAttemptsWithClaim() error = %v", err)
	}
	if len(canceled) != 1 {
		t.Fatalf("canceled = %+v", canceled)
	}
	select {
	case <-handle.Done():
	default:
		t.Fatal("the live member must reach a terminal state before cancel settles")
	}
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusCanceled {
		t.Fatalf("run status = %q, want canceled", run.Status)
	}
}

// Cancellation matrix item 13: CLI cancel and agent-tool cancel use the same
// panel child principal, so two independent CancelRunWithAttemptsWithClaim
// callers (simulating the two surfaces, serialized by the workflow claim)
// converge on the same tombstones instead of racing to create different
// children.
func TestCancelRunWithAttemptsWithClaim_PanelConcurrentCancelSurfacesCoalesce(t *testing.T) {
	ctrl, repo, _, _, ctx := panelCancelReconcileFixture(t, `{}`, `{}`)
	runner := ctrl.Runner.(*CoordinatorRunner)

	first, err := CancelRunWithAttemptsWithClaim(ctx, repo, runner.Coordinator, ctrl.RunID, ctrl.Holder)
	if err != nil {
		t.Fatalf("first cancel error = %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first canceled = %+v", first)
	}
	// A second cancel against the now-terminal run is an idempotent no-op
	// (already-terminal short circuit), not a duplicate tombstone attempt.
	second, err := CancelRunWithAttemptsWithClaim(ctx, repo, runner.Coordinator, ctrl.RunID, ctrl.Holder)
	if err != nil {
		t.Fatalf("second cancel error = %v", err)
	}
	if second != nil {
		t.Fatalf("second canceled = %+v, want nil (idempotent no-op on a terminal run)", second)
	}
}

func TestCancelPanelAttempt_ErrCancelPendingWrapsIfNotAllTerminal(t *testing.T) {
	if !errors.Is(ErrCancelBlocked, ErrCancelBlocked) || !strings.Contains(ErrCancelBlocked.Error(), "cancel_blocked") {
		t.Fatalf("ErrCancelBlocked = %v", ErrCancelBlocked)
	}
	if !strings.Contains(ErrCancelPending.Error(), "cancel_pending") {
		t.Fatalf("ErrCancelPending = %v", ErrCancelPending)
	}
}
