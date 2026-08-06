package controller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// deliveryWorkflow compiles a one-agent-step workflow ending in the reserved
// success route, optionally declaring a pull_request delivery policy.
func deliveryWorkflow(t *testing.T, withDelivery bool) *compiler.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "delivery", InitialStep: "one",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Steps:  []definition.Step{{ID: "one", Kind: "agent", Agent: "worker"}},
		Transitions: []definition.Transition{
			{From: "one", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	if withDelivery {
		wf.Delivery = &definition.Delivery{Kind: "pull_request", Mode: "draft", Provider: "github", Base: "main"}
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// humanDeliveryWorkflow compiles a human_gate workflow ending in the reserved
// success route, optionally declaring a pull_request delivery policy.
func humanDeliveryWorkflow(t *testing.T, withDelivery bool) *compiler.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "human-delivery", InitialStep: "approve_me",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 4},
		Steps:  []definition.Step{{ID: "approve_me", Kind: "human_gate", OnFailure: "failure"}},
		Transitions: []definition.Transition{
			{From: "approve_me", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"decision": "approved"}}},
		},
	}
	if withDelivery {
		wf.Delivery = &definition.Delivery{Kind: "pull_request", Mode: "draft", Provider: "github", Base: "main"}
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func newDeliveryController(t *testing.T, repo workflowledger.Repository, runner AgentStepRunner, withDelivery bool, runID string) *LinearController {
	t.Helper()
	ctrl, err := NewLinearController(repo, runner, deliveryWorkflow(t, withDelivery), map[string]StepRuntime{
		"one": {Agent: agents.ResolvedAgent{Name: "worker"}},
	}, nil, runID, []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	return ctrl
}

// failingRunner fails every dispatched step with a fixed cause.
type failingRunner struct {
	cause error
}

func (r failingRunner) RunStep(context.Context, AgentStepRequest) (AgentStepResult, error) {
	return AgentStepResult{}, r.cause
}

func TestSuccessTerminalWithDraftPolicySettlesDeliveryPending(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runner := &linearRunner{outputs: map[string]json.RawMessage{"one": json.RawMessage(`{"ok":true}`)}}
	ctrl := newDeliveryController(t, repo, runner, true, "wfr-success-draft")
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending (not succeeded)", got.Status)
	}
	if got.ActiveStepID != "success" {
		t.Fatalf("active step = %q, want success", got.ActiveStepID)
	}
	stored, err := repo.GetRun(context.Background(), ctrl.RunID)
	if err != nil || stored.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("stored run = %+v, err = %v", stored, err)
	}
	// The settled run never re-dispatches and stays delivery_pending.
	again, done, err := ctrl.Advance(context.Background())
	if err != nil || !done || again.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("advance after settle = %+v, done = %v, err = %v", again, done, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.calls))
	}
}

func TestSuccessTerminalWithoutPolicyStaysSucceeded(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runner := &linearRunner{outputs: map[string]json.RawMessage{"one": json.RawMessage(`{"ok":true}`)}}
	ctrl := newDeliveryController(t, repo, runner, false, "wfr-success-no-policy")
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v, err = %v", got, err)
	}
}

func TestFailureTerminalStaysFailedWithPolicy(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	ctrl := newDeliveryController(t, repo, failingRunner{cause: errors.New("boom")}, true, "wfr-failure-with-policy")
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil {
		t.Fatal("failed run returned no error")
	}
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %q, want failed", got.Status)
	}
	stored, err := repo.GetRun(context.Background(), ctrl.RunID)
	if err != nil || stored.Status != workflowledger.RunStatusFailed {
		t.Fatalf("stored run = %+v, err = %v", stored, err)
	}
}

func TestDeliveryPendingNeverAdvancesToSucceeded(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	ctrl := newDeliveryController(t, repo, &linearRunner{}, true, "wfr-pending-settled")
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), ctrl.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	run, err = repo.GetRun(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), ctrl.RunID, run.Version, workflowledger.RunStatusDeliveryPending, nil); err != nil {
		t.Fatal(err)
	}
	settled, err := repo.GetRun(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	versionBefore := settled.Version
	got, done, err := ctrl.Advance(context.Background())
	if err != nil || !done || got.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("advance = %+v, done = %v, err = %v", got, done, err)
	}
	stored, err := repo.GetRun(context.Background(), ctrl.RunID)
	if err != nil || stored.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("stored run = %+v, err = %v", stored, err)
	}
	if stored.Version != versionBefore {
		t.Fatalf("settled pause bumped version: %d -> %d", versionBefore, stored.Version)
	}
}

func TestAdmissionRemoteURLRoundTrip(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	first, err := NewLinearController(repo, &linearRunner{}, deliveryWorkflow(t, false), nil, nil, "wfr-remote-url", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SetAdmission(Admission{RemoteURL: "https://github.com/o/r"}); err != nil {
		t.Fatal(err)
	}
	if err := first.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(context.Background(), first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RemoteURL != "https://github.com/o/r" {
		t.Fatalf("remote url = %q, want https://github.com/o/r", stored.RemoteURL)
	}
	second, err := NewLinearController(repo, &linearRunner{}, deliveryWorkflow(t, false), nil, nil, first.RunID, []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := second.SetAdmission(Admission{RemoteURL: "https://github.com/other/repo"}); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(context.Background()); err == nil {
		t.Fatal("changed remote url was accepted")
	}
}

func TestFinishHumanRunStatusRoutesDelivery(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, humanDeliveryWorkflow(t, true), nil, map[string]any{"task": "x"}, "wfr-human-delivery", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("status = %q, want waiting_approval", got.Status)
	}
	if err := ctrl.Approve(context.Background(), PendingApprovalID("approve_me", 1), "operator"); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(context.Background(), ctrl.RunID)
	if err != nil || stored.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("stored run = %+v, err = %v", stored, err)
	}
	got, err = ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run = %+v, err = %v", got, err)
	}
}
