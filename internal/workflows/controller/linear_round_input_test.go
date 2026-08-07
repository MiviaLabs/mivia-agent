package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestRoundInputEqualsLoopCounter pins convergence plan v3 part 2 end to end:
// agentStepRequest injects a synthetic inputs.round equal to the step's loop
// iteration counter read from the ledger. The review step is in the
// review_repair loop: its first call runs before any completed iteration
// (round 0), its second call after one completed back-edge (round 1). A step
// outside a loop gets no round input.
func TestRoundInputEqualsLoopCounter(t *testing.T) {
	wf := repairWorkflow(t, -1, 12)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"v1"}`),
		"review#1":    json.RawMessage(`{"verdict":"changes_requested","findings":[]}`),
		"implement#2": json.RawMessage(`{"summary":"v2"}`),
		"review#2":    json.RawMessage(`{"verdict":"approved","findings":[]}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-round-input", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	counters, err := repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(counters) != 1 || counters[0].LoopName != "review_repair" || counters[0].Iterations != 1 {
		t.Fatalf("loop counters = %+v, want review_repair=1", counters)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	var reviewRounds []int
	for _, call := range runner.calls {
		switch call.StepID {
		case "implement":
			if _, present := call.Inputs["round"]; present {
				t.Fatalf("implement (outside a loop) received a round input: %+v", call.Inputs)
			}
		case "review":
			round, present := call.Inputs["round"]
			if !present {
				t.Fatalf("review call missing inputs.round: %+v", call.Inputs)
			}
			n, ok := round.(int)
			if !ok {
				t.Fatalf("review round = %#v (%T), want int", round, round)
			}
			reviewRounds = append(reviewRounds, n)
		}
	}
	if len(reviewRounds) != 2 || reviewRounds[0] != 0 || reviewRounds[1] != 1 {
		t.Fatalf("review rounds = %v, want [0 1] (round equals the loop counter)", reviewRounds)
	}
}
