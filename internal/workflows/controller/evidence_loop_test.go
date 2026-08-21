package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestEvidenceGateRepairLoopRespectsMaxIterations verifies that an
// evidence_gate repair loop with a finite max_iterations terminates after the
// declared cap. Before the fix, routeEvidenceFailure never incremented the
// loop counter, so checkLoopCap always read zero and the loop ran unbounded.
func TestEvidenceGateRepairLoopRespectsMaxIterations(t *testing.T) {
	repo, ctrl := newEvidenceLoopController(t, "evidence-loop", "wfr-evidence-loop-cap", "failure")
	got, err := ctrl.Run(context.Background())
	if err == nil {
		t.Fatalf("run succeeded = %+v; want loop-exhausted failure", got)
	}
	if !strings.Contains(err.Error(), "loop \"repair\" exhausted") {
		t.Fatalf("error = %v; want loop exhausted", err)
	}
	// The failure must carry the structured recovery hint (R2 Phase 1).
	assertLoopExhaustionHint(t, err)
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

// TestEvidenceGateLoopExhaustedPersistsTerminalRoute pins wf-evidence-loop-
// exhausted-nonterminal-route: when an evidence_gate repair loop spends its
// budget AND the gate's on_failure names a NON-TERMINAL step ("implement"),
// the refused attempt must persist a TERMINAL route ("failure") — never the
// un-honored failureTarget(step) that a crash between the attempt persist and
// the run-fail CAS could resume into past a spent loop budget. Before the fix,
// routeEvidenceFailure's error branch persisted ToStepID "implement".
func TestEvidenceGateLoopExhaustedPersistsTerminalRoute(t *testing.T) {
	repo, ctrl := newEvidenceLoopController(t, "evidence-loop-nonterminal", "wfr-evidence-loop-nonterminal", "implement")
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
	// SUCCESS PATH: the repair loop budget is honored — the counter reaches
	// the cap and attempts stop at two verified runs plus two implementations.
	assertEvidenceLoopBudgetSpent(t, repo, ctrl.RunID)
	// NEGATIVE PATH: the refused attempt is durably Failed and routes to the
	// TERMINAL step "failure", never the spent loop's non-terminal on_failure
	// target "implement" (a crash between persist and the run-fail CAS must
	// not resume into the un-honored failureTarget).
	assertEvidenceLoopTerminalRoute(t, repo, ctrl.RunID)
}

// newEvidenceLoopController builds the loop-exhaustion fixture for an
// evidence_gate repair loop capped at 2 iterations. onFailure names the gate's
// on_failure step ("failure" for the terminal test, "implement" for the
// non-terminal route test). The verifier always fails and the repair step
// always reports success, so the loop spends its budget deterministically.
func newEvidenceLoopController(t *testing.T, name, runID, onFailure string) (*workflowledger.StorageRepository, *LinearController) {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: name, InitialStep: "verify",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		// No global limit: the per-loop cap must be the sole bound.
		Limits: definition.Limits{MaxStepAttempts: 0},
		Steps: []definition.Step{
			// OnFailure names the step the refused route must NOT take when the
			// loop budget is spent (a crash between persist and the run-fail CAS
			// must not resume into the un-honored failureTarget).
			{ID: "verify", Kind: "evidence_gate", Verifier: "always-fails", OnFailure: onFailure},
			{ID: "implement", Kind: "agent", Agent: "dev", OnFailure: "failure",
				Context: []definition.ContextBinding{{From: "steps.verify.output", As: "failed_evidence", Optional: true}}},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "verify", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}}},
			{From: "verify", To: "implement", Match: definition.MatchCriteria{Status: "failed"}, Loop: "repair", MaxIterations: 2},
		},
	}
	compiled, err := definition.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	cat := definition.NewCatalogue()
	if err := cat.Register(fixedVerifierProfile{
		name:   "always-fails",
		result: definition.Result{Status: "failed", Checks: []definition.Check{{Name: "test", Status: "failed", Class: "source"}}},
	}); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#*": verifiedJSON(`{"summary":"repair"}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, compiled, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
	}, map[string]any{"task": "x"}, runID, []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	return repo, ctrl
}

