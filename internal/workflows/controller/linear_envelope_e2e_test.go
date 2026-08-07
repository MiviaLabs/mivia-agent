package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// envelopeRepairWorkflow mirrors the feature-delivery plan/plan_review loop:
// the plan step binds the plan_review step's prior output (review_findings)
// and plan_review binds its OWN prior output (prior_findings). Both findings
// bindings are envelope_only with a 4096 cap — large enough for the
// production-sized reference envelope skeleton, small enough to prove the
// findings payload itself is never inlined.
func envelopeRepairWorkflow(t *testing.T) *compiler.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "envelope-repair", InitialStep: "plan",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 8},
		Steps: []definition.Step{
			{ID: "plan", Kind: "agent", Agent: "eng", OnFailure: "failure",
				Context: []definition.ContextBinding{
					{From: "inputs.task", As: "task"},
					{From: "steps.plan_review.output", As: "review_findings", MaxBytes: 4096, Optional: true, EnvelopeOnly: true},
				}},
			{ID: "plan_review", Kind: "agent_gate", Agent: "rev", OnFailure: "failure",
				Context: []definition.ContextBinding{
					{From: "inputs.task", As: "task"},
					{From: "steps.plan.output", As: "plan", MaxBytes: 24000},
					{From: "steps.plan_review.output", As: "prior_findings", MaxBytes: 4096, Optional: true, EnvelopeOnly: true},
				}},
		},
		Transitions: []definition.Transition{
			{From: "plan", To: "plan_review", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "plan_review", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
			{From: "plan_review", To: "plan", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "changes_requested"}}, Loop: "plan_review_repair", MaxIterations: 4},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// assertEnvelope asserts value is a ledger reference envelope pointing at the
// given prior step attempt, and returns the envelope map.
func assertEnvelope(t *testing.T, value any, wantStep string, wantAttempt int) map[string]any {
	t.Helper()
	env, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("evidence = %#v (%T), want a ledger reference envelope", value, value)
	}
	artifact, ok := env["artifact"].(map[string]any)
	if !ok {
		t.Fatalf("envelope = %#v, want an artifact key", env)
	}
	if artifact["step"] != wantStep || artifact["attempt"] != wantAttempt {
		t.Fatalf("artifact = %#v, want step=%q attempt=%d", artifact, wantStep, wantAttempt)
	}
	if _, hasPreview := artifact["preview"]; !hasPreview {
		t.Fatalf("artifact = %#v, want a preview", artifact)
	}
	return env
}

// TestEnvelopeOnlyFindingsSurvivePlanReviewBackEdge pins the Step-5 audit fix
// end to end: a changes_requested review round carrying two findings survives
// the plan_review -> plan back-edge, and the plan step's review_findings
// binding receives the prior review output as a ledger reference envelope
// (artifact pointer + note), never the inline payload. Before the fix the
// findings binding caps were too small to fit even the reference envelope
// skeleton, so every findings-bearing repair iteration failed the run.
func TestEnvelopeOnlyFindingsSurvivePlanReviewBackEdge(t *testing.T) {
	wf := envelopeRepairWorkflow(t)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"plan#1":        json.RawMessage(`{"summary":"v1","steps":[{"action":"read"}]}`),
		"plan_review#1": json.RawMessage(`{"verdict":"changes_requested","findings":[{"id":"R0-f1","severity":"high","reason":"x"},{"id":"R0-f2","severity":"medium","reason":"y"}]}`),
		"plan#2":        json.RawMessage(`{"summary":"v2","steps":[{"action":"write"}]}`),
		"plan_review#2": json.RawMessage(`{"verdict":"approved","findings":[]}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"plan":        {Agent: agents.ResolvedAgent{Name: "eng"}},
		"plan_review": {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-envelope-e2e", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v; want succeeded after one changes_requested repair round", got, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 4 {
		t.Fatalf("runner calls = %d, want 4 (plan, plan_review, plan, plan_review)", len(runner.calls))
	}
	// plan#1 (first round): no prior plan_review output -> optional-absent "".
	if v, ok := runner.calls[0].Evidence["review_findings"].(string); !ok || v != "" {
		t.Fatalf("plan#1 review_findings = %#v, want the optional-absent empty string", runner.calls[0].Evidence["review_findings"])
	}
	// plan_review#1 (first round): no prior own output -> optional-absent "".
	if v, ok := runner.calls[1].Evidence["prior_findings"].(string); !ok || v != "" {
		t.Fatalf("plan_review#1 prior_findings = %#v, want the optional-absent empty string", runner.calls[1].Evidence["prior_findings"])
	}
	// plan#2 (repair round): review_findings must be the reference envelope for
	// plan_review attempt 1 — never the inline findings payload.
	assertEnvelope(t, runner.calls[2].Evidence["review_findings"], "plan_review", 1)
	// plan_review#2 (repair round): prior_findings must be the reference
	// envelope for plan_review attempt 1.
	assertEnvelope(t, runner.calls[3].Evidence["prior_findings"], "plan_review", 1)
}
