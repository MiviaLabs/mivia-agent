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

// TestFailAttemptPersistsErrorRef pins Defect 2: an attempt failed before the
// child ran (a step that cannot even build its dispatch request) must carry a
// loadable ErrorRef naming its cause, never an empty AgentStepResult.
func TestFailAttemptPersistsErrorRef(t *testing.T) {
	wf := linearWorkflow(t)
	// Break the second step's context so agentStepRequest fails after the
	// attempt is admitted: failAttempt completes it Failed.
	wf.Steps[1].Context = []definition.ContextBinding{{From: "steps.missing.output", As: "x"}}
	repo := workflowledger.NewMemoryRepository()
	runner := &linearRunner{outputs: map[string]json.RawMessage{"first": json.RawMessage(`{"ok":true}`)}}
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"first":  {Agent: agents.ResolvedAgent{Name: "one"}},
		"second": {Agent: agents.ResolvedAgent{Name: "two"}},
	}, map[string]any{"task": "build"}, "wfr-error-ref", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := ctrl.Run(context.Background())
	if err == nil || run.Status != workflowledger.RunStatusFailed || !strings.Contains(err.Error(), "missing prior output") {
		t.Fatalf("run = %+v, error = %v", run, err)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts = %d, error = %v", len(attempts), err)
	}
	var failed workflowledger.StepAttempt
	for _, attempt := range attempts {
		if attempt.StepID == "second" {
			failed = attempt
		}
	}
	if failed.StepID != "second" || failed.Status != workflowledger.AttemptStatusFailed || failed.ErrorRef == "" {
		t.Fatalf("failed attempt = %+v, want Failed with a non-empty ErrorRef", failed)
	}
	text, err := repo.LoadContent(context.Background(), failed.ErrorRef)
	if err != nil {
		t.Fatalf("error ref %q is not loadable: %v", failed.ErrorRef, err)
	}
	if !strings.Contains(string(text), "missing prior output") {
		t.Fatalf("error text = %q, want the cause", text)
	}
}

// TestSettleAgentAttemptRouteFailurePersistsErrorRef pins the silent-failure
// class in settleAgentAttempt: a SUCCEEDED child whose route selection fails
// flips the attempt to Failed and must persist a loadable ErrorRef naming the
// route-selection cause.
func TestSettleAgentAttemptRouteFailurePersistsErrorRef(t *testing.T) {
	wf := linearWorkflow(t)
	// Drop the transition out of "first": the child succeeds but selectRoute
	// finds no matching transition.
	wf.Transitions = wf.Transitions[1:]
	repo := workflowledger.NewMemoryRepository()
	runner := &linearRunner{outputs: map[string]json.RawMessage{"first": json.RawMessage(`{"ok":true}`)}}
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"first": {Agent: agents.ResolvedAgent{Name: "one"}},
	}, map[string]any{"task": "build"}, "wfr-route-error-ref", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := ctrl.Run(context.Background())
	if err == nil || run.Status != workflowledger.RunStatusFailed || !strings.Contains(err.Error(), "no matching transition") {
		t.Fatalf("run = %+v, error = %v", run, err)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts = %d, error = %v", len(attempts), err)
	}
	attempt := attempts[0]
	if attempt.Status != workflowledger.AttemptStatusFailed || attempt.ErrorRef == "" {
		t.Fatalf("attempt = %+v, want Failed with a non-empty ErrorRef", attempt)
	}
	text, err := repo.LoadContent(context.Background(), attempt.ErrorRef)
	if err != nil {
		t.Fatalf("error ref %q is not loadable: %v", attempt.ErrorRef, err)
	}
	if !strings.Contains(string(text), "no matching transition") {
		t.Fatalf("error text = %q, want the route-selection cause", text)
	}
}
