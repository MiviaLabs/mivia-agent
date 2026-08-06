package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// twoGateWorkflow compiles gate_a (human) -> mid (agent) -> gate_b (human)
// -> success, for the stale-approval replay regression.
func twoGateWorkflow(t *testing.T) *compiler.CompiledWorkflow {
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
	compiled, err := compiler.Compile(wf)
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
