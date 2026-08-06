package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// errorRunner returns a scripted result/error per call and records requests.
type errorRunner struct {
	mu      sync.Mutex
	calls   []AgentStepRequest
	results map[string]AgentStepResult
	errors  map[string]error
}

func (r *errorRunner) RunStep(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, req)
	if res, ok := r.results[req.StepID]; ok {
		return res, r.errors[req.StepID]
	}
	return AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID}, r.errors[req.StepID]
}

func errorWorkflow(t *testing.T) *compiler.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "errorflow", InitialStep: "one",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 3},
		Steps: []definition.Step{
			{ID: "one", Kind: "agent", Agent: "dev", OnFailure: "failure",
				Context: []definition.ContextBinding{{From: "inputs.task", As: "task"}}},
		},
		Transitions: []definition.Transition{
			{From: "one", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func newErrorController(t *testing.T, runner AgentStepRunner, runID string) (*LinearController, *workflowledger.StorageRepository) {
	t.Helper()
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, errorWorkflow(t), map[string]StepRuntime{
		"one": {Agent: agents.ResolvedAgent{Name: "dev"}},
	}, map[string]any{"task": "x"}, runID, []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	return ctrl, repo
}

// TestFailedAttemptPersistsErrorRef pins that a failed agent attempt stores
// its error text content-addressed and exposes the reference.
func TestFailedAttemptPersistsErrorRef(t *testing.T) {
	runner := &errorRunner{errors: map[string]error{"one": errors.New("child exploded: boom")}}
	ctrl, repo := newErrorController(t, runner, "wfr-err-ref")
	got, err := ctrl.Run(context.Background())
	if err == nil {
		t.Fatalf("run succeeded = %+v; want failure", got)
	}
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != workflowledger.AttemptStatusFailed {
		t.Fatalf("attempts = %+v, want one failed attempt", attempts)
	}
	if attempts[0].ErrorRef == "" {
		t.Fatal("attempt ErrorRef is empty; want content-addressed error detail")
	}
	body, err := repo.LoadContent(context.Background(), attempts[0].ErrorRef)
	if err != nil {
		t.Fatalf("load error content: %v", err)
	}
	if !strings.Contains(string(body), "child exploded") {
		t.Fatalf("error content %q does not name the cause", body)
	}
}

// TestAttemptTimeoutDerivedFromRunDeadline pins that a child without its own
// timeout is bounded by the remaining run deadline.
func TestAttemptTimeoutDerivedFromRunDeadline(t *testing.T) {
	runner := &errorRunner{results: map[string]AgentStepResult{
		"one": {Output: json.RawMessage(`{"ok":true}`), Status: "completed"},
	}}
	ctrl, _ := newErrorController(t, runner, "wfr-deadline-timeout")
	now := time.Date(2026, 8, 6, 14, 0, 0, 0, time.UTC)
	deadline := now.Add(30 * time.Minute)
	if err := ctrl.SetTimeSource(func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetAdmission(Admission{DeadlineAt: &deadline}); err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.calls))
	}
	if want := 30 * time.Minute; runner.calls[0].Timeout != want {
		t.Fatalf("child timeout = %s, want %s", runner.calls[0].Timeout, want)
	}
	// Budget stays 0: the coordinator pool's MaxBudget is a step-weight bound
	// (default 1000), not a seconds budget, and Task.Timeout carries the step
	// duration. Setting Budget to timeout-seconds rejected every step longer
	// than the pool cap at dispatch ("budget limit exceeded").
	if runner.calls[0].Budget != 0 {
		t.Fatalf("child budget = %d, want 0 (step duration is enforced by Timeout, not the pool Budget)", runner.calls[0].Budget)
	}
}

// TestChildTimedOutStatusUpgradesFailure pins that a plain child error whose
// recorded status says timed_out classifies the attempt as timed_out.
func TestChildTimedOutStatusUpgradesFailure(t *testing.T) {
	runner := &errorRunner{
		results: map[string]AgentStepResult{
			"one": {Status: "timed_out", Output: nil},
		},
		errors: map[string]error{"one": errors.New("provider hang")},
	}
	ctrl, repo := newErrorController(t, runner, "wfr-child-timeout")
	got, err := ctrl.Run(context.Background())
	if err == nil {
		t.Fatalf("run succeeded = %+v; want timed out", got)
	}
	if got.Status != workflowledger.RunStatusTimedOut {
		t.Fatalf("status = %q, want timed_out", got.Status)
	}
	attempts, _ := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if len(attempts) != 1 || attempts[0].Status != workflowledger.AttemptStatusTimedOut {
		t.Fatalf("attempts = %+v, want one timed_out attempt", attempts)
	}
}

// TestSucceededOutputPreservedOnStepError pins that a succeeded child whose
// step-level join fails keeps its output and gains an error reference.
func TestSucceededOutputPreservedOnStepError(t *testing.T) {
	runner := &errorRunner{
		results: map[string]AgentStepResult{
			"one": {Output: json.RawMessage(`{"ok":true}`), Status: "completed"},
		},
		errors: map[string]error{"one": errors.New("join failed")},
	}
	ctrl, repo := newErrorController(t, runner, "wfr-output-preserved")
	if _, err := ctrl.Run(context.Background()); err == nil {
		t.Fatal("run succeeded; want failure")
	}
	attempts, _ := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	a := attempts[0]
	if a.OutputRef == "" {
		t.Fatal("attempt OutputRef is empty; succeeded child output must survive")
	}
	if a.ErrorRef == "" {
		t.Fatal("attempt ErrorRef is empty; step error must be recorded")
	}
}
