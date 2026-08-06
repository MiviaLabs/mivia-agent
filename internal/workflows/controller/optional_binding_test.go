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

// feedbackWorkflow builds a two-step review loop where the implement step
// declares an OPTIONAL binding to the reviewer step output. This mirrors the
// feature-delivery review-repair loop: on the first attempt the prior review
// does not exist yet, so the optional binding must resolve to an empty value
// instead of failing. On repair iterations the binding must deliver the
// reviewer findings so the agent can address them.
func feedbackWorkflow(t *testing.T) *compiler.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "feedback", InitialStep: "implement",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 12},
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "dev", OnFailure: "failure",
				Context: []definition.ContextBinding{
					{From: "inputs.task", As: "task"},
					{From: "steps.review.output", As: "review_findings", MaxBytes: 16000, Optional: true},
				}},
			{ID: "review", Kind: "agent_gate", Agent: "rev", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
			{From: "review", To: "implement", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "changes_requested"}}, Loop: "review_repair", MaxIterations: -1},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// TestOptionalContextBindingDeliversReviewFindingsOnRepair verifies that an
// optional steps.X.output binding resolves to an empty string on the first
// attempt (no prior review exists) and to the reviewer output on repair.
func TestOptionalContextBindingDeliversReviewFindingsOnRepair(t *testing.T) {
	wf := feedbackWorkflow(t)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"v1","inspected":["a.go"]}`),
		"implement#2": json.RawMessage(`{"summary":"v2","inspected":["a.go"]}`),
		"review#1":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"severity":"high","reason":"x"}],"inspected":["a.go"]}`),
		"review#2":    json.RawMessage(`{"verdict":"approved","findings":[],"inspected":["a.go"]}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-optional-feedback", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) < 2 {
		t.Fatalf("expected at least 2 implement calls, got %d", len(runner.calls))
	}
	// First implement call: optional binding must be present but empty.
	firstFindings, ok := runner.calls[0].Evidence["review_findings"].(string)
	if !ok {
		t.Fatalf("first call review_findings = %#v, want empty string", runner.calls[0].Evidence["review_findings"])
	}
	if firstFindings != "" {
		t.Fatalf("first call review_findings = %q, want empty (no prior review)", firstFindings)
	}
	// The implement call that follows the rejection must carry the review output.
	var rejectionCall *AgentStepRequest
	for i := range runner.calls {
		if runner.calls[i].StepID == "implement" && i > 0 {
			rejectionCall = &runner.calls[i]
		}
	}
	if rejectionCall == nil {
		t.Fatal("no second implement call received")
	}
	findings := rejectionCall.Evidence["review_findings"]
	if findings == nil {
		t.Fatal("repair implement call did not receive review_findings")
	}
	raw, _ := json.Marshal(findings)
	if !strings.Contains(string(raw), "changes_requested") {
		t.Fatalf("repair review_findings = %s, want the rejection payload", raw)
	}
}

// TestOptionalContextBindingFailsClosedWhenNotOptional verifies that a
// non-optional steps.X.output binding still fails when the prior output is
// missing — the optional flag is opt-in, not a default.
func TestOptionalContextBindingFailsClosedWhenNotOptional(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "required-binding", InitialStep: "implement",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 4},
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "dev", OnFailure: "failure",
				Context: []definition.ContextBinding{
					{From: "inputs.task", As: "task"},
					{From: "steps.review.output", As: "review_findings", MaxBytes: 16000},
				}},
			{ID: "review", Kind: "agent_gate", Agent: "rev", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"v1"}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, compiled, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-required-binding", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil {
		t.Fatalf("run succeeded = %+v; want failure from missing non-optional binding", got)
	}
}
