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
			// The cause must reach the error ref, not just the report: the step
			// error is what the run summary and the caller surface, so it has
			// to say WHY the verifier could not run (DC-9).
			body, err := repo.LoadContent(context.Background(), a.ErrorRef)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), "sandbox unavailable") {
				t.Fatalf("host failure cause = %q, want the verifier's host detail", body)
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
	if gate > defaultMaxOnFailureReentries+1 {
		t.Fatalf("gate ran %d times, want it bounded near %d", gate, defaultMaxOnFailureReentries)
	}
}

// A run that exhausts the host-failure re-entry budget must record the
// TERMINAL failure route on the budget-exhausting attempt — never the
// un-honored repair target. settleAgentAttempt already rewrites the route
// before persisting (linear_execution.go); settleHostFailure must do the
// same, or the Failed run claims an active repair step that never ran (DC-9)
// and a crash between the attempt-complete write and the run-fail CAS resumes
// into a gate past its spent budget (DC-4).
func TestHostFailureBudgetExhaustionRecordsTerminalRoute(t *testing.T) {
	ctrl, _, repo := newHostFailureFixture(t, "repair", 40)
	got, _ := ctrl.Run(context.Background())
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %q, want failed once the repair budget is spent", got.Status)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	gateRuns, repairRuns := 0, 0
	var lastFailedGate workflowledger.StepAttempt
	for _, a := range attempts {
		switch a.StepID {
		case "verify":
			gateRuns++
			if a.Status == workflowledger.AttemptStatusFailed && a.AttemptNo > lastFailedGate.AttemptNo {
				lastFailedGate = a
			}
		case "repair":
			repairRuns++
		}
	}
	// verify#1 fails -> repair#1; verify#2 fails -> repair#2; verify#3 fails
	// with the budget spent -> run fails.
	if gateRuns != 3 || repairRuns != 2 {
		t.Fatalf("verify ran %d times (want 3), repair ran %d times (want 2): %+v", gateRuns, repairRuns, attempts)
	}
	if lastFailedGate.Status != workflowledger.AttemptStatusFailed || lastFailedGate.ToStepID != "failure" {
		t.Fatalf("last failed verify attempt = %+v; want failed routed to the terminal failure once the budget is spent, not the un-honored repair target", lastFailedGate)
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

// TestJoinInFlightEvidenceGateAttemptLeavesItForAdvance: resume must not
// hard-fail on an in-flight EVIDENCE GATE attempt, which has no agent runtime
// to join. JoinInFlightAttempt leaves it in-flight; Advance's admitAttempt
// marks the stale attempt interrupted and admits a fresh attempt that re-runs
// the gate.
// Regression: resume aborted with "step %q has no snapshotted runtime", which
// parked any run that died mid-gate forever (the CLI resume join could not
// finish, so the run never reached Advance's reconciliation).
func TestJoinInFlightEvidenceGateAttemptLeavesItForAdvance(t *testing.T) {
	ctx := context.Background()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "evidence-join", InitialStep: "verify",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Steps: []definition.Step{
			{ID: "verify", Kind: "evidence_gate", Verifier: "always-passes", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "verify", To: "failure", Match: definition.MatchCriteria{Status: "failed"}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	cat := verifier.NewCatalogue()
	if err := cat.Register(fixedVerifierProfile{name: "always-passes", result: verifier.Result{
		Status: "passed", Checks: []verifier.Check{{Name: "check", Status: "passed"}},
	}}); err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{}}, compiled, map[string]StepRuntime{}, map[string]any{"task": "x"}, "wfr-gate-join", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// Seed the crash artifact: a RUNNING gate attempt with no coordinator child.
	attempt := workflowledger.StepAttempt{AttemptID: "wfa-verify-1", RunID: ctrl.RunID, StepID: "verify", AttemptNo: 1, Status: workflowledger.AttemptStatusRunning}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	// Regression: this used to error "step \"verify\" has no snapshotted runtime".
	if err := ctrl.JoinInFlightAttempt(ctx, attempt); err != nil {
		t.Fatalf("JoinInFlightAttempt() error = %v, want nil (leave the gate attempt in-flight)", err)
	}
	got, done, err := ctrl.Advance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !done || got.ActiveStepID != "success" {
		t.Fatalf("advance = %+v, done=%t; want the gate re-run routed to success", got, done)
	}
	attempts, err := repo.ListStepAttempts(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var stale, fresh bool
	for _, a := range attempts {
		if a.StepID == "verify" && a.AttemptNo == 1 && a.Status == workflowledger.AttemptStatusInterrupted {
			stale = true
		}
		if a.StepID == "verify" && a.AttemptNo == 2 && a.Status == workflowledger.AttemptStatusSucceeded {
			fresh = true
		}
	}
	if !stale || !fresh {
		t.Fatalf("attempts = %+v, want attempt 1 interrupted and attempt 2 succeeded", attempts)
	}
}
