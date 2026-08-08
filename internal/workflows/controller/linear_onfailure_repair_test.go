package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// These tests pin the DC-9 fix for agent/agent_gate failures: a genuine agent
// failure on a step that declares a NON-TERMINAL on_failure target must honor
// that target (the run advances to the declared repair step) instead of always
// failing the run while recording a route nobody follows. Degraded failures
// (route-selection, zero-progress) stay hard failures regardless of on_failure.

// onFailureRepairWorkflow returns a workflow whose agent step declares a
// non-terminal on_failure target ("repair"). The implement -> repair "failed"
// transition is never matched at runtime (agent steps route failures through
// failureRoute, never the matcher); it exists only because the compiler's
// reachability check requires every step to be reachable through transitions,
// while the repair step is actually reached via on_failure.
func onFailureRepairWorkflow(t *testing.T) *compiler.CompiledWorkflow {
	t.Helper()
	return compileOnFailureWorkflow(t, false)
}

// onFailureRepairLoopWorkflow returns the same workflow with a repair ->
// implement back-edge, so a permanently failing implement keeps re-entering
// the repair step until the on_failure re-entry budget is spent.
func onFailureRepairLoopWorkflow(t *testing.T) *compiler.CompiledWorkflow {
	t.Helper()
	return compileOnFailureWorkflow(t, true)
}

func compileOnFailureWorkflow(t *testing.T, loopBack bool) *compiler.CompiledWorkflow {
	t.Helper()
	transitions := []definition.Transition{
		{From: "implement", To: "repair", Match: definition.MatchCriteria{Status: "failed"}},
		{From: "implement", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
	}
	repairTarget := "success"
	if loopBack {
		repairTarget = "implement"
	}
	transitions = append(transitions, definition.Transition{
		From: "repair", To: repairTarget, Match: definition.MatchCriteria{Status: "succeeded"},
	})
	wf := &definition.WorkflowFile{
		Version: 1, Name: "onfailure-repair", InitialStep: "implement",
		Limits: definition.Limits{MaxStepAttempts: 16},
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "dev", OnFailure: "repair"},
			{ID: "repair", Kind: "agent", Agent: "dev", OnFailure: "failure"},
		},
		Transitions: transitions,
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func newOnFailureRepairController(t *testing.T, runner AgentStepRunner, wf *compiler.CompiledWorkflow, runID string) (*LinearController, workflowledger.Repository) {
	t.Helper()
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"repair":    {Agent: agents.ResolvedAgent{Name: "dev"}},
	}, map[string]any{"task": "x"}, runID, []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	return ctrl, repo
}

// (1) A genuine agent failure on a step whose on_failure names a non-terminal
// step runs the repair step and the run continues to success.
func TestAgentFailureRoutesToNonTerminalOnFailureRepairStep(t *testing.T) {
	wf := onFailureRepairWorkflow(t)
	runner := &scriptedRunner{
		outputsByStepCall: map[string]json.RawMessage{
			"repair#1": json.RawMessage(`{"summary":"repaired"}`),
		},
		failOn: map[string]error{
			"implement#1": errors.New("agent infrastructure boom"),
		},
	}
	ctrl, repo := newOnFailureRepairController(t, runner, wf, "wfr-onfailure-repair")
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v; want the run to advance to the declared repair step and succeed", got, err)
	}
	attempts, _ := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	var implement, repair workflowledger.StepAttempt
	for _, a := range attempts {
		switch a.StepID {
		case "implement":
			implement = a
		case "repair":
			repair = a
		}
	}
	if implement.Status != workflowledger.AttemptStatusFailed || implement.ToStepID != "repair" {
		t.Fatalf("implement attempt = %+v; want failed routed to the declared repair step", implement)
	}
	if repair.Status != workflowledger.AttemptStatusSucceeded || repair.ToStepID != "success" {
		t.Fatalf("repair attempt = %+v; want succeeded routed to success", repair)
	}
	// The run is not failed: the declared on_failure target was honored.
	if got.Status == workflowledger.RunStatusFailed {
		t.Fatal("run failed; the documented non-terminal on_failure target was not honored")
	}
}

// (2) The repair step itself failing fails the run: no infinite loop.
func TestAgentFailureOnFailureRepairAlsoFailsStopsRun(t *testing.T) {
	wf := onFailureRepairWorkflow(t)
	runner := &scriptedRunner{
		failOn: map[string]error{
			"implement#1": errors.New("agent infrastructure boom"),
			"repair#1":    errors.New("repair step also failed"),
		},
	}
	ctrl, repo := newOnFailureRepairController(t, runner, wf, "wfr-onfailure-repair-fails")
	got, err := ctrl.Run(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run = %+v err=%v; want failed when the repair step itself fails", got, err)
	}
	attempts, _ := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want exactly 2 (implement#1 failed, repair#1 failed); no infinite loop: %+v", len(attempts), attempts)
	}
}

