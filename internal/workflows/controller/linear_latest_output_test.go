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

// selfBindingWorkflow builds a review-repair loop whose review step binds its
// OWN prior output (prior_findings) as an optional evidence value, mirroring
// the convergence-plan review template.
func selfBindingWorkflow(t *testing.T) *definition.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "self-bind", InitialStep: "implement",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 12},
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "dev", OnFailure: "failure"},
			{ID: "review", Kind: "agent_gate", Agent: "rev", OnFailure: "failure",
				Context: []definition.ContextBinding{{From: "steps.review.output", As: "prior_findings", Optional: true}}},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
			{From: "review", To: "implement", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "changes_requested"}}, Loop: "review_repair", MaxIterations: -1},
		},
	}
	compiled, err := definition.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// TestReviewSelfBindingResolvesPreviousCompletedReview pins convergence plan
// v3 part 1 end to end: a review step that binds its OWN prior output must
// resolve to the previous COMPLETED review (recorded in the ledger), not its
// own in-flight attempt (which has no OutputRef yet). On the first round the
// optional binding stays empty.
func TestReviewSelfBindingResolvesPreviousCompletedReview(t *testing.T) {
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"v1"}`),
		"review#1":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"id":"R0-F1","severity":"high","reason":"x"}]}`),
		"implement#2": json.RawMessage(`{"summary":"v2"}`),
		"review#2":    json.RawMessage(`{"verdict":"approved","findings":[]}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, selfBindingWorkflow(t), map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-self-bind", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	var reviewCalls []AgentStepRequest
	for _, call := range runner.calls {
		if call.StepID == "review" {
			reviewCalls = append(reviewCalls, call)
		}
	}
	if len(reviewCalls) != 2 {
		t.Fatalf("review calls = %d, want 2", len(reviewCalls))
	}
	first, ok := reviewCalls[0].Evidence["prior_findings"].(string)
	if !ok || first != "" {
		t.Fatalf("first review prior_findings = %#v, want empty (no prior review yet)", reviewCalls[0].Evidence["prior_findings"])
	}
	second, ok := reviewCalls[1].Evidence["prior_findings"].(map[string]any)
	if !ok {
		t.Fatalf("second review prior_findings = %#v (%T), want the previous review output object", reviewCalls[1].Evidence["prior_findings"], reviewCalls[1].Evidence["prior_findings"])
	}
	raw, _ := json.Marshal(second)
	if !strings.Contains(string(raw), "R0-F1") || !strings.Contains(string(raw), "changes_requested") {
		t.Fatalf("second review prior_findings = %s, want review#1's output", raw)
	}
}
