package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/verifier"
)

// A verifier that could not RUN is a missing verdict, not a verdict of fail.
// The sandbox did not start, the binary was absent, the check was killed. None
// of that says anything about the change, so a workflow that names a repair
// step must reach it instead of losing every finished step.
//
// This drives ctrl.Run, the real call site, so the whole path executes:
// advanceEvidenceGate -> routeEvidenceFailure -> settleHostFailure -> route.
func TestHostFailureReachesTheDeclaredRepairStep(t *testing.T) {
	ctrl, runner, repo := newHostFailureFixture(t, "repair", 4)
	_, _ = ctrl.Run(context.Background())
	if len(runner.calls) == 0 {
		t.Fatal("the repair step never ran; the host failure did not reach it")
	}
	// The gate must still be recorded as failed. A gate that did not verify
	// must never be recorded as a pass.
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var sawFailedGate bool
	for _, a := range attempts {
		if a.StepID == "verify" && a.Status == workflowledger.AttemptStatusFailed {
			sawFailedGate = true
			if a.ErrorRef == "" {
				t.Fatal("the host failure carries no cause; a repair agent would have no evidence")
			}
			if a.ToStepID != "repair" {
				t.Fatalf("gate routed to %q, want the declared repair step", a.ToStepID)
			}
		}
	}
	if !sawFailedGate {
		t.Fatal("no failed gate attempt recorded")
	}
}

// A host that stays broken must not repair forever. enforceGlobalAttemptCap is
// a no-op when a workflow leaves max_step_attempts unset, and checkLoopCap
// never fires for this route, so this bound is the only one.
func TestHostFailureRepairIsBounded(t *testing.T) {
	// The step cap is set far above the repair budget, so the bound under test
	// is the host-failure budget and not the global cap. (A workflow with NO
	// limits at all cannot be admitted: the compiler refuses an unbounded
	// cycle. The budget still matters for an on_failure escape that forms no
	// declared cycle, and for a resume, which skips that admission check.)
	ctrl, _, repo := newHostFailureFixture(t, "repair", 40)
	// The repair returns to the gate, which fails on the host again.
	got, _ := ctrl.Run(context.Background())
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %q, want failed once the repair budget is spent", got.Status)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	gate := 0
	for _, a := range attempts {
		if a.StepID == "verify" {
			gate++
		}
	}
	if gate > maxHostFailureRepairs+1 {
		t.Fatalf("gate ran %d times, want it bounded near %d", gate, maxHostFailureRepairs)
	}
}

// A step that names no repair target keeps the old behaviour exactly: the run
// fails, and the host cause reaches the caller.
func TestHostFailureWithoutARepairTargetStillFailsTheRun(t *testing.T) {
	ctrl, runner, _ := newHostFailureFixture(t, "", 4)
	got, err := ctrl.Run(context.Background())
	if err == nil {
		t.Fatal("run error = nil, want the host cause to reach the caller")
	}
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %q, want failed", got.Status)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("no repair target was declared, yet a step ran: %+v", runner.calls)
	}
}

func newHostFailureFixture(t *testing.T, onFailure string, maxAttempts int) (*LinearController, *scriptedRunner, workflowledger.Repository) {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "evidence-host-repair", InitialStep: "verify",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: maxAttempts},
		Steps: []definition.Step{
			{ID: "verify", Kind: "evidence_gate", Verifier: "host-failure", OnFailure: onFailure},
			{ID: "repair", Kind: "agent", Agent: "dev"},
		},
		Transitions: []definition.Transition{
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "verify", To: "repair", Match: definition.MatchCriteria{Status: "failed"}},
			{From: "repair", To: "verify", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	cat := verifier.NewCatalogue()
	if err := cat.Register(fixedVerifierProfile{name: "host-failure", result: verifier.Result{
		Status: "failed",
		Checks: []verifier.Check{{Name: "sandbox", Status: "failed", Class: "host", Detail: "sandbox unavailable"}},
	}}); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{}}
	for i := 1; i <= 8; i++ {
		runner.outputsByStepCall[repairCallKey(i)] = json.RawMessage(`{"summary":"repaired"}`)
	}
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ctrl, err := NewLinearController(repo, runner, compiled, map[string]StepRuntime{
		"repair": {Agent: agents.ResolvedAgent{Name: "dev"}},
	}, map[string]any{"task": "x"}, "wfr-host-repair", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	return ctrl, runner, repo
}

func repairCallKey(n int) string {
	return "repair#" + string(rune('0'+n))
}
