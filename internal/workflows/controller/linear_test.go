package controller

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

type linearRunner struct {
	mu      sync.Mutex
	calls   []AgentStepRequest
	outputs map[string]json.RawMessage
}

type canceledLinearRunner struct{}

func (canceledLinearRunner) RunStep(context.Context, AgentStepRequest) (AgentStepResult, error) {
	return AgentStepResult{}, context.Canceled
}

func (r *linearRunner) RunStep(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, req)
	r.mu.Unlock()
	return AgentStepResult{CoordinatorRunID: "coord-" + req.StepID, TaskID: req.TaskID, Output: r.outputs[req.StepID], EvidenceJSON: []byte(`[]`)}, nil
}

func linearWorkflow(t *testing.T) *compiler.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "linear", InitialStep: "first",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}, "secret": {Type: "string"}},
		Steps: []definition.Step{
			{ID: "first", Kind: "agent", Agent: "one", Context: []definition.ContextBinding{{From: "inputs.task", As: "task"}}},
			{ID: "second", Kind: "agent", Agent: "two", Context: []definition.ContextBinding{{From: "steps.first.output", As: "first"}}},
		},
		Transitions: []definition.Transition{
			{From: "first", To: "second", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "second", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func TestLinearControllerRunsTwoStepsAndPersistsChildReferences(t *testing.T) {
	wf := linearWorkflow(t)
	runner := &linearRunner{outputs: map[string]json.RawMessage{"first": json.RawMessage(`{"ok":true}`), "second": json.RawMessage(`{"done":true}`)}}
	repo := workflowledger.NewMemoryRepository()
	snapshot, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{SchemaVersion: 1, DefinitionTOML: []byte("linear"), DefinitionDigest: wf.Digest})
	if err != nil {
		t.Fatal(err)
	}
	steps := map[string]StepRuntime{
		"first":  {Agent: agents.ResolvedAgent{Name: "one"}, Digest: "sha256:one"},
		"second": {Agent: agents.ResolvedAgent{Name: "two"}, Digest: "sha256:two"},
	}
	ctrl, err := NewLinearController(repo, runner, wf, steps, map[string]any{"task": "build"}, "wfr-linear", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v, err=%v", got, err)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts = %d, err=%v", len(attempts), err)
	}
	for _, attempt := range attempts {
		if attempt.CoordinatorRunID == "" || attempt.TaskID == "" || attempt.OutputRef == "" {
			t.Fatalf("attempt has incomplete child identity or output: %+v", attempt)
		}
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 2 || runner.calls[0].Inputs["secret"] != nil || runner.calls[1].Inputs["secret"] != nil || runner.calls[1].Evidence["first"] == nil {
		t.Fatalf("calls = %+v", runner.calls)
	}
}

func TestLinearControllerRejectsLoopAndDuplicateSnapshot(t *testing.T) {
	wf := linearWorkflow(t)
	wf.Transitions[0].Loop = "repeat"
	_, err := NewLinearController(workflowledger.NewMemoryRepository(), &linearRunner{}, wf, nil, nil, "wfr-loop", []byte("snapshot"))
	if err == nil {
		t.Fatal("loop was accepted")
	}
	wf = linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	runner := &linearRunner{outputs: map[string]json.RawMessage{"first": json.RawMessage(`{}`), "second": json.RawMessage(`{}`)}}
	first, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{"first": {Agent: agents.ResolvedAgent{Name: "one"}}, "second": {Agent: agents.ResolvedAgent{Name: "two"}}}, map[string]any{"task": "a"}, "wfr-duplicate", []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := NewLinearController(repo, runner, wf, nil, map[string]any{"task": "b"}, "wfr-duplicate", []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Start(context.Background()); err == nil {
		t.Fatal("different duplicate snapshot was accepted")
	}
}

func TestLinearControllerResumesRecordedAttemptWithoutNewAttempt(t *testing.T) {
	wf := linearWorkflow(t)
	runner := &linearRunner{outputs: map[string]json.RawMessage{"first": json.RawMessage(`{"ok":true}`)}}
	repo := workflowledger.NewMemoryRepository()
	snapshot := []byte("resume-snapshot")
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"first":  {Agent: agents.ResolvedAgent{Name: "one"}},
		"second": {Agent: agents.ResolvedAgent{Name: "two"}},
	}, map[string]any{"task": "build"}, "wfr-resume", snapshot)
	if err != nil {
		t.Fatal(err)
	}
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
	if err := repo.CreateStepAttempt(context.Background(), workflowledger.StepAttempt{
		AttemptID: "wfa-first-1", RunID: ctrl.RunID, StepID: "first", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning, CoordinatorRunID: "coord-existing", TaskID: "task-existing",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ctrl.Advance(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 1 || runner.calls[0].CoordinatorRunID != "coord-existing" || runner.calls[0].TaskID != "task-existing" {
		t.Fatalf("resume request = %+v", runner.calls)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts = %d, err=%v", len(attempts), err)
	}
}

func TestLinearControllerPersistsCanceledAttempt(t *testing.T) {
	wf := linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, canceledLinearRunner{}, wf, map[string]StepRuntime{
		"first": {Agent: agents.ResolvedAgent{Name: "one"}},
	}, map[string]any{"task": "build"}, "wfr-canceled", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ctrl.Run(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts = %d, err=%v", len(attempts), err)
	}
	if attempts[0].Status != workflowledger.AttemptStatusCanceled {
		t.Fatalf("attempt status = %q, want canceled", attempts[0].Status)
	}
}
