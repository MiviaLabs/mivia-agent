package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// planWithStepsJSON is a plan-v1-shaped output declaring concrete
// implementation steps - the exact shape the confirmed no_bug bug was judged
// against (observed run wfr-inv-b252179884a57b2b9411fb34d30371fa: the plan
// declared 7 steps and decompose still settled no_bug with a zero-diff
// delivery).
const planWithStepsJSON = `{"summary":"implement the feature","steps":["add tests","implement executeWorkflowEventsJSON","wire --json flag"],"inspected":["internal/cli/workflow_json.go"],"addressed_findings":[]}`

// decomposeNoBugJSON is the decompose verdict that must never settle a plan
// declaring actionable steps.
const decomposeNoBugJSON = `{"stack_mode":"no_bug","chunk_plan":{"chunks":[]}}`

// decomposeSingleJSON is a valid single-chunk plan that drives the implement
// step.
const decomposeSingleJSON = `{"stack_mode":"single","chunk_plan":{"chunks":[` +
	`{"id":"c1","title":"t","files":["internal/cli/workflow_json.go"],"est_diff_lines":10,"tests":true,"depends_on":[]}]}}`

// stackingSingleRunOutputs scripts the full single-mode happy path: plan with
// steps, one accepted single chunk, implement, and the verify gate.
func stackingSingleRunOutputs() map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"plan#1":      json.RawMessage(planWithStepsJSON),
		"decompose#2": json.RawMessage(decomposeSingleJSON),
		"implement#1": json.RawMessage(`{"files_changed":["internal/cli/workflow_json.go"],"summary":"s","inspected":["x"],"addressed_findings":[],"pr_title":"feat: events json","pr_summary":"s"}`),
		"verify#1":    json.RawMessage(`{"verdict":"approved"}`),
	}
}

// TestStackingDecomposeNoBugWithActionablePlanReroutesToRepair pins the
// confirmed bug: a decompose no_bug verdict on a plan that declares steps
// must NOT settle success. It is rerouted back to decompose through the
// bounded repair loop, and a corrected single-chunk plan drives implement to
// success.
func TestStackingDecomposeNoBugWithActionablePlanReroutesToRepair(t *testing.T) {
	wf := stackingFixture(t)
	outputs := stackingSingleRunOutputs()
	outputs["decompose#1"] = json.RawMessage(decomposeNoBugJSON)
	runner := &scriptedRunner{outputsByStepCall: outputs}
	ctrl, err := newStackingController(t, runner, wf, map[string]any{"task": "build"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v; want success after decompose repair", got, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.stepCalls["decompose"] != 2 {
		t.Fatalf("decompose ran %d times; want 2 (no_bug rejected, single accepted)", runner.stepCalls["decompose"])
	}
	if runner.stepCalls["implement"] != 1 {
		t.Fatalf("implement ran %d times; want 1", runner.stepCalls["implement"])
	}
	counters, err := ctrl.Repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, lc := range counters {
		if lc.LoopName == "decompose_repair" && lc.Iterations != 1 {
			t.Fatalf("decompose_repair iterations = %d; want 1", lc.Iterations)
		}
	}
}

// TestStackingDecomposeNoBugWithActionablePlanFailsClosedOnExhaustion pins
// the fail-closed bound: a decompose agent that keeps returning no_bug for an
// actionable plan must fail the run honestly after the repair loop is
// exhausted - never settle success with the planned work silently dropped
// (the observed zero-diff delivery outcome).
func TestStackingDecomposeNoBugWithActionablePlanFailsClosedOnExhaustion(t *testing.T) {
	wf := stackingFixture(t)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"plan#1":      json.RawMessage(planWithStepsJSON),
		"decompose#*": json.RawMessage(decomposeNoBugJSON),
	}}
	ctrl, err := newStackingController(t, runner, wf, map[string]any{"task": "build"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil {
		t.Fatal("run must fail when no_bug is repeated for an actionable plan")
	}
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("status = %s; want failed", got.Status)
	}
	if !strings.Contains(err.Error(), "decompose_repair") {
		t.Fatalf("error %q must mention the decompose_repair loop", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.stepCalls["decompose"] != 4 {
		t.Fatalf("decompose ran %d times; want 4 before loop exhaustion", runner.stepCalls["decompose"])
	}
	if runner.stepCalls["implement"] != 0 {
		t.Fatalf("implement ran %d times; want 0", runner.stepCalls["implement"])
	}
}

// TestStackingDecomposeNoBugWithEmptyPlanSettlesClean pins the legitimate
// clean-audit path: a plan that genuinely declares zero steps keeps the
// no_bug verdict (decompose -> success, no implement).
func TestStackingDecomposeNoBugWithEmptyPlanSettlesClean(t *testing.T) {
	wf := stackingFixture(t)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"plan#1":      json.RawMessage(`{"summary":"nothing to do","steps":[],"inspected":["x"],"addressed_findings":[]}`),
		"decompose#1": json.RawMessage(decomposeNoBugJSON),
	}}
	ctrl, err := newStackingController(t, runner, wf, map[string]any{"task": "build"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v; want clean success", got, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.stepCalls["decompose"] != 1 {
		t.Fatalf("decompose ran %d times; want 1", runner.stepCalls["decompose"])
	}
	if runner.stepCalls["implement"] != 0 {
		t.Fatalf("implement ran %d times; want 0", runner.stepCalls["implement"])
	}
}
