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

func covMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestCoveragePlanModeStackingRunRecordsDigests(t *testing.T) {
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{"plan#1": json.RawMessage(`{"summary":"s"}`), "decompose#1": json.RawMessage(decomposeValidPlan), "chunk_plan_validate#1": json.RawMessage(`{"valid":true,"reasons":[]}`)}}
	ctrl, err := newStackingController(t, runner, stackingFixture(t), map[string]any{"task": "build"})
	covMust(t, err)
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v; want success", got, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.stepCalls["decompose"] != 1 || runner.stepCalls["chunk_plan_validate"] != 1 {
		t.Fatalf("synthesized steps dispatched = decompose:%d gate:%d; want 1 each", runner.stepCalls["decompose"], runner.stepCalls["chunk_plan_validate"])
	}
	attempts, err := ctrl.Repo.ListStepAttempts(context.Background(), ctrl.RunID)
	covMust(t, err)
	for _, a := range attempts {
		if (a.StepID == "decompose" || a.StepID == "chunk_plan_validate") && (a.MatchDigest == "" || len(a.DecisionJSON) == 0 || (a.Status == workflowledger.AttemptStatusSucceeded && a.ToStepID == "")) {
			t.Errorf("attempt %s missing route digests: %+v", a.StepID, a)
		}
	}
	transitions, err := ctrl.Repo.ListTransitions(context.Background(), ctrl.RunID)
	covMust(t, err)
	saw := map[string]bool{}
	for _, tr := range transitions {
		if tr.ToStepID == "decompose" || tr.ToStepID == "chunk_plan_validate" {
			saw[tr.ToStepID] = true
			if tr.MatchDigest == "" {
				t.Errorf("transition to %s has empty MatchDigest", tr.ToStepID)
			}
		}
	}
	if !saw["decompose"] || !saw["chunk_plan_validate"] {
		t.Errorf("ListTransitions missing synthesized routes: %v", saw)
	}
}
func TestCoverageChunkPlanGateFailsClosedOnEnvelopeUnmarshal(t *testing.T) {
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{"plan#1": json.RawMessage(`{"summary":"s"}`), "decompose#1": json.RawMessage(`{"stack_mode":"multi","chunk_plan":{"chunks":[{"id":123}]}}`)}}
	ctrl, err := newStackingController(t, runner, stackingFixture(t), map[string]any{"task": "build"})
	covMust(t, err)
	got, err := ctrl.Run(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed || !strings.Contains(err.Error(), "chunk plan validation failed") {
		t.Fatalf("run = %+v err=%v; want fail-closed 'chunk plan validation failed'", got, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.stepCalls["chunk_plan_validate"] != 0 {
		t.Fatalf("chunk_plan_validate dispatched %d times; want 0", runner.stepCalls["chunk_plan_validate"])
	}
}
func TestCoverageValidateChunkPlanStructuredInputs(t *testing.T) {
	cfg := stackingConfigFixture()
	for _, tt := range []struct{ name, raw, want string }{
		{"empty chunk list", chunkPlanJSON(), "no chunks"},
		{"duplicate chunk id", chunkPlanJSON(chunkJSON("c1", `["a.go"]`, 5, true, nil), chunkJSON("c1", `["b.go"]`, 5, true, nil)), "appears more than once"},
		{"self-dependency", chunkPlanJSON(chunkJSON("c1", `["a.go"]`, 5, true, []string{"c1"})), "depends on itself"},
		{"oversized payload", strings.Repeat("a", 70<<10), "exceeds"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, err := ValidateChunkPlan(json.RawMessage(tt.raw), cfg)
			if tt.want == "exceeds" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("err = %v, want error mentioning %q", err, tt.want)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected decode error: %v", err)
			}
			if !strings.Contains(strings.Join(out.Reasons, "\n"), tt.want) {
				t.Fatalf("reasons %v do not mention %q", out.Reasons, tt.want)
			}
		})
	}
}
func TestCoverageUnmatchedOutputFailsClosed(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "review-route", InitialStep: "plan",
		Inputs:      map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits:      definition.Limits{MaxStepAttempts: 4},
		Steps:       []definition.Step{{ID: "plan", Kind: "agent", Agent: "dev", OnFailure: "failure"}},
		Transitions: []definition.Transition{{From: "plan", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}}},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Stacking != nil {
		t.Fatal("single-step review-route workflow resolved stacking")
	}
	for _, tt := range []struct {
		name, output string
		ok           bool
	}{
		{"output missing required transition key", `{"other":"x"}`, false},
		{"output with wrong value", `{"verdict":"rejected"}`, false},
		{"empty output object", `{}`, false},
		{"matching value routes to success", `{"verdict":"approved"}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{"plan#1": json.RawMessage(tt.output)}}
			ctrl, err := NewLinearController(workflowledger.NewMemoryRepository(), runner, compiled, map[string]StepRuntime{"plan": {Agent: agents.ResolvedAgent{Name: "dev"}}}, map[string]any{"task": "build"}, "wfr-unmatched", []byte("snap"))
			covMust(t, err)
			got, err := ctrl.Run(context.Background())
			if tt.ok && (err != nil || got.Status != workflowledger.RunStatusSucceeded) {
				t.Fatalf("control run = %+v err=%v; want success", got, err)
			}
			if !tt.ok && (err == nil || got.Status != workflowledger.RunStatusFailed || !strings.Contains(err.Error(), "no matching transition")) {
				t.Fatalf("run = %+v err=%v; want fail-closed 'no matching transition'", got, err)
			}
		})
	}
}
func FuzzValidateChunkPlan(f *testing.F) {
	cfg := stackingConfigFixture()
	f.Add(decomposeValidPlan)
	f.Add(`{"stack_mode":"single"}`)
	f.Add(`{"stack_mode":"no_bug"}`)
	f.Add(`{"stack_mode":`)
	f.Add(strings.Repeat("a", 70<<10))
	f.Add(chunkPlanJSON())
	f.Add(chunkPlanJSON(chunkJSON("c1", `["a.go"]`, 5, true, nil), chunkJSON("c1", `["b.go"]`, 5, true, nil)))
	f.Add(chunkPlanJSON(chunkJSON("c1", `["a.go"]`, 5, true, []string{"c2"}), chunkJSON("c2", `["b.go"]`, 5, true, []string{"c1"})))
	f.Fuzz(func(t *testing.T, raw string) { _, _ = ValidateChunkPlan(json.RawMessage(raw), cfg) })
}
