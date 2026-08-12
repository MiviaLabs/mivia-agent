package controller

import (
	"context"
	"errors"
	"strings"
	"sync"
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
// callers (simulating the two surfaces racing each other) converge on the
// same tombstones instead of racing to create different children.
//
// This drives the two calls from separate goroutines, started concurrently
// against a not-yet-terminal run, so the second call can actually contend
// with the first's in-flight reconciliation and run-status CAS instead of
// hitting the cheap already-terminal early exit. Exactly one caller may win
// the run-status CAS and report the canceled attempt; the loser must either
// see the idempotent already-terminal no-op (nil, nil) or the CAS conflict
// the ledger reports on a version race (workflowledger.ErrConflict) - never
// a duplicate tombstone, a panic, or a second canceled attempt.
func TestCancelRunWithAttemptsWithClaim_PanelConcurrentCancelSurfacesCoalesce(t *testing.T) {
	ctrl, repo, _, _, ctx := panelCancelReconcileFixture(t, `{}`, `{}`)
	runner := ctrl.Runner.(*CoordinatorRunner)

	type result struct {
		canceled []workflowledger.StepAttempt
		err      error
	}
	results := make([]result, 2)

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	done.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer done.Done()
			start.Wait()
			canceled, err := CancelRunWithAttemptsWithClaim(ctx, repo, runner.Coordinator, ctrl.RunID, ctrl.Holder)
			results[i] = result{canceled: canceled, err: err}
		}(i)
	}
	start.Done()
	done.Wait()

	totalCanceled := 0
	for _, r := range results {
		if r.err != nil && !errors.Is(r.err, workflowledger.ErrConflict) {
			t.Fatalf("unexpected error from a concurrent cancel caller: %v", r.err)
		}
		if len(r.canceled) > 1 {
			t.Fatalf("a single caller reported more than one canceled attempt: %+v", r.canceled)
		}
		totalCanceled += len(r.canceled)
	}
	if totalCanceled != 1 {
		t.Fatalf("total canceled attempts across both concurrent callers = %d, want exactly 1 (no duplicate tombstones)", totalCanceled)
	}

	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusCanceled {
		t.Fatalf("run status = %q, want canceled", run.Status)
	}
}

// failingMemberCancelCoordinator implements PanelCancelCoordinator and fails
// CancelOrTombstoneMember for one designated member ID, so tests can drive
// ReconcilePanelCancellation's own per-child error wrapping instead of
// re-deriving the wrap locally.
type failingMemberCancelCoordinator struct {
	failMemberID string
	err          error
}

func (f *failingMemberCancelCoordinator) CancelOrTombstoneMember(ctx context.Context, attemptID, memberID string) (bool, error) {
	if memberID == f.failMemberID {
		return false, f.err
	}
	return true, nil
}

func (f *failingMemberCancelCoordinator) CancelOrTombstoneSynthesis(context.Context, string) (bool, error) {
	return true, nil
}

// Regression: ReconcilePanelCancellation wraps a per-child cancel failure as
// fmt.Errorf("%w: member %q: %v", ErrCancelBlocked, ...) (panel_cancel.go).
// This drives that real call site through a failing PanelCancelCoordinator
// so a regression from %w to %v there would break errors.Is and be caught,
// unlike a test that only re-derives the wrap locally.
func TestReconcilePanelCancellation_WrapsChildFailureAsErrCancelBlocked(t *testing.T) {
	ctrl, repo, _, attempt, ctx := panelCancelReconcileFixture(t, `{}`, `{}`)

	underlying := errors.New("claim held by another executor")
	failing := &failingMemberCancelCoordinator{failMemberID: "security", err: underlying}

	_, allTerminal, err := ReconcilePanelCancellation(ctx, repo, failing, ctrl.RunID, ctrl.Holder, attempt.AttemptID)
	if allTerminal {
		t.Fatal("a blocked member cancel must not report allTerminal")
	}
	if !errors.Is(err, ErrCancelBlocked) {
		t.Fatalf("errors.Is(%v, ErrCancelBlocked) = false, want true", err)
	}
	if !strings.Contains(err.Error(), "security") {
		t.Fatalf("wrapped error %q lost the member ID detail", err.Error())
	}
	if !strings.Contains(err.Error(), underlying.Error()) {
		t.Fatalf("wrapped error %q lost the underlying cancel error detail", err.Error())
	}
}
