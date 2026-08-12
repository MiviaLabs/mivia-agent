package controller

import (
	"context"
	"errors"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func panelCancelReconcileFixture(t *testing.T, memberReport, synthesisOutput string) (*LinearController, workflowledger.Repository, workflowledger.RunSnapshot, workflowledger.StepAttempt, context.Context) {
	t.Helper()
	ctrl, repo, step := panelSynthesisFixture(t, "wfr-cancel", memberReport, synthesisOutput)
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	ctx := workflowledger.ContextWithClaimHolder(context.Background(), ctrl.Holder)
	if err := repo.ClaimRun(ctx, ctrl.RunID, ctrl.Holder); err != nil {
		t.Fatalf("ClaimRun() error = %v", err)
	}
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if run.Status == workflowledger.RunStatusPending {
		if err := repo.CompareAndSetRunStatus(ctx, ctrl.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
			t.Fatalf("CompareAndSetRunStatus() error = %v", err)
		}
		run, err = repo.GetRun(ctx, ctrl.RunID)
		if err != nil {
			t.Fatalf("GetRun() error = %v", err)
		}
	}
	attempt, err := ctrl.buildPanelAttempt(ctx, run, step, nil)
	if err != nil {
		t.Fatalf("buildPanelAttempt() error = %v", err)
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatalf("CreateStepAttempt() error = %v", err)
	}
	attempt, err = repo.GetStepAttempt(ctx, ctrl.RunID, attempt.AttemptID)
	if err != nil {
		t.Fatalf("GetStepAttempt() error = %v", err)
	}
	return ctrl, repo, run, attempt, ctx
}

func TestReconcilePanelCancellation_MembersAdmittedNeverDispatchedTombstonesAll(t *testing.T) {
	ctrl, repo, _, attempt, ctx := panelCancelReconcileFixture(t, `{}`, `{}`)
	runner := ctrl.Runner.(*CoordinatorRunner)
	panel := workflowledger.NewPanelCoordinator(ctrl.RunID, runner.Coordinator, repo)

	updated, allTerminal, err := ReconcilePanelCancellation(ctx, repo, panel, ctrl.RunID, ctrl.Holder, attempt.AttemptID)
	if err != nil {
		t.Fatalf("ReconcilePanelCancellation() error = %v", err)
	}
	if !allTerminal {
		t.Fatal("expected every never-dispatched member to be tombstoned terminal")
	}
	if updated.PanelExecution.Phase != workflowledger.PanelPhaseCancelPending {
		t.Fatalf("phase = %q, want cancel_pending", updated.PanelExecution.Phase)
	}

	// Idempotent re-entry after the phase already reached cancel_pending.
	updated2, allTerminal2, err := ReconcilePanelCancellation(ctx, repo, panel, ctrl.RunID, ctrl.Holder, attempt.AttemptID)
	if err != nil || !allTerminal2 {
		t.Fatalf("re-entry allTerminal=%v err=%v", allTerminal2, err)
	}
	if updated2.Version != updated.Version {
		t.Fatalf("idempotent re-entry must not advance the attempt version further: %d -> %d", updated.Version, updated2.Version)
	}
}

func TestReconcilePanelCancellation_AlreadyTerminalAttemptIsNoOp(t *testing.T) {
	ctrl, repo, _, attempt, ctx := panelCancelReconcileFixture(t, `{}`, `{}`)
	runner := ctrl.Runner.(*CoordinatorRunner)
	panel := workflowledger.NewPanelCoordinator(ctrl.RunID, runner.Coordinator, repo)

	outcome := workflowledger.AttemptOutcome{Status: workflowledger.AttemptStatusFailed}
	if err := repo.CompleteStepAttempt(ctx, ctrl.RunID, attempt.AttemptID, attempt.Version, outcome); err != nil {
		t.Fatalf("CompleteStepAttempt() error = %v", err)
	}

	counting := &countingPanelPhaseRepository{Repository: repo}
	updated, allTerminal, err := ReconcilePanelCancellation(ctx, counting, panel, ctrl.RunID, ctrl.Holder, attempt.AttemptID)
	if err != nil || !allTerminal {
		t.Fatalf("allTerminal=%v err=%v", allTerminal, err)
	}
	if updated.Status != workflowledger.AttemptStatusFailed {
		t.Fatalf("status = %q, want unchanged failed", updated.Status)
	}
	if counting.compareAndSetPanelPhaseCalls != 0 {
		t.Fatalf("an already-terminal attempt must not attempt a phase transition, got %d calls", counting.compareAndSetPanelPhaseCalls)
	}
}

func TestReconcilePanelCancellation_MissingClaimHolderFails(t *testing.T) {
	ctrl, repo, _, attempt, ctx := panelCancelReconcileFixture(t, `{}`, `{}`)
	runner := ctrl.Runner.(*CoordinatorRunner)
	panel := workflowledger.NewPanelCoordinator(ctrl.RunID, runner.Coordinator, repo)

	if _, _, err := ReconcilePanelCancellation(ctx, repo, panel, ctrl.RunID, "", attempt.AttemptID); !errors.Is(err, workflowledger.ErrClaimNotHeld) {
		t.Fatalf("error = %v, want ErrClaimNotHeld", err)
	}
}

// alwaysConflictPanelPhaseRepository fails every CompareAndSetPanelPhase
// call with ErrConflict, simulating sustained contention (repeated claim
// handoffs mid-reconciliation) that never lets this caller's CAS land.
type alwaysConflictPanelPhaseRepository struct {
	workflowledger.Repository
}

func (r *alwaysConflictPanelPhaseRepository) CompareAndSetPanelPhase(ctx context.Context, runID, attemptID string, expectedVersion uint64, from, to workflowledger.PanelPhase, synthesis *workflowledger.PanelSynthesisExecution) error {
	return workflowledger.ErrConflict
}

// Bug-audit finding: exhausting advancePanelPhaseToCancelPending's bounded
// retry loop must report the same retryable ErrCancelBlocked outcome every
// other "cannot make progress right now" cancel path reports, not a hard
// error that would permanently fail the run.
func TestReconcilePanelCancellation_RetryExhaustionReportsErrCancelBlocked(t *testing.T) {
	ctrl, repo, _, attempt, ctx := panelCancelReconcileFixture(t, `{}`, `{}`)
	runner := ctrl.Runner.(*CoordinatorRunner)
	stuck := &alwaysConflictPanelPhaseRepository{Repository: repo}
	panel := workflowledger.NewPanelCoordinator(ctrl.RunID, runner.Coordinator, stuck)

	_, allTerminal, err := ReconcilePanelCancellation(ctx, stuck, panel, ctrl.RunID, ctrl.Holder, attempt.AttemptID)
	if allTerminal {
		t.Fatal("retry exhaustion must not report allTerminal")
	}
	if !errors.Is(err, ErrCancelBlocked) {
		t.Fatalf("error = %v, want errors.Is(err, ErrCancelBlocked)", err)
	}
}

// claimHeldOnPanelPhaseWriteRepository fails the first CompareAndSetPanelPhase
// call with ErrClaimHeld, simulating a legitimate concurrent takeover
// (lease-reaper, operator retry, claim-heartbeat handoff) that flips the
// workflow claim's holder in the gap between advancePanelPhaseToCancelPending's
// own ClaimRun refresh and this CAS write's independent claim check.
type claimHeldOnPanelPhaseWriteRepository struct {
	workflowledger.Repository
	calls int
}

func (r *claimHeldOnPanelPhaseWriteRepository) CompareAndSetPanelPhase(ctx context.Context, runID, attemptID string, expectedVersion uint64, from, to workflowledger.PanelPhase, synthesis *workflowledger.PanelSynthesisExecution) error {
	r.calls++
	if r.calls == 1 {
		return workflowledger.ErrClaimHeld
	}
	return r.Repository.CompareAndSetPanelPhase(ctx, runID, attemptID, expectedVersion, from, to, synthesis)
}

// Regression: a lost CAS on the cancel-phase write itself must classify as
// ErrCancelBlocked, exactly like the ClaimRun refresh above it and the
// bounded-retry-exhaustion path, when the durable write reports
// workflowledger.ErrClaimHeld instead of workflowledger.ErrConflict. Before
// the fix, advancePanelPhaseToCancelPending only special-cased ErrConflict
// and returned the bare ErrClaimHeld unwrapped, which callers (via
// panel_step.go's reconcilePanelCancelPending) do not recognize as
// retryable and instead treat as a permanent run failure.
func TestReconcilePanelCancellation_ClaimHeldOnPhaseWriteReportsErrCancelBlocked(t *testing.T) {
	ctrl, repo, _, attempt, ctx := panelCancelReconcileFixture(t, `{}`, `{}`)
	runner := ctrl.Runner.(*CoordinatorRunner)
	takenOver := &claimHeldOnPanelPhaseWriteRepository{Repository: repo}
	panel := workflowledger.NewPanelCoordinator(ctrl.RunID, runner.Coordinator, takenOver)

	_, allTerminal, err := ReconcilePanelCancellation(ctx, takenOver, panel, ctrl.RunID, ctrl.Holder, attempt.AttemptID)
	if allTerminal {
		t.Fatal("a claim lost mid-write must not report allTerminal")
	}
	if !errors.Is(err, ErrCancelBlocked) {
		t.Fatalf("error = %v, want errors.Is(err, ErrCancelBlocked)", err)
	}
	if errors.Is(err, ErrCancelPending) {
		t.Fatalf("error = %v, must not also match ErrCancelPending", err)
	}
}

// errSentinelCancelCause is a stand-in for a concrete underlying cause (e.g.
// workflowledger.ErrClaimHeld, context.DeadlineExceeded,
// coordinator.ErrWaitOnlyJoinLost) that a PanelCancelCoordinator might return
// from CancelOrTombstoneMember/CancelOrTombstoneSynthesis.
var errSentinelCancelCause = errors.New("sentinel underlying cancel cause")

// failingCancelCoordinator always fails member and synthesis
// cancel/tombstone attempts with errSentinelCancelCause, so tests can assert
// that ReconcilePanelCancellation's ErrCancelBlocked wrapping still exposes
// the concrete cause to errors.Is.
type failingCancelCoordinator struct{}

func (failingCancelCoordinator) CancelOrTombstoneMember(ctx context.Context, attemptID, memberID string) (bool, error) {
	return false, errSentinelCancelCause
}

func (failingCancelCoordinator) CancelOrTombstoneSynthesis(ctx context.Context, attemptID string) (bool, error) {
	return false, errSentinelCancelCause
}

// Regression: ReconcilePanelCancellation must preserve the underlying cause
// of a per-child cancel/tombstone failure through errors.Is, not just the
// ErrCancelBlocked sentinel. Before the fix, the member/synthesis/workflow
// -claim wrapping sites used fmt.Errorf's %v verb for the underlying error,
// which stringifies it and makes it unreachable via errors.Is/errors.As by
// any caller further up the stack.
func TestReconcilePanelCancellation_MemberCancelErrorPreservesUnderlyingCause(t *testing.T) {
	ctrl, repo, _, attempt, ctx := panelCancelReconcileFixture(t, `{}`, `{}`)

	_, allTerminal, err := ReconcilePanelCancellation(ctx, repo, failingCancelCoordinator{}, ctrl.RunID, ctrl.Holder, attempt.AttemptID)
	if allTerminal {
		t.Fatal("a failing member cancel must not report allTerminal")
	}
	if !errors.Is(err, ErrCancelBlocked) {
		t.Fatalf("error = %v, want errors.Is(err, ErrCancelBlocked)", err)
	}
	if !errors.Is(err, errSentinelCancelCause) {
		t.Fatalf("error = %v, want errors.Is(err, errSentinelCancelCause): the underlying cause must remain reachable through the wrap", err)
	}
}

func TestReconcilePanelCancellation_RetriesAfterLostCAS(t *testing.T) {
	ctrl, repo, _, attempt, ctx := panelCancelReconcileFixture(t, `{}`, `{}`)
	runner := ctrl.Runner.(*CoordinatorRunner)
	conflicting := &conflictingPanelPhaseRepository{Repository: repo}
	panel := workflowledger.NewPanelCoordinator(ctrl.RunID, runner.Coordinator, conflicting)

	updated, allTerminal, err := ReconcilePanelCancellation(ctx, conflicting, panel, ctrl.RunID, ctrl.Holder, attempt.AttemptID)
	if err != nil {
		t.Fatalf("ReconcilePanelCancellation() error = %v", err)
	}
	if !allTerminal {
		t.Fatal("expected reconciliation to converge after retrying the lost CAS")
	}
	if updated.PanelExecution.Phase != workflowledger.PanelPhaseCancelPending {
		t.Fatalf("phase = %q, want cancel_pending", updated.PanelExecution.Phase)
	}
}
