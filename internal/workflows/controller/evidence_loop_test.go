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
	"github.com/MiviaLabs/mivia-agent/internal/workflows/verifier"
)

// TestEvidenceGateRepairLoopRespectsMaxIterations verifies that an
// evidence_gate repair loop with a finite max_iterations terminates after the
// declared cap. Before the fix, routeEvidenceFailure never incremented the
// loop counter, so checkLoopCap always read zero and the loop ran unbounded.
func TestEvidenceGateRepairLoopRespectsMaxIterations(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "evidence-loop", InitialStep: "verify",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		// No global limit: the per-loop cap must be the sole bound.
		Limits: definition.Limits{MaxStepAttempts: 0},
		Steps: []definition.Step{
			{ID: "verify", Kind: "evidence_gate", Verifier: "always-fails", OnFailure: "failure"},
			{ID: "implement", Kind: "agent", Agent: "dev", OnFailure: "failure",
				Context: []definition.ContextBinding{{From: "steps.verify.output", As: "failed_evidence", Optional: true}}},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "verify", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}}},
			{From: "verify", To: "implement", Match: definition.MatchCriteria{Status: "failed"}, Loop: "repair", MaxIterations: 2},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	cat := verifier.NewCatalogue()
	if err := cat.Register(fixedVerifierProfile{
		name:   "always-fails",
		result: verifier.Result{Status: "failed", Checks: []verifier.Check{{Name: "test", Status: "failed", Class: "source"}}},
	}); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#*": verifiedJSON(`{"summary":"repair"}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, compiled, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
	}, map[string]any{"task": "x"}, "wfr-evidence-loop-cap", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil {
		t.Fatalf("run succeeded = %+v; want loop-exhausted failure", got)
	}
	if !strings.Contains(err.Error(), "loop \"repair\" exhausted") {
		t.Fatalf("error = %v; want loop exhausted", err)
	}
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	// The loop must have incremented exactly to the cap (2).
	counters, err := repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var repairCount int
	for _, lc := range counters {
		if lc.LoopName == "repair" {
			repairCount = lc.Iterations
		}
	}
	if repairCount != 2 {
		t.Fatalf("repair loop counter = %d, want 2", repairCount)
	}
	// Three verify attempts (two allowed + one refused) and two implement attempts.
	attempts, _ := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	verifyCount, implementCount := 0, 0
	for _, a := range attempts {
		switch a.StepID {
		case "verify":
			verifyCount++
		case "implement":
			implementCount++
		}
	}
	if verifyCount != 3 || implementCount != 2 {
		t.Fatalf("verify=%d implement=%d; want 3 verify, 2 implement", verifyCount, implementCount)
	}
}

func verifiedJSON(s string) json.RawMessage { return json.RawMessage(s) }