// (3) A permanently failing implement keeps re-entering repair until the
// on_failure re-entry budget is spent, then the run fails. The final exhausted
// failure records the terminal failure route, not the un-honored repair target.
func TestAgentFailureOnFailureRepairIsBounded(t *testing.T) {
	wf := onFailureRepairLoopWorkflow(t)
	runner := &scriptedRunner{
		outputsByStepCall: map[string]json.RawMessage{
			"repair#*": json.RawMessage(`{"summary":"repaired"}`),
		},
		failOn: map[string]error{
			"implement#1": errors.New("boom"),
			"implement#2": errors.New("boom"),
			"implement#3": errors.New("boom"),
			"implement#4": errors.New("boom"),
		},
	}
	ctrl, repo := newOnFailureRepairController(t, runner, wf, "wfr-onfailure-repair-bounded")
	got, err := ctrl.Run(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run = %+v err=%v; want failed once the on_failure re-entry budget is spent", got, err)
	}
	attempts, _ := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	implementRuns, repairRuns := 0, 0
	var lastImplement workflowledger.StepAttempt
	for _, a := range attempts {
		switch a.StepID {
		case "implement":
			implementRuns++
			if a.AttemptNo > lastImplement.AttemptNo {
				lastImplement = a
			}
		case "repair":
			repairRuns++
		}
	}
	// implement#1 fails -> repair#1; implement#2 fails -> repair#2;
	// implement#3 fails with the budget spent -> run fails.
	if implementRuns != 3 || repairRuns != 2 {
		t.Fatalf("implement ran %d times (want 3), repair ran %d times (want 2): %+v", implementRuns, repairRuns, attempts)
	}
	if implementRuns > maxOnFailureReentries+1 {
		t.Fatalf("implement ran %d times, want it bounded near %d", implementRuns, maxOnFailureReentries)
	}
	if lastImplement.Status != workflowledger.AttemptStatusFailed || lastImplement.ToStepID != "failure" {
		t.Fatalf("last implement attempt = %+v; want failed routed to the terminal failure once the budget is spent", lastImplement)
	}
}

// (4) Regression guard: a zero-progress degradation on a review whose
// on_failure names a NON-TERMINAL step must STILL fail the run. Degraded
// failures (route-selection, zero-progress) are not genuine agent failures and
// must not divert to the on_failure target.
func TestReviewZeroProgressNonTerminalOnFailureStillFailsRun(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "zero-progress-nonterminal-onfailure", InitialStep: "implement",
		Limits: definition.Limits{MaxStepAttempts: 16},
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "dev", OnFailure: "failure"},
			{ID: "review", Kind: "agent_gate", Agent: "rev", OnFailure: "repair"},
			{ID: "repair", Kind: "agent", Agent: "dev", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
			{From: "review", To: "implement", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "changes_requested"}}, Loop: "review_repair", MaxIterations: 30},
			// Dead at runtime for agent_gate steps (failures route via
			// failureRoute); exists for the compiler's reachability check.
			{From: "review", To: "repair", Match: definition.MatchCriteria{Status: "failed"}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"v1"}`),
		"review#1":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"id":"R0-f1","severity":"high","reason":"x"}]}`),
		"implement#2": json.RawMessage(`{"summary":"v2"}`),
		"review#2":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"id":"R1-f1","severity":"high","reason":"x"}]}`),
		"implement#3": json.RawMessage(`{"summary":"v3"}`),
		"review#3":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"id":"R2-f1","severity":"high","reason":"x"}]}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, compiled, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
		"repair":    {Agent: agents.ResolvedAgent{Name: "dev"}},
	}, map[string]any{"task": "x"}, "wfr-zero-progress-onfailure", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run = %+v err=%v; a zero-progress degradation must fail the run even with a non-terminal on_failure", got, err)
	}
	if !strings.Contains(err.Error(), "review made no progress across rounds") {
		t.Fatalf("err = %v, want the zero-progress cause", err)
	}
	// The degraded review must NOT divert to its non-terminal on_failure
	// target: the repair step must never run.
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, call := range runner.calls {
		if call.StepID == "repair" {
			t.Fatalf("repair ran after a zero-progress degradation; degraded failures must not route: %+v", runner.calls)
		}
	}
	if len(runner.calls) != 6 {
		t.Fatalf("runner calls = %d, want 6 (two repairs, no repair dispatch after degradation)", len(runner.calls))
	}
}
