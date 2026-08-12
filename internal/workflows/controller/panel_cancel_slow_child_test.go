package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	coordledger "github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// slowListTasksLedgerRepository delays ListTasks by delay before delegating.
// The coordinator's own cancel finalization (reconcileCancellation) calls
// ListTasks as its very first step and only closes the run handle's
// cancelDone channel once that whole (unbounded, background-context) flow
// finishes - so a delay here reliably makes a real *caller* ctx with a
// shorter deadline lose the internal race in coordinator.Cancel's `select
// { case <-h.cancelDone: ...; case <-ctx.Done(): ... }`, without needing to
// coordinate a live blocked handler goroutine against a wall-clock timeout.
type slowListTasksLedgerRepository struct {
	coordledger.LedgerRepository
	delay time.Duration
}

func (r *slowListTasksLedgerRepository) ListTasks(ctx context.Context, runID string) ([]coordledger.TaskSnapshot, error) {
	select {
	case <-ctx.Done():
	case <-time.After(r.delay):
	}
	return r.LedgerRepository.ListTasks(ctx, runID)
}

// singleCoordinatorSlowPanelFixture builds one controller with ONE
// coordinator (no racing second executor, unlike twoCoordinatorPanelFixture)
// whose underlying coordinator ledger answers ListTasks slowly. The
// "security" member is genuinely dispatched and admitted through that same
// coordinator (so its cancel path is the live, non-recovered handle, not an
// ambiguous recovered one), but the ledger latency means a caller with a
// short ctx deadline loses the coordinator.Cancel race before the ledger's
// own (unbounded, background-context) cancellation bookkeeping finishes.
func singleCoordinatorSlowPanelFixture(t *testing.T, listTasksDelay time.Duration) (*LinearController, workflowledger.Repository, workflowledger.StepAttempt, context.Context) {
	t.Helper()
	ctrl, repo, step, coord := newSingleCoordinatorSlowPanelController(t, listTasksDelay)
	return admitSlowPanelSecurityMemberAndReachCancelPending(t, ctrl, repo, step, coord)
}

func newSingleCoordinatorSlowPanelController(t *testing.T, listTasksDelay time.Duration) (*LinearController, workflowledger.Repository, definition.Step, coordinator.Coordinator) {
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
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "panel-reviewer", fixedOutputHandler{raw: `{}`}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Register(runtime.Subagent, "review-synthesizer", fixedOutputHandler{raw: `{}`}); err != nil {
		t.Fatal(err)
	}
	slowLedger := &slowListTasksLedgerRepository{LedgerRepository: coordledger.NewMemoryLedgerRepository(), delay: listTasksDelay}
	coord := coordinator.New(slowLedger, subagents.New(dispatcher, subagents.Policy{Workers: 4}))

	repo := workflowledger.NewMemoryRepository()
	wf := &compiler.CompiledWorkflow{Name: "panel", InitialStep: step.ID, Steps: []definition.Step{step}, Transitions: []definition.Transition{
		{From: step.ID, To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
	}}
	ctrl, err := NewLinearController(repo, NewCoordinatorRunner(coord), wf, nil, map[string]any{"task": "change"}, "wfr-single-coord-slow", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	return ctrl, repo, step, coord
}

// admitSlowPanelSecurityMemberAndReachCancelPending claims the run, builds
// and admits the panel attempt, becomes the security member's local actor
// (so its cancel path is live, not recovered), then durably advances the
// attempt's panel phase to cancel_pending - the state
// reconcilePanelCancelPending expects to resume from.
func admitSlowPanelSecurityMemberAndReachCancelPending(t *testing.T, ctrl *LinearController, repo workflowledger.Repository, step definition.Step, coord coordinator.Coordinator) (*LinearController, workflowledger.Repository, workflowledger.StepAttempt, context.Context) {
	t.Helper()
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

	panel := workflowledger.NewPanelCoordinator(ctrl.RunID, coord, repo)
	handle, err := panel.EnsureMember(ctx, attempt.AttemptID, "security")
	if err != nil {
		t.Fatalf("EnsureMember() error = %v", err)
	}
	if !handle.LocalActor() {
		t.Fatal("expected this coordinator to become the local actor for security")
	}

	if err := repo.CompareAndSetPanelPhase(ctx, ctrl.RunID, attempt.AttemptID, attempt.Version, workflowledger.PanelPhaseMembersAdmitted, workflowledger.PanelPhaseCancelPending, nil); err != nil {
		t.Fatal(err)
	}
	attempt, err = repo.GetStepAttempt(ctx, ctrl.RunID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	return ctrl, repo, attempt, ctx
}

// TestAdvancePanelStep_SlowNotAmbiguousChildLeavesRunNonTerminal covers
// reconcilePanelCancelPending's `if !allTerminal { return run, false,
// ErrCancelReconciliationPending }` branch (panel_step.go), for the
// genuinely-slow-child case - distinct from TestAdvancePanelStep_
// CancelPendingAmbiguousChildLeavesRunNonTerminal's ErrCancelBlocked branch.
// Here there is exactly one coordinator (no racing second executor, so the
// "security" child's claim is never ambiguous, and it is dispatched and
// canceled through the exact same live, non-recovered handle), but the
// underlying coordinator ledger answers slowly. coordinator.Cancel races the
// caller's ctx deadline against its own unbounded cancellation bookkeeping
// (see reconcileCancellation, which uses a fixed internal background
// context, not the caller's ctx) and loses, returning
// context.DeadlineExceeded. isPanelCancelContention (panel_coordinator.go)
// recognizes that as benign contention rather than a failure, so
// CancelOrTombstoneMember reports (terminal=false, err=nil) and
// ReconcilePanelCancellation returns (attempt, false, nil) - landing exactly
// on the uncovered line.
func TestAdvancePanelStep_SlowNotAmbiguousChildLeavesRunNonTerminal(t *testing.T) {
	ctrl, repo, attempt, _ := singleCoordinatorSlowPanelFixture(t, 300*time.Millisecond)

	shortCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	run, done, err := ctrl.Advance(shortCtx)
	if !errors.Is(err, ErrCancelReconciliationPending) {
		t.Fatalf("Advance() error = %v, want ErrCancelReconciliationPending", err)
	}
	if errors.Is(err, ErrCancelBlocked) {
		t.Fatalf("Advance() error = %v, must not also match ErrCancelBlocked: a slow-but-unambiguous child is a distinct case from an ambiguous claim", err)
	}
	if done {
		t.Fatal("a slow, non-ambiguous child must not let Advance settle the run in this call")
	}
	if run.Status == workflowledger.RunStatusCanceled {
		t.Fatal("a slow child must not report the run canceled before it actually terminates")
	}
	stored, err := repo.GetStepAttempt(context.Background(), ctrl.RunID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PanelExecution.Phase != workflowledger.PanelPhaseCancelPending {
		t.Fatalf("phase = %q, want cancel_pending (durably recorded for a later reconciliation retry)", stored.PanelExecution.Phase)
	}
	if workflowledger.IsTerminalAttemptStatus(stored.Status) {
		t.Fatalf("attempt status = %q, want non-terminal while the child is still being canceled", stored.Status)
	}
}
