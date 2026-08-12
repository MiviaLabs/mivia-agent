package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	coordledger "github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
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
	wf := &compiler.CompiledWorkflow{Name: "panel", InitialStep: step.ID, Steps: []definition.Step{step}, Transitions: []definition.Transition{
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
	if err != nil {
		t.Fatalf("Advance() error = %v", err)
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
	// already exercises this; a held workflow claim here would surface as an
	// error from this second call, not a silent hang).
	if _, _, err := ctrl.Advance(context.Background()); err != nil {
		t.Fatalf("retry Advance() error = %v, want the workflow claim still acquirable", err)
	}
}