// assertEvidenceLoopBudgetSpent checks the success path: the "repair" loop
// counter reached the cap (2) and the attempt counts stopped at three verify
// attempts (two allowed + one refused) and two implement attempts.
func assertEvidenceLoopBudgetSpent(t *testing.T, repo *workflowledger.StorageRepository, runID string) {
	t.Helper()
	counters, err := repo.GetLoopCounters(context.Background(), runID)
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
	attempts, err := repo.ListStepAttempts(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
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

// assertEvidenceLoopTerminalRoute checks the negative path: the refused (last)
// verify attempt is durably Failed and its route is the TERMINAL step
// "failure", never the un-honored non-terminal on_failure target "implement".
func assertEvidenceLoopTerminalRoute(t *testing.T, repo *workflowledger.StorageRepository, runID string) {
	t.Helper()
	attempts, err := repo.ListStepAttempts(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	var lastVerify workflowledger.StepAttempt
	for _, a := range attempts {
		if a.StepID == "verify" && (lastVerify.AttemptID == "" || a.AttemptNo > lastVerify.AttemptNo) {
			lastVerify = a
		}
	}
	if lastVerify.AttemptID == "" {
		t.Fatal("no verify attempt recorded")
	}
	if lastVerify.Status != workflowledger.AttemptStatusFailed {
		t.Fatalf("last verify attempt status = %q, want failed", lastVerify.Status)
	}
	if lastVerify.ToStepID != "failure" {
		t.Fatalf("last verify attempt ToStepID = %q, want \"failure\" (not the non-terminal on_failure target \"implement\")", lastVerify.ToStepID)
	}
}

func verifiedJSON(s string) json.RawMessage { return json.RawMessage(s) }

// assertLoopExhaustionHint checks the structured recovery hint carried by a
// loop-exhausted failure: loop name, cap, spent iterations, and the step whose
// route was refused (R2 Phase 1).
func assertLoopExhaustionHint(t *testing.T, err error) {
	t.Helper()
	var loopErr *loopExhaustedError
	if !errors.As(err, &loopErr) {
		t.Fatalf("error %v does not carry the structured loop-exhaustion hint", err)
	}
	if loopErr.LoopName != "repair" || loopErr.MaxIterations != 2 || loopErr.Iterations != 2 || loopErr.StepID != "verify" {
		t.Fatalf("loop hint = %+v, want loop=repair max=2 iterations=2 step=verify", loopErr)
	}
	if !strings.Contains(err.Error(), `(step "verify")`) {
		t.Fatalf("error = %v; want the refused step named in the recovery hint", err)
	}
	if len(loopErr.Salvage) == 0 {
		t.Fatalf("loop hint carries no salvaged outputs; want the verified implement output preserved (R2 Phase 2)")
	}
	salvagedImplement := false
	for _, s := range loopErr.Salvage {
		if s.StepID == "implement" && s.AttemptNo == 2 && (s.OutputRef != "" || s.OutputDigest != "") {
			salvagedImplement = true
		}
	}
	if !salvagedImplement {
		t.Fatalf("salvage = %+v, want the last implement attempt (#2) with an output ref", loopErr.Salvage)
	}
	if !strings.Contains(err.Error(), "(salvaged:") {
		t.Fatalf("error = %v; want the salvaged refs named in the recovery hint", err)
	}
}

// newEvidenceLoopPartialController builds the partial-accept fixture: the
// repair loop declares partial_target "deliver", which binds run.salvage and
// forwards to success. The verifier always fails and the repair step always
// reports success, so the loop spends its budget and the run must route to
// deliver (with the verified outputs salvaged) instead of failing.
func newEvidenceLoopPartialController(t *testing.T) (*workflowledger.StorageRepository, *LinearController, *scriptedRunner) {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "evidence-loop-partial", InitialStep: "verify",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		// No global limit: the per-loop cap must be the sole bound.
		Limits: definition.Limits{MaxStepAttempts: 0},
		Steps: []definition.Step{
			{ID: "verify", Kind: "evidence_gate", Verifier: "always-fails", OnFailure: "failure"},
			{ID: "implement", Kind: "agent", Agent: "dev", OnFailure: "failure",
				Context: []definition.ContextBinding{{From: "steps.verify.output", As: "failed_evidence", Optional: true}}},
			{ID: "deliver", Kind: "agent", Agent: "dev", OnFailure: "failure",
				Context: []definition.ContextBinding{{From: "run.salvage", As: "salvage", Optional: true}}},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "verify", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}}},
			{From: "verify", To: "implement", Match: definition.MatchCriteria{Status: "failed"}, Loop: "repair", MaxIterations: 2, PartialTarget: "deliver"},
			{From: "deliver", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := definition.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	cat := definition.NewCatalogue()
	if err := cat.Register(fixedVerifierProfile{
		name:   "always-fails",
		result: definition.Result{Status: "failed", Checks: []definition.Check{{Name: "test", Status: "failed", Class: "source"}}},
	}); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#*": verifiedJSON(`{"summary":"repair"}`),
		"deliver#*":   verifiedJSON(`{"summary":"delivered partial"}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, compiled, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"deliver":   {Agent: agents.ResolvedAgent{Name: "dev"}},
	}, map[string]any{"task": "x"}, "wfr-evidence-loop-partial", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	return repo, ctrl, runner
}

// TestEvidenceGateLoopPartialAcceptRoutesToDeclaredTarget pins R2 Phase 2: a
// loop whose transition declares partial_target routes to that step when its
// budget exhausts and verified outputs survive, instead of failing the run.
// The refused attempt is persisted with the partial route, the deliver step
// receives the salvaged refs as run.salvage evidence, and the run succeeds.
func TestEvidenceGateLoopPartialAcceptRoutesToDeclaredTarget(t *testing.T) {
	repo, ctrl, runner := newEvidenceLoopPartialController(t)
	got, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("run failed = %v; want partial-accept success", err)
	}
	if got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded (partial accept)", got.Status)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	verifyCount, implementCount, deliverCount := 0, 0, 0
	var refusedVerify workflowledger.StepAttempt
	for _, a := range attempts {
		switch a.StepID {
		case "verify":
			verifyCount++
			if a.Status == workflowledger.AttemptStatusFailed && a.AttemptNo > refusedVerify.AttemptNo {
				refusedVerify = a
			}
		case "implement":
			implementCount++
		case "deliver":
			deliverCount++
		}
	}
	if verifyCount != 3 || implementCount != 2 || deliverCount != 1 {
		t.Fatalf("verify=%d implement=%d deliver=%d; want 3/2/1 (loop spent, partial ran once)", verifyCount, implementCount, deliverCount)
	}
	if refusedVerify.ToStepID != "deliver" {
		t.Fatalf("refused verify attempt ToStepID = %q, want the declared partial_target %q", refusedVerify.ToStepID, "deliver")
	}
	deliverRan := false
	for _, req := range runner.calls {
		if req.StepID != "deliver" {
			continue
		}
		deliverRan = true
		salvage, ok := req.Evidence["salvage"].(string)
		if !ok {
			t.Fatal("deliver step received no run.salvage evidence")
		}
		if !strings.Contains(salvage, `"step_id":"implement"`) {
			t.Fatalf("salvage evidence = %s; want the implement ref preserved", salvage)
		}
	}
	if !deliverRan {
		t.Fatal("deliver step never ran")
	}
}
