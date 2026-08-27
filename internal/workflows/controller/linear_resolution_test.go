package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// twoGateWorkflow compiles gate_a (human) -> mid (agent) -> gate_b (human)
// -> success, for the stale-approval replay regression.
func twoGateWorkflow(t *testing.T) *definition.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "two-gate", InitialStep: "gate_a",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 8},
		Steps: []definition.Step{
			{ID: "gate_a", Kind: "human_gate", OnFailure: "failure"},
			{ID: "mid", Kind: "agent", Agent: "worker", OnFailure: "failure"},
			{ID: "gate_b", Kind: "human_gate", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			{From: "gate_a", To: "mid", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"decision": "approved"}}},
			{From: "mid", To: "gate_b", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "gate_b", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"decision": "approved"}}},
		},
	}
	compiled, err := definition.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// TestResolutionControllerApprove: a human_gate run parked at
// waiting_approval is approved through the resolution controller and settles
// to succeeded; a replay is an idempotent no-op.
func TestResolutionControllerApprove(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	wf := humanDeliveryWorkflow(t, false)
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, "wfr-approve", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("run status = %q, want waiting_approval", got.Status)
	}

	res, err := NewResolutionController(repo, wf, ctrl.RunID, []byte("snapshot"), map[string]any{"task": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Approve(ctx, "wfa-approval-approve_me-1", "operator"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded", run.Status)
	}
	// Replay after the run finished is an idempotent no-op.
	if err := res.Approve(ctx, "wfa-approval-approve_me-1", "operator"); err != nil {
		t.Fatalf("replayed Approve: %v", err)
	}
}

// TestResolutionControllerReject: rejecting the gate fails the run and the
// approval is recorded as rejected.
func TestResolutionControllerReject(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	wf := humanDeliveryWorkflow(t, false)
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, "wfr-reject", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.Run(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := NewResolutionController(repo, wf, ctrl.RunID, []byte("snapshot"), map[string]any{"task": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Reject(ctx, "wfa-approval-approve_me-1", "operator", "not now"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
	approvals, err := repo.ListApprovals(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(approvals) != 1 || approvals[0].Status != "rejected" || approvals[0].Reason != "not now" {
		t.Fatalf("approvals = %+v, want one rejected approval with reason", approvals)
	}
}

// humanGateRejectionWorkflow compiles a human_gate "approve_me" whose
// on_failure names the NON-TERMINAL step "fix". The approve_me -> fix
// "failed" transition is never matched at runtime (human rejections route
// through failureRoute, never the matcher); it exists only because the
// compiler's reachability check requires every step to be reachable through
// transitions, mirroring onFailureRepairWorkflow.
func humanGateRejectionWorkflow(t *testing.T) *definition.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "human-reject-nonterminal", InitialStep: "approve_me",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 4},
		Steps: []definition.Step{
			{ID: "approve_me", Kind: "human_gate", OnFailure: "fix"},
			{ID: "fix", Kind: "agent", Agent: "dev", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			{From: "approve_me", To: "fix", Match: definition.MatchCriteria{Status: "failed"}},
			{From: "approve_me", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"decision": "approved"}}},
			{From: "fix", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := definition.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// TestHumanGateRejectionPersistsTerminalRoute pins
// wf-human-gate-rejection-nonterminal-route: when a human_gate's on_failure
// names a NON-TERMINAL step ("fix"), rejecting the gate must persist a
// TERMINAL route ("failure") on the attempt — never the un-honored
// failureTarget(step) that a crash between the attempt persist and the
// run-fail CAS could resume into and silently undo the rejection. Rejection
// always fails the run, so the durable route must be terminal and the fix
// step must never be dispatched. Before the fix, finishHumanResolutionForAttempt
// persisted ToStepID "fix".
func TestHumanGateRejectionPersistsTerminalRoute(t *testing.T) {
	ctx := context.Background()
	wf := humanGateRejectionWorkflow(t)
	runner := &scriptedRunner{}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"fix": {Agent: agents.ResolvedAgent{Name: "dev"}},
	}, map[string]any{"task": "x"}, "wfr-reject-nonterminal", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("run status = %q, want waiting_approval", got.Status)
	}

	res, err := NewResolutionController(repo, wf, ctrl.RunID, []byte("snap"), map[string]any{"task": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Reject(ctx, "wfa-approval-approve_me-1", "operator", "not now"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
	// The rejected attempt must record a TERMINAL route ("failure"), never the
	// un-honored non-terminal on_failure target "fix".
	attempts, err := repo.ListStepAttempts(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var rejected workflowledger.StepAttempt
	for _, a := range attempts {
		if a.StepID == "approve_me" {
			rejected = a
		}
	}
	if rejected.AttemptID == "" {
		t.Fatal("approve_me attempt missing")
	}
	if rejected.Status != workflowledger.AttemptStatusFailed {
		t.Fatalf("approve_me attempt status = %q, want failed", rejected.Status)
	}
	if rejected.ToStepID != "failure" {
		t.Fatalf("rejected attempt ToStepID = %q, want \"failure\" (not the non-terminal on_failure target \"fix\")", rejected.ToStepID)
	}
	// NEGATIVE PATH: rejection must fail the run and never route — the fix
	// step is never dispatched.
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, call := range runner.calls {
		if call.StepID == "fix" {
			t.Fatalf("fix step dispatched after rejection; rejection must never route to on_failure")
		}
	}
}

// TestStaleApprovalReplayDoesNotTouchCurrentGate (audit regression): after
// the run advances from gate_a to gate_b, replaying gate_a's already-approved
// approval must fail and must NOT flip the run status; the current gate's
// approval stays actionable.
func TestStaleApprovalReplayDoesNotTouchCurrentGate(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	wf := twoGateWorkflow(t)
	runner := &linearRunner{outputs: map[string]json.RawMessage{"mid": json.RawMessage(`{"ok":true}`)}}
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"mid": {Agent: agents.ResolvedAgent{Name: "worker"}},
	}, map[string]any{"task": "x"}, "wfr-two-gate", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != workflowledger.RunStatusWaitingApproval || got.ActiveStepID != "gate_a" {
		t.Fatalf("run = %q/%q, want waiting_approval at gate_a", got.Status, got.ActiveStepID)
	}

	res, err := NewResolutionController(repo, wf, ctrl.RunID, []byte("snapshot"), map[string]any{"task": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Approve(ctx, "wfa-approval-gate_a-1", "operator"); err != nil {
		t.Fatalf("Approve gate_a: %v", err)
	}

	// Continue: the mid step runs and the run parks at gate_b.
	got, err = ctrl.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != workflowledger.RunStatusWaitingApproval || got.ActiveStepID != "gate_b" {
		t.Fatalf("run = %q/%q, want waiting_approval at gate_b", got.Status, got.ActiveStepID)
	}

	// Replay the stale gate_a approval: refused, run untouched.
	err = res.Approve(ctx, "wfa-approval-gate_a-1", "operator")
	if err == nil || !strings.Contains(err.Error(), "targets step") {
		t.Fatalf("stale replay error = %v, want a step-mismatch refusal", err)
	}
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusWaitingApproval || run.ActiveStepID != "gate_b" {
		t.Fatalf("run after stale replay = %q/%q, want untouched waiting_approval at gate_b", run.Status, run.ActiveStepID)
	}

	// The current gate's approval is still actionable.
	if err := res.Approve(ctx, "wfa-approval-gate_b-1", "operator"); err != nil {
		t.Fatalf("Approve gate_b: %v", err)
	}
	run, err = repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded", run.Status)
	}
}
