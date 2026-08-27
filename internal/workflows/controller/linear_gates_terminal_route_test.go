package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// evidenceGateNonTerminalRouteWorkflow builds the evidence-gate fixture: the
// gate's OnFailure names a NON-TERMINAL agent step ("implement"), so when the
// succeeded transition zero-matches at runtime, selectRoute's error branch
// returns failureTarget(step) = "implement" — the crash-resume hazard the fix
// forces to the "failure" terminal.
func evidenceGateNonTerminalRouteWorkflow(t *testing.T) *definition.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "evidence-nonterminal-route", InitialStep: "verify",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 4},
		Steps: []definition.Step{
			{ID: "verify", Kind: "evidence_gate", Verifier: "always-pass", OnFailure: "implement"},
			{ID: "implement", Kind: "agent", Agent: "dev", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			// Keeps "implement" reachable for the compiler; never fires at
			// runtime (the succeeded path below is what runs).
			{From: "verify", To: "implement", Match: definition.MatchCriteria{Status: "failed"}},
			// The ONLY succeeded transition is output-gated on status
			// "passed"; the always-pass profile reports its pass as
			// "succeeded" (any non-"failed" verifier status is a pass), so the
			// runtime output map carries "status":"succeeded" and this gate
			// deterministically zero-matches -> selectRoute returns
			// failureTarget(step) ("implement") plus the error.
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}}},
			{From: "implement", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := definition.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// TestEvidenceGateSucceededZeroMatchPersistsTerminalRoute pins
// wf-evidence-gate-nonterminal-route-crash-resume: when an evidence gate's
// succeeded transition zero-matches at runtime, selectRoute's error branch
// returns failureTarget(step) — the gate's on_failure target. When that
// target is a NON-TERMINAL step ("implement"), the refused attempt must
// persist a TERMINAL route ("failure") — never the un-honored on_failure
// target that a crash between the attempt persist and the run-fail CAS could
// resume into and execute. Before the fix, advanceEvidenceGate's success-path
// error branch persisted ToStepID "implement".
func TestEvidenceGateSucceededZeroMatchPersistsTerminalRoute(t *testing.T) {
	ctx := context.Background()
	wf := evidenceGateNonTerminalRouteWorkflow(t)
	runner := &scriptedRunner{}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
	}, map[string]any{"task": "x"}, "wfr-evidence-nonterminal-route", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	cat := definition.NewCatalogue()
	if err := cat.Register(fixedVerifierProfile{
		name:   "always-pass",
		result: definition.Result{Status: "succeeded", Checks: []definition.Check{{Name: "test", Status: "passed"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(ctx)
	if err == nil {
		t.Fatalf("run succeeded = %+v; want selectRoute failure", got)
	}
	if !strings.Contains(err.Error(), "no matching transition") {
		t.Fatalf("err = %v; want transition match failure", err)
	}
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %q, want failed", got.Status)
	}
	// Derived active step: with a terminal durable route the ledger projects
	// "failure"; before the fix it projected the non-terminal "implement".
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ActiveStepID != "failure" {
		t.Fatalf("derived run.ActiveStepID = %q, want \"failure\" (not the non-terminal on_failure target \"implement\")", run.ActiveStepID)
	}
	attempts, err := repo.ListStepAttempts(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var verify workflowledger.StepAttempt
	for _, a := range attempts {
		if a.StepID == "verify" && (verify.AttemptID == "" || a.AttemptNo > verify.AttemptNo) {
			verify = a
		}
	}
	if verify.AttemptID == "" {
		t.Fatal("verify attempt missing")
	}
	if verify.Status != workflowledger.AttemptStatusFailed {
		t.Fatalf("verify attempt status = %q, want failed", verify.Status)
	}
	if verify.ToStepID != "failure" {
		t.Fatalf("verify attempt ToStepID = %q, want \"failure\" (not the non-terminal on_failure target \"implement\")", verify.ToStepID)
	}
	// NEGATIVE PATH: the refused gate must never dispatch the non-terminal
	// on_failure target.
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, call := range runner.calls {
		if call.StepID == "implement" {
			t.Fatalf("implement step dispatched after the failed gate; the refused attempt must never route to the non-terminal on_failure target")
		}
	}
}

// humanApproveNonTerminalRouteWorkflow builds the human-gate fixture: the
// gate's OnFailure names a NON-TERMINAL agent step ("implement"), so when the
// approved succeeded transition zero-matches at runtime, selectRoute's error
// branch returns failureTarget(step) = "implement" — the crash-resume hazard
// the fix forces to the "failure" terminal.
func humanApproveNonTerminalRouteWorkflow(t *testing.T) *definition.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "human-approve-nonterminal-route", InitialStep: "approve_me",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 4},
		Steps: []definition.Step{
			{ID: "approve_me", Kind: "human_gate", OnFailure: "implement"},
			{ID: "implement", Kind: "agent", Agent: "dev", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			// Keeps "implement" reachable for the compiler; never fires at
			// runtime (the approved succeeded path below is what runs).
			{From: "approve_me", To: "implement", Match: definition.MatchCriteria{Status: "failed"}},
			// The ONLY succeeded transition is output-gated on verdict
			// "pass", which the approval output {"decision":"approved"} never
			// contains -> zero-match -> selectRoute returns
			// failureTarget(step) ("implement") plus the error.
			{From: "approve_me", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "pass"}}},
			{From: "implement", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := definition.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// TestHumanGateApproveZeroMatchPersistsTerminalRoute pins
// wf-human-approve-nonterminal-route-crash-resume: when an approved human
// gate's succeeded transition zero-matches at runtime, selectRoute's error
// branch returns failureTarget(step) — the gate's on_failure target. When
// that target is a NON-TERMINAL step ("implement"), the refused approval
// attempt must persist a TERMINAL route ("failure") — never the un-honored
// on_failure target that a crash between the attempt persist and the run-fail
// CAS could resume into and execute. Before the fix,
// finishHumanResolutionForAttempt's approval error branch persisted ToStepID
// "implement".
func TestHumanGateApproveZeroMatchPersistsTerminalRoute(t *testing.T) {
	ctx := context.Background()
	wf := humanApproveNonTerminalRouteWorkflow(t)
	runner := &scriptedRunner{}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
	}, map[string]any{"task": "x"}, "wfr-human-approve-nonterminal-route", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("run status = %q, want waiting_approval", got.Status)
	}
	err = ctrl.Approve(ctx, PendingApprovalID("approve_me", 1), "operator")
	if err == nil {
		t.Fatal("Approve succeeded; want selectRoute failure")
	}
	if !strings.Contains(err.Error(), "no matching transition") {
		t.Fatalf("Approve err = %v; want transition match failure", err)
	}
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
	// Derived active step: with a terminal durable route the ledger projects
	// "failure"; before the fix it projected the non-terminal "implement".
	if run.ActiveStepID != "failure" {
		t.Fatalf("derived run.ActiveStepID = %q, want \"failure\" (not the non-terminal on_failure target \"implement\")", run.ActiveStepID)
	}
	attempts, err := repo.ListStepAttempts(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var approval workflowledger.StepAttempt
	for _, a := range attempts {
		if a.StepID == "approve_me" {
			approval = a
		}
	}
	if approval.AttemptID == "" {
		t.Fatal("approve_me attempt missing")
	}
	if approval.Status != workflowledger.AttemptStatusFailed {
		t.Fatalf("approve_me attempt status = %q, want failed", approval.Status)
	}
	if approval.ToStepID != "failure" {
		t.Fatalf("approve_me attempt ToStepID = %q, want \"failure\" (not the non-terminal on_failure target \"implement\")", approval.ToStepID)
	}
	// NEGATIVE PATH: the refused approval must never dispatch the
	// non-terminal on_failure target.
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, call := range runner.calls {
		if call.StepID == "implement" {
			t.Fatalf("implement step dispatched after the failed approval; the refused attempt must never route to the non-terminal on_failure target")
		}
	}
}
