package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	coordledger "github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// Cancellation matrix item 10: resume from cancel_pending repairs a crash
// after each tombstone write. Advance itself (not a separate cancel call)
// finishes reconciling an attempt a prior crashed cancel left in
// cancel_pending, and settles the run canceled once every child is terminal.
func TestAdvancePanelStep_ResumesCancelPendingUntilAllTerminal(t *testing.T) {
	ctrl, repo, _, attempt, ctx := panelCancelReconcileFixture(t, `{}`, `{}`)
	if err := repo.CompareAndSetPanelPhase(ctx, ctrl.RunID, attempt.AttemptID, attempt.Version, workflowledger.PanelPhaseMembersAdmitted, workflowledger.PanelPhaseCancelPending, nil); err != nil {
		t.Fatalf("CompareAndSetPanelPhase() error = %v", err)
	}

	run, done, err := ctrl.Advance(context.Background())
	if err != nil {
		t.Fatalf("Advance() error = %v", err)
	}
	if !done {
		t.Fatal("expected Advance to settle the canceled run in one call")
	}
	if run.Status != workflowledger.RunStatusCanceled {
		t.Fatalf("run status = %q, want canceled", run.Status)
	}
	stored, err := repo.GetStepAttempt(context.Background(), ctrl.RunID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != workflowledger.AttemptStatusCanceled {
		t.Fatalf("attempt status = %q, want canceled", stored.Status)
	}
}

// conflictingCompleteStepAttemptRepository fails the first CompleteStepAttempt
// call with ErrConflict, simulating a second executor's concurrent Advance
// winning the CAS on the same cancel_pending attempt first (D14's
// claim-heartbeat handoff window), then behaves normally.
type conflictingCompleteStepAttemptRepository struct {
	workflowledger.Repository
	failed bool
}

func (r *conflictingCompleteStepAttemptRepository) CompleteStepAttempt(ctx context.Context, runID, attemptID string, expectedVersion uint64, outcome workflowledger.AttemptOutcome) error {
	if !r.failed {
		r.failed = true
		return workflowledger.ErrConflict
	}
	return r.Repository.CompleteStepAttempt(ctx, runID, attemptID, expectedVersion, outcome)
}

// Bug-audit finding: reconcilePanelCancelPending must treat a version-CAS
// conflict on CompleteStepAttempt the same retryable way ErrCancelBlocked is
// treated, not escalate it to a permanent run failure via c.fail. A losing
// CompleteStepAttempt here is exactly what happens when two executors are
// both legitimately live for the same run near a claim-lease handoff and
// both race reconcilePanelCancelPending for the same attempt.
func TestAdvancePanelStep_CancelPendingCompleteStepAttemptConflictStaysNonTerminal(t *testing.T) {
	ctrl, repo, _, attempt, ctx := panelCancelReconcileFixture(t, `{}`, `{}`)
	if err := repo.CompareAndSetPanelPhase(ctx, ctrl.RunID, attempt.AttemptID, attempt.Version, workflowledger.PanelPhaseMembersAdmitted, workflowledger.PanelPhaseCancelPending, nil); err != nil {
		t.Fatalf("CompareAndSetPanelPhase() error = %v", err)
	}
	conflicting := &conflictingCompleteStepAttemptRepository{Repository: repo}
	ctrl.Repo = conflicting

	run, done, err := ctrl.Advance(context.Background())
	if !errors.Is(err, ErrCancelReconciliationPending) {
		t.Fatalf("Advance() error = %v, want ErrCancelReconciliationPending (a CAS conflict must be retryable, not a durable error)", err)
	}
	if done {
		t.Fatal("Advance() done = true, want false: a losing CompleteStepAttempt CAS must leave the run non-terminal for a later retry")
	}
	if run.Status == workflowledger.RunStatusFailed {
		t.Fatal("a CompleteStepAttempt version conflict must never fail the run; the attempt legitimately reconciled to canceled")
	}
	stored, err := repo.GetStepAttempt(context.Background(), ctrl.RunID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if workflowledger.IsTerminalAttemptStatus(stored.Status) {
		t.Fatalf("attempt status = %q, want non-terminal after the lost CAS so a retry can settle it", stored.Status)
	}

	// A later retry (the other executor's write already landed; this repo
	// now behaves normally) converges to canceled without failing.
	run, done, err = ctrl.Advance(context.Background())
	if err != nil {
		t.Fatalf("retry Advance() error = %v", err)
	}
	if !done || run.Status != workflowledger.RunStatusCanceled {
		t.Fatalf("retry Advance() done=%v status=%q, want done=true status=canceled", done, run.Status)
	}
}

// claimHeldCompleteStepAttemptRepository fails the first CompleteStepAttempt
// call with ErrClaimHeld, simulating a concurrent claim takeover landing
// exactly at this commit: the version CAS matched (so this is not
// ErrConflict), but the claim-fenced append lost to another holder's claim
// (D14's claim-heartbeat handoff window), then behaves normally.
type claimHeldCompleteStepAttemptRepository struct {
	workflowledger.Repository
	failed bool
}

func (r *claimHeldCompleteStepAttemptRepository) CompleteStepAttempt(ctx context.Context, runID, attemptID string, expectedVersion uint64, outcome workflowledger.AttemptOutcome) error {
	if !r.failed {
		r.failed = true
		return workflowledger.ErrClaimHeld
	}
	return r.Repository.CompleteStepAttempt(ctx, runID, attemptID, expectedVersion, outcome)
}

// Bug-audit finding: reconcilePanelCancelPending must treat ErrClaimHeld from
// CompleteStepAttempt the same retryable way ErrConflict and ErrCancelBlocked
// are treated, not escalate it to a permanent run failure via c.fail.
// ErrClaimHeld is a distinct sentinel from ErrConflict (the version CAS can
// match while the claim-fenced append still loses to a concurrent holder),
// and the adjacent comment's own stated rationale names exactly this
// claim-heartbeat handoff window as the scenario that must stay non-terminal.
func TestAdvancePanelStep_CancelPendingCompleteStepAttemptClaimHeldStaysNonTerminal(t *testing.T) {
	ctrl, repo, _, attempt, ctx := panelCancelReconcileFixture(t, `{}`, `{}`)
	if err := repo.CompareAndSetPanelPhase(ctx, ctrl.RunID, attempt.AttemptID, attempt.Version, workflowledger.PanelPhaseMembersAdmitted, workflowledger.PanelPhaseCancelPending, nil); err != nil {
		t.Fatalf("CompareAndSetPanelPhase() error = %v", err)
	}
	claimHeld := &claimHeldCompleteStepAttemptRepository{Repository: repo}
	ctrl.Repo = claimHeld

	run, done, err := ctrl.Advance(context.Background())
	if !errors.Is(err, ErrCancelReconciliationPending) {
		t.Fatalf("Advance() error = %v, want ErrCancelReconciliationPending (ErrClaimHeld must be retryable, not a durable error)", err)
	}
	if done {
		t.Fatal("Advance() done = true, want false: a losing CompleteStepAttempt claim-fenced write must leave the run non-terminal for a later retry")
	}
	if run.Status == workflowledger.RunStatusFailed {
		t.Fatal("a CompleteStepAttempt ErrClaimHeld must never fail the run; the attempt legitimately reconciled to canceled")
	}
	stored, err := repo.GetStepAttempt(context.Background(), ctrl.RunID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if workflowledger.IsTerminalAttemptStatus(stored.Status) {
		t.Fatalf("attempt status = %q, want non-terminal after the lost claim-fenced write so a retry can settle it", stored.Status)
	}

	// A later retry (the other executor's write already landed; this repo
	// now behaves normally) converges to canceled without failing.
	run, done, err = ctrl.Advance(context.Background())
	if err != nil {
		t.Fatalf("retry Advance() error = %v", err)
	}
	if !done || run.Status != workflowledger.RunStatusCanceled {
		t.Fatalf("retry Advance() done=%v status=%q, want done=true status=canceled", done, run.Status)
	}
}

// conflictingRunStatusRepository simulates a second executor's concurrent
// CompareAndSetRunStatus call winning the race first: on the first call it
// actually performs the real CAS to the run's current version (as the
// concurrent winner would), then reports ErrConflict to this caller, exactly
// like a real optimistic-concurrency loss — this caller's `run` view is
// simply stale, not wrong.
type conflictingRunStatusRepository struct {
	workflowledger.Repository
	failed bool
}

func (r *conflictingRunStatusRepository) CompareAndSetRunStatus(ctx context.Context, runID string, expectedVersion uint64, status workflowledger.RunStatus, finishedAt *time.Time) error {
	if !r.failed {
		r.failed = true
		current, err := r.Repository.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if err := r.Repository.CompareAndSetRunStatus(ctx, runID, current.Version, status, finishedAt); err != nil {
			return err
		}
		return workflowledger.ErrConflict
	}
	return r.Repository.CompareAndSetRunStatus(ctx, runID, expectedVersion, status, finishedAt)
}

// Bug-audit finding: settlePanelRunCanceled must treat a version-CAS conflict
// on CompareAndSetRunStatus as retryable too, not escalate to c.fail. Without
// this, a stale run version held by an executor that lost an earlier race can
// durably flip an already-reconciled-canceled run to Failed even though the
// run was in fact just settled canceled by the winner.
func TestAdvancePanelStep_CancelPendingRunStatusConflictStaysNonTerminal(t *testing.T) {
	ctrl, repo, _, attempt, ctx := panelCancelReconcileFixture(t, `{}`, `{}`)
	if err := repo.CompareAndSetPanelPhase(ctx, ctrl.RunID, attempt.AttemptID, attempt.Version, workflowledger.PanelPhaseMembersAdmitted, workflowledger.PanelPhaseCancelPending, nil); err != nil {
		t.Fatalf("CompareAndSetPanelPhase() error = %v", err)
	}
	conflicting := &conflictingRunStatusRepository{Repository: repo}
	ctrl.Repo = conflicting

	run, done, err := ctrl.Advance(context.Background())
	if !errors.Is(err, ErrCancelReconciliationPending) {
		t.Fatalf("Advance() error = %v, want ErrCancelReconciliationPending (a CAS conflict must be retryable, not a durable error)", err)
	}
	if done {
		t.Fatal("Advance() done = true, want false: a losing CompareAndSetRunStatus CAS must not report the run settled from this call")
	}
	if run.Status == workflowledger.RunStatusFailed {
		t.Fatal("a CompareAndSetRunStatus version conflict must never fail the run; every panel child legitimately reconciled to canceled")
	}
	stored, err := repo.GetRun(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != workflowledger.RunStatusCanceled {
		t.Fatalf("run status = %q, want canceled: the concurrent winner's write must be the one visible durably", stored.Status)
	}

	// A later retry observes the run already terminal canceled and reports
	// it done, without ever reaching c.fail.
	run, done, err = ctrl.Advance(context.Background())
	if err != nil {
		t.Fatalf("retry Advance() error = %v", err)
	}
	if !done || run.Status != workflowledger.RunStatusCanceled {
		t.Fatalf("retry Advance() done=%v status=%q, want done=true status=canceled", done, run.Status)
	}
}

// claimHeldRunStatusRepository simulates a concurrent claim takeover landing
// exactly at settlePanelRunCanceled's run-status write: the version CAS
// matches (so this is not ErrConflict), but the trailing claim-fenced event
// append loses to another holder's claim (D14's claim-heartbeat handoff
// window), then behaves normally on retry.
type claimHeldRunStatusRepository struct {
	workflowledger.Repository
	failed bool
}

func (r *claimHeldRunStatusRepository) CompareAndSetRunStatus(ctx context.Context, runID string, expectedVersion uint64, status workflowledger.RunStatus, finishedAt *time.Time) error {
	if !r.failed {
		r.failed = true
		return workflowledger.ErrClaimHeld
	}
	return r.Repository.CompareAndSetRunStatus(ctx, runID, expectedVersion, status, finishedAt)
}

// Bug-audit finding: settlePanelRunCanceled must treat ErrClaimHeld from
// CompareAndSetRunStatus the same retryable way ErrConflict is treated, not
// escalate it to a permanent run failure via c.fail. ErrClaimHeld is a
// distinct sentinel from ErrConflict (the version CAS can match while the
// trailing claim-fenced event append still loses to a concurrent claim
// holder), and is exactly the same D14 claim-heartbeat handoff window already
// handled at the two sibling CompleteStepAttempt call sites.
func TestAdvancePanelStep_CancelPendingRunStatusClaimHeldStaysNonTerminal(t *testing.T) {
	ctrl, repo, _, attempt, ctx := panelCancelReconcileFixture(t, `{}`, `{}`)
	if err := repo.CompareAndSetPanelPhase(ctx, ctrl.RunID, attempt.AttemptID, attempt.Version, workflowledger.PanelPhaseMembersAdmitted, workflowledger.PanelPhaseCancelPending, nil); err != nil {
		t.Fatalf("CompareAndSetPanelPhase() error = %v", err)
	}
	claimHeld := &claimHeldRunStatusRepository{Repository: repo}
	ctrl.Repo = claimHeld

	run, done, err := ctrl.Advance(context.Background())
	if !errors.Is(err, ErrCancelReconciliationPending) {
		t.Fatalf("Advance() error = %v, want ErrCancelReconciliationPending (ErrClaimHeld must be retryable, not a durable error)", err)
	}
	if done {
		t.Fatal("Advance() done = true, want false: a losing CompareAndSetRunStatus claim-fenced write must leave the run non-terminal for a later retry")
	}
	if run.Status == workflowledger.RunStatusFailed {
		t.Fatal("a CompareAndSetRunStatus ErrClaimHeld must never fail the run; every panel child legitimately reconciled to canceled")
	}
	stored, err := repo.GetStepAttempt(context.Background(), ctrl.RunID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if !workflowledger.IsTerminalAttemptStatus(stored.Status) {
		t.Fatalf("attempt status = %q, want terminal canceled: the attempt CompleteStepAttempt already succeeded before the run-status write failed", stored.Status)
	}

	// A later retry with the claim (the wrapper now behaves normally, as the
	// real claim holder eventually would) settles the run canceled without
	// ever having gone through c.fail.
	settled, done, err := ctrl.settlePanelRunCanceled(ctx, run)
	if err != nil {
		t.Fatalf("retry settlePanelRunCanceled() error = %v", err)
	}
	if !done || settled.Status != workflowledger.RunStatusCanceled {
		t.Fatalf("retry settlePanelRunCanceled() done=%v status=%q, want done=true status=canceled", done, settled.Status)
	}
}

// twoCoordinatorPanelFixture builds two independent coordinator instances
// sharing the same durable coordinator ledger, so one can hold a live child
// claim while the other (the workflow controller under test) cannot verify
// it, exercising D15's fail-closed ambiguous-claim path.
func twoCoordinatorPanelFixture(t *testing.T, blockingRelease <-chan struct{}) (*LinearController, workflowledger.Repository, workflowledger.StepAttempt, *coordinator.RunHandle, context.Context) {
	t.Helper()
	step := definition.Step{
		ID: "review", Kind: "agent_panel", Agent: "review-synthesizer", Skill: "review-synthesis",
		Template: "synth", OutputSchema: "synthschema",
		Context: []definition.ContextBinding{{From: "inputs.task", As: "task", MaxBytes: 1024}},
		Panel: &definition.AgentPanel{FailurePolicy: "require_all", Members: []definition.PanelMember{
			{ID: "security", Agent: "panel-reviewer", Skill: "secure-change", Template: "security", OutputSchema: "report"},
			{ID: "correctness", Agent: "panel-reviewer", Skill: "bug-audit", Template: "correctness", OutputSchema: "report"},
		}},
	}
	snapshot, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		PanelBindings: map[string]workflowledger.PanelBindingSnapshot{
			"review/security":    {AgentName: "panel-reviewer", AgentDigest: strings.Repeat("a", 64), ProviderName: "deepseek", Model: "deepseek-v4-flash"},
			"review/correctness": {AgentName: "panel-reviewer", AgentDigest: strings.Repeat("b", 64), ProviderName: "zai", Model: "glm-5.2"},
			"review/synthesis":   {AgentName: "review-synthesizer", AgentDigest: strings.Repeat("c", 64), ProviderName: "deepseek", Model: "deepseek-v4-flash"},
		},
		Templates: map[string]workflowledger.RefSnapshot{
			"security": {Bytes: []byte("Review {{inputs.task}}.")}, "correctness": {Bytes: []byte("Review {{inputs.task}}.")},
			"synth": {Bytes: []byte("Synthesize.")},
		},
		Schemas: map[string]workflowledger.RefSnapshot{
			"report":      {Bytes: []byte(`{"type":"object"}`)},
			"synthschema": {Bytes: []byte(`{"type":"object"}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	blockingHandler := fixedBlockingHandler{release: blockingRelease}
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "panel-reviewer", blockingHandler); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Register(runtime.Subagent, "review-synthesizer", fixedOutputHandler{raw: `{}`}); err != nil {
		t.Fatal(err)
	}
	coordLedger := coordledger.NewMemoryLedgerRepository()
	otherCoord := coordinator.New(coordLedger, subagents.New(dispatcher, subagents.Policy{Workers: 4}))
	ctrlCoord := coordinator.New(coordLedger, subagents.New(dispatcher, subagents.Policy{Workers: 4}))

	repo := workflowledger.NewMemoryRepository()
	wf := &definition.CompiledWorkflow{Name: "panel", InitialStep: step.ID, Steps: []definition.Step{step}, Transitions: []definition.Transition{
		{From: step.ID, To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
	}}
	ctrl, err := NewLinearController(repo, NewCoordinatorRunner(ctrlCoord), wf, nil, map[string]any{"task": "change"}, "wfr-two-coord", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	ctx := workflowledger.ContextWithClaimHolder(context.Background(), ctrl.Holder)
	if err := repo.ClaimRun(ctx, ctrl.RunID, ctrl.Holder); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, ctrl.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	run, err = repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := ctrl.buildPanelAttempt(ctx, run, step, nil)
	if err != nil {
		t.Fatalf("buildPanelAttempt() error = %v", err)
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	attempt, err = repo.GetStepAttempt(ctx, ctrl.RunID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}

	// otherCoord dispatches "security" and holds its child claim, simulating
	// a still-alive different executor. ctrlCoord (the workflow controller's
	// own coordinator) shares the same durable ledger but has no in-process
	// handle for that child.
	otherPanel := workflowledger.NewPanelCoordinator(ctrl.RunID, otherCoord, repo)
	handle, err := otherPanel.EnsureMember(ctx, attempt.AttemptID, "security")
	if err != nil {
		t.Fatalf("EnsureMember() error = %v", err)
	}
	if !handle.LocalActor() {
		t.Fatal("expected the other coordinator to become the local actor for security")
	}

	if err := repo.CompareAndSetPanelPhase(ctx, ctrl.RunID, attempt.AttemptID, attempt.Version, workflowledger.PanelPhaseMembersAdmitted, workflowledger.PanelPhaseCancelPending, nil); err != nil {
		t.Fatal(err)
	}
	return ctrl, repo, attempt, handle, ctx
}

type fixedBlockingHandler struct{ release <-chan struct{} }

func (h fixedBlockingHandler) Invoke(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
	select {
	case <-h.release:
		return json.RawMessage(`{}`), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Cancellation matrix item 6: an ambiguous recovered claim produces
// cancel_blocked, not canceled, and does not clear the coordinator claim.
func TestAdvancePanelStep_CancelPendingAmbiguousChildLeavesRunNonTerminal(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ctrl, repo, attempt, _, _ := twoCoordinatorPanelFixture(t, release)

	run, done, err := ctrl.Advance(context.Background())
	if !errors.Is(err, ErrCancelReconciliationPending) {
		t.Fatalf("Advance() error = %v, want ErrCancelReconciliationPending", err)
	}
	if done {
		t.Fatal("an ambiguous child claim must never let Advance settle the run")
	}
	if run.Status == workflowledger.RunStatusCanceled {
		t.Fatal("an ambiguous child claim must not report the run canceled")
	}
	stored, err := repo.GetStepAttempt(context.Background(), ctrl.RunID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PanelExecution.Phase != workflowledger.PanelPhaseCancelPending {
		t.Fatalf("phase = %q, want cancel_pending (durably recorded, reconciliation retried later)", stored.PanelExecution.Phase)
	}
	if workflowledger.IsTerminalAttemptStatus(stored.Status) {
		t.Fatalf("attempt status = %q, must stay non-terminal while blocked", stored.Status)
	}
	// The workflow claim itself is untouched: a second Advance can still
	// claim and retry (Advance's own ClaimRun/ReleaseRun around the call
	// already exercises this; a held workflow claim here would surface as a
	// distinct error from this second call, not a silent hang). The child
	// claim is still ambiguous (release has not been closed yet), so the
	// retry stays blocked too - it must report ErrCancelReconciliationPending
	// again, not some other error, proving the claim itself was acquirable.
	if _, _, err := ctrl.Advance(context.Background()); !errors.Is(err, ErrCancelReconciliationPending) {
		t.Fatalf("retry Advance() error = %v, want ErrCancelReconciliationPending (the workflow claim must still be acquirable)", err)
	}
}
