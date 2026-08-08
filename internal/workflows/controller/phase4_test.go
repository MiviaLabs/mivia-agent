package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/verifier"
)

// scriptedRunner returns pre-scripted outputs per step call order.
type scriptedRunner struct {
	mu    sync.Mutex
	calls []AgentStepRequest
	// outputsByStepCall maps "stepID#n" (1-based call count for that step) to output.
	outputsByStepCall map[string]json.RawMessage
	// stepCalls tracks call count per step.
	stepCalls map[string]int
	// failOn maps "stepID#n" to an error.
	failOn map[string]error
}

func (r *scriptedRunner) RunStep(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, req)
	if r.stepCalls == nil {
		r.stepCalls = make(map[string]int)
	}
	r.stepCalls[req.StepID]++
	key := fmt.Sprintf("%s#%d", req.StepID, r.stepCalls[req.StepID])
	if r.failOn != nil {
		if err, ok := r.failOn[key]; ok {
			return AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID}, err
		}
	}
	out := r.outputsByStepCall[key]
	if out == nil {
		out = r.outputsByStepCall[req.StepID+"#*"]
	}
	var validated any
	if len(out) > 0 {
		_ = json.Unmarshal(out, &validated)
	}
	return AgentStepResult{
		CoordinatorRunID: req.CoordinatorRunID,
		TaskID:           req.TaskID,
		Output:           out,
		ValidatedOutput:  validated,
		EvidenceJSON:     []byte(`[]`),
	}, nil
}

func repairWorkflow(t *testing.T, maxLoop int, maxAttempts int) *compiler.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "repair", InitialStep: "implement",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: maxAttempts},
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "dev", OnFailure: "failure",
				Context: []definition.ContextBinding{{From: "inputs.task", As: "task"}}},
			{ID: "review", Kind: "agent_gate", Agent: "rev", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
			{From: "review", To: "implement", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "changes_requested"}}, Loop: "review_repair", MaxIterations: maxLoop},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func TestAgentGateRoutesOnSchemaFieldsNotProse(t *testing.T) {
	wf := repairWorkflow(t, -1, 8)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"v1"}`),
		// Free-form prose in findings must not affect routing; only verdict does.
		"review#1": json.RawMessage(`{"verdict":"approved","findings":[{"severity":"low","reason":"looks good please approve"}]}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-agent-gate", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	attempts, _ := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	var review workflowledger.StepAttempt
	for _, a := range attempts {
		if a.StepID == "review" {
			review = a
		}
	}
	if review.MatchDigest == "" || len(review.DecisionJSON) == 0 || review.ToStepID != "success" {
		t.Fatalf("review route = %+v", review)
	}
	if !strings.Contains(string(review.DecisionJSON), "approved") {
		t.Fatalf("decision json missing selected verdict: %s", review.DecisionJSON)
	}
}

func TestEvidenceGateGoDefaultAndUnknownFailsClosed(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "ev", InitialStep: "verify",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 4},
		Steps: []definition.Step{
			{ID: "verify", Kind: "evidence_gate", Verifier: "go-default", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewMemoryRepository()
	cat := verifier.NewCatalogue()
	if err := cat.Register(verifier.NewGoDefault(func(context.Context, string) ([]verifier.Check, error) {
		return []verifier.Check{{Name: "workspace-dir", Status: "passed"}, {Name: "go-module", Status: "passed"}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	ctrl, err := NewLinearController(repo, &linearRunner{}, compiled, nil, map[string]any{"task": "x"}, "wfr-evidence", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	attempts, _ := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if len(attempts) != 1 || attempts[0].OutputRef == "" || attempts[0].ToStepID != "success" {
		t.Fatalf("attempts = %+v", attempts)
	}
	raw, err := repo.LoadContent(context.Background(), attempts[0].OutputRef)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "passed" {
		t.Fatalf("evidence body = %s", raw)
	}

	// Unknown verifier fails closed without dispatch.
	wf2 := *wf
	wf2.Steps = []definition.Step{{ID: "verify", Kind: "evidence_gate", Verifier: "not-registered", OnFailure: "failure"}}
	compiled2, err := compiler.Compile(&wf2)
	if err != nil {
		t.Fatal(err)
	}
	repo2 := workflowledger.NewMemoryRepository()
	ctrl2, err := NewLinearController(repo2, &linearRunner{}, compiled2, nil, map[string]any{"task": "x"}, "wfr-unknown-v", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl2.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	got2, err := ctrl2.Run(context.Background())
	if err == nil || got2.Status != workflowledger.RunStatusFailed {
		t.Fatalf("unknown verifier: got=%+v err=%v", got2, err)
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("err = %v", err)
	}
}

type fixedVerifierProfile struct {
	name   string
	result verifier.Result
	err    error
}

func (p fixedVerifierProfile) Name() string { return p.name }

func (p fixedVerifierProfile) Verify(context.Context, verifier.Request) (verifier.Result, error) {
	return p.result, p.err
}

func TestEvidenceGateReadmitsFailedAttemptAfterRepairRoute(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	ctrl := &LinearController{Repo: repo, RunID: "wfr-readmit", Workflow: &compiler.CompiledWorkflow{}}
	if err := repo.CreateRun(context.Background(), workflowledger.RunSnapshot{RunID: ctrl.RunID, WorkflowName: "test", WorkflowDigest: "digest", ActiveStepID: "verify", Status: workflowledger.RunStatusPending}, []byte("snapshot")); err != nil {
		t.Fatal(err)
	}
	prior := workflowledger.StepAttempt{AttemptID: "wfa-verify-1", RunID: ctrl.RunID, StepID: "verify", AttemptNo: 1, Status: workflowledger.AttemptStatusFailed, ToStepID: "repair"}
	if err := repo.CreateStepAttempt(context.Background(), prior); err != nil {
		t.Fatal(err)
	}
	attempt, ok, err := ctrl.admitAttempt(context.Background(), workflowledger.RunSnapshot{}, "verify", []workflowledger.StepAttempt{prior})
	if err != nil || !ok || attempt.AttemptNo != 2 {
		t.Fatalf("admitAttempt() = %+v, %t, %v; want attempt 2", attempt, ok, err)
	}
}

func TestEvidenceGateFailureRoutesToRepairWithPersistedResult(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "evidence-repair", InitialStep: "verify",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 4},
		Steps: []definition.Step{
			{ID: "verify", Kind: "evidence_gate", Verifier: "failing-check", OnFailure: "failure"},
			{ID: "repair", Kind: "agent", Agent: "dev", Context: []definition.ContextBinding{{From: "steps.verify.output", As: "verification"}}},
		},
		Transitions: []definition.Transition{
			{From: "verify", To: "repair", Match: definition.MatchCriteria{Status: "failed"}},
			{From: "repair", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	cat := verifier.NewCatalogue()
	if err := cat.Register(fixedVerifierProfile{
		name:   "failing-check",
		result: verifier.Result{Status: "failed", Checks: []verifier.Check{{Name: "go test ./...", Status: "failed"}}},
	}); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"repair#1": json.RawMessage(`{"summary":"fixed"}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, compiled, map[string]StepRuntime{
		"repair": {Agent: agents.ResolvedAgent{Name: "dev"}},
	}, map[string]any{"task": "x"}, "wfr-evidence-repair", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	verify, ok := latestAttempt(attempts, "verify")
	if !ok || verify.Status != workflowledger.AttemptStatusFailed || verify.ToStepID != "repair" || verify.OutputRef == "" {
		t.Fatalf("verify attempt = %+v", verify)
	}
	raw, err := repo.LoadContent(context.Background(), verify.OutputRef)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"go test ./..."`) || !strings.Contains(string(raw), `"failed"`) {
		t.Fatalf("failure evidence = %s", raw)
	}
	if len(runner.calls) != 1 || runner.calls[0].StepID != "repair" {
		t.Fatalf("repair calls = %+v", runner.calls)
	}
	if got := runner.calls[0].Evidence["verification"]; got == nil {
		t.Fatal("repair did not receive failure evidence")
	}
}

// TestEvidenceGateStructureFailureRoutesToRepairWithFailedDetail pins the
// preflight_structure tail of feature-delivery: a source-class structure
// violation must persist resolvable failed evidence (the Detail lives in the
// stored body, not an unresolvable digest) and reach the repair agent.
func TestEvidenceGateStructureFailureRoutesToRepairWithFailedDetail(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "structure-repair", InitialStep: "preflight_structure",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 4},
		Steps: []definition.Step{
			{ID: "preflight_structure", Kind: "evidence_gate", Verifier: "failing-structure", OnFailure: "failure"},
			{ID: "repair_preflight_structure", Kind: "agent", Agent: "dev", Context: []definition.ContextBinding{{From: "steps.preflight_structure.output", As: "failed_evidence", MaxBytes: 16000}}},
			{ID: "review", Kind: "agent_gate", Agent: "rev", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			{From: "preflight_structure", To: "repair_preflight_structure", Match: definition.MatchCriteria{Status: "failed"}},
			{From: "repair_preflight_structure", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	cat := verifier.NewCatalogue()
	if err := cat.Register(fixedVerifierProfile{name: "failing-structure", result: verifier.Result{Status: "failed", Checks: []verifier.Check{{Name: "go-structure", Status: "failed", Class: "source", Detail: "HARD function LOC: internal/ledger/close_run_atomicity_test.go L3-L125 (123 lines, hard max 120). Extract helpers."}}}}); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"repair_preflight_structure#1": json.RawMessage(`{"summary":"split the oversized function"}`),
		"review#1":                     json.RawMessage(`{"verdict":"approved"}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, compiled, map[string]StepRuntime{
		"repair_preflight_structure": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":                     {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-structure-repair", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	gate, ok := latestAttempt(attempts, "preflight_structure")
	if !ok || gate.Status != workflowledger.AttemptStatusFailed || gate.ToStepID != "repair_preflight_structure" || gate.OutputRef == "" {
		t.Fatalf("gate attempt = %+v", gate)
	}
	raw, err := repo.LoadContent(context.Background(), gate.OutputRef)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "HARD function LOC") || !strings.Contains(string(raw), "close_run_atomicity_test.go") {
		t.Fatalf("failed evidence body = %s", raw)
	}
	if len(runner.calls) != 2 || runner.calls[0].StepID != "repair_preflight_structure" {
		t.Fatalf("repair calls = %+v", runner.calls)
	}
	if evidence := runner.calls[0].Evidence["failed_evidence"]; evidence == nil || !strings.Contains(fmt.Sprint(evidence), "HARD function LOC") {
		t.Fatalf("failed_evidence = %v, want the HARD detail", evidence)
	}
}

func TestEvidenceGateHostFailureDoesNotRouteToRepair(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "evidence-host-failure", InitialStep: "verify",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 4},
		Steps: []definition.Step{
			{ID: "verify", Kind: "evidence_gate", Verifier: "host-failure", OnFailure: "failure"},
			{ID: "repair", Kind: "agent", Agent: "dev"},
		},
		Transitions: []definition.Transition{
			{From: "verify", To: "repair", Match: definition.MatchCriteria{Status: "failed"}},
			{From: "repair", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	cat := verifier.NewCatalogue()
	if err := cat.Register(fixedVerifierProfile{name: "host-failure", result: verifier.Result{Status: "failed", Checks: []verifier.Check{{Name: "sandbox", Status: "failed", Class: "host", Detail: "sandbox unavailable"}}}}); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{"repair#1": json.RawMessage(`{"summary":"must not run"}`)}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, compiled, map[string]StepRuntime{"repair": {Agent: agents.ResolvedAgent{Name: "dev"}}}, map[string]any{"task": "x"}, "wfr-evidence-host-failure", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("host failure routed to repair: %+v", runner.calls)
	}
}

func TestEvidenceGateFailureWithoutTransitionFailsClosed(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "evidence-no-repair", InitialStep: "verify",
		Inputs:      map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits:      definition.Limits{MaxStepAttempts: 4},
		Steps:       []definition.Step{{ID: "verify", Kind: "evidence_gate", Verifier: "failing-check", OnFailure: "failure"}},
		Transitions: []definition.Transition{{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}}},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	cat := verifier.NewCatalogue()
	if err := cat.Register(fixedVerifierProfile{name: "failing-check", result: verifier.Result{Status: "failed", Checks: []verifier.Check{{Name: "go test ./...", Status: "failed"}}}}); err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, compiled, nil, map[string]any{"task": "x"}, "wfr-evidence-no-repair", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	attempts, listErr := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	verify, ok := latestAttempt(attempts, "verify")
	if !ok || verify.Status != workflowledger.AttemptStatusFailed || verify.ToStepID != "failure" || verify.OutputRef == "" {
		t.Fatalf("verify attempt = %+v", verify)
	}
}

func TestHumanGateWaitingApprovalAndApproveReject(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "human", InitialStep: "approve_me",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 4},
		Steps: []definition.Step{
			{ID: "approve_me", Kind: "human_gate", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			{From: "approve_me", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"decision": "approved"}}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, compiled, nil, map[string]any{"task": "x"}, "wfr-human", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("status = %q, want waiting_approval", got.Status)
	}
	// Second advance stays paused.
	got, done, err := ctrl.Advance(context.Background())
	if err != nil || !done || got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("advance while waiting = %+v done=%v err=%v", got, done, err)
	}
	approvalID := PendingApprovalID("approve_me", 1)
	if err := ctrl.Approve(context.Background(), approvalID, "operator"); err != nil {
		t.Fatal(err)
	}
	got, err = ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("after approve = %+v err=%v", got, err)
	}

	// Reject path
	repoR := workflowledger.NewMemoryRepository()
	ctrlR, err := NewLinearController(repoR, &linearRunner{}, compiled, nil, map[string]any{"task": "x"}, "wfr-human-reject", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctrlR.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ctrlR.Reject(context.Background(), PendingApprovalID("approve_me", 1), "operator", "no"); err != nil {
		t.Fatal(err)
	}
	run, _ := repoR.GetRun(context.Background(), ctrlR.RunID)
	if run.Status != workflowledger.RunStatusFailed {
		t.Fatalf("reject status = %q", run.Status)
	}
}

func TestLoopCapStopsBeforeDispatch(t *testing.T) {
	// max_iterations=1 allows one back-edge, then exhausts.
	wf := repairWorkflow(t, 1, 20)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"v1"}`),
		"review#1":    json.RawMessage(`{"verdict":"changes_requested","findings":[]}`),
		"implement#2": json.RawMessage(`{"summary":"v2"}`),
		"review#2":    json.RawMessage(`{"verdict":"changes_requested","findings":[]}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-loop-cap", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	if !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("err = %v, want loop exhausted", err)
	}
	// First repair back-edge must have incremented the loop once; second should not dispatch implement#3.
	runner.mu.Lock()
	defer runner.mu.Unlock()
	implCalls := 0
	for _, c := range runner.calls {
		if c.StepID == "implement" {
			implCalls++
		}
	}
	if implCalls != 2 {
		t.Fatalf("implement calls = %d, want 2", implCalls)
	}
	counters, _ := repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if len(counters) != 1 || counters[0].Iterations != 1 {
		t.Fatalf("counters = %+v", counters)
	}
}

func TestUnboundedLoopStopsAtGlobalMaxStepAttempts(t *testing.T) {
	wf := repairWorkflow(t, -1, 4)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#*": json.RawMessage(`{"summary":"v"}`),
		"review#*":    json.RawMessage(`{"verdict":"changes_requested","findings":[]}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-global-cap", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	if !strings.Contains(err.Error(), "max_step_attempts") {
		t.Fatalf("err = %v", err)
	}
	attempts, _ := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if len(attempts) != 4 {
		t.Fatalf("attempts = %d, want 4", len(attempts))
	}
	// All prior attempts remain; nothing overwritten.
	seen := map[string]map[int]bool{}
	for _, a := range attempts {
		if seen[a.StepID] == nil {
			seen[a.StepID] = map[int]bool{}
		}
		if seen[a.StepID][a.AttemptNo] {
			t.Fatalf("duplicate attempt number for %s: %+v", a.StepID, attempts)
		}
		seen[a.StepID][a.AttemptNo] = true
	}
}

func TestRepairLoopHistoryImplement2Review2(t *testing.T) {
	wf := repairWorkflow(t, -1, 16)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"first"}`),
		"review#1":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"severity":"medium","reason":"missing tests"}]}`),
		"implement#2": json.RawMessage(`{"summary":"second"}`),
		"review#2":    json.RawMessage(`{"verdict":"approved","findings":[]}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "add retries"}, "wfr-repair-history", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	byStep := map[string][]workflowledger.StepAttempt{}
	for _, a := range attempts {
		byStep[a.StepID] = append(byStep[a.StepID], a)
	}
	if len(byStep["implement"]) != 2 || byStep["implement"][0].AttemptNo != 1 || byStep["implement"][1].AttemptNo != 2 {
		t.Fatalf("implement attempts = %+v", byStep["implement"])
	}
	if len(byStep["review"]) != 2 || byStep["review"][0].AttemptNo != 1 || byStep["review"][1].AttemptNo != 2 {
		t.Fatalf("review attempts = %+v", byStep["review"])
	}
	// Both attempts remain; history is append-only.
	if byStep["implement"][0].Status != workflowledger.AttemptStatusSucceeded || byStep["implement"][1].Status != workflowledger.AttemptStatusSucceeded {
		t.Fatalf("implement statuses = %+v", byStep["implement"])
	}
	if byStep["review"][0].ToStepID != "implement" || byStep["review"][1].ToStepID != "success" {
		t.Fatalf("review routes = %+v", byStep["review"])
	}
	if byStep["review"][0].MatchDigest == "" || byStep["review"][1].MatchDigest == "" {
		t.Fatal("missing match digests on review attempts")
	}
	counters, _ := repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if len(counters) != 1 || counters[0].LoopName != "review_repair" || counters[0].Iterations != 1 {
		t.Fatalf("loop counters = %+v", counters)
	}
}

func TestAgentFailureUsesOnFailureNeverRepairLoop(t *testing.T) {
	wf := repairWorkflow(t, -1, 8)
	runner := &scriptedRunner{
		outputsByStepCall: map[string]json.RawMessage{
			"implement#1": json.RawMessage(`{"summary":"v1"}`),
		},
		failOn: map[string]error{
			"review#1": errors.New("agent infrastructure boom"),
		},
	}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-on-failure", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	attempts, _ := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	// Only implement#1 and review#1 — no repair dispatch.
	if len(attempts) != 2 {
		t.Fatalf("attempts = %+v", attempts)
	}
	for _, a := range attempts {
		if a.StepID == "review" && a.ToStepID != "failure" {
			t.Fatalf("review on_failure route = %+v", a)
		}
	}
	counters, _ := repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if len(counters) != 0 {
		t.Fatalf("loop should not increment on infrastructure failure: %+v", counters)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(runner.calls))
	}
}

func TestUnlimitedAttemptsAndUnlimitedLoopConverges(t *testing.T) {
	// max_step_attempts=0 and max_iterations=-1 must never trip their caps:
	// the run converges on an approved verdict after one repair back-edge.
	wf := &definition.WorkflowFile{
		Version: 1, Name: "unlimited", InitialStep: "implement",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 0, MaxDurationSeconds: 3600},
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "dev", OnFailure: "failure",
				Context: []definition.ContextBinding{{From: "inputs.task", As: "task"}}},
			{ID: "review", Kind: "agent_gate", Agent: "rev", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
			{From: "review", To: "implement", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "changes_requested"}}, Loop: "review_repair", MaxIterations: -1},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"v1"}`),
		"review#1":    json.RawMessage(`{"verdict":"changes_requested","findings":[]}`),
		"implement#2": json.RawMessage(`{"summary":"v2"}`),
		"review#2":    json.RawMessage(`{"verdict":"approved","findings":[]}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, compiled, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-unlimited-loop", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	reviewAttempts := 0
	for _, a := range attempts {
		if a.StepID == "review" {
			reviewAttempts++
		}
	}
	if reviewAttempts != 2 {
		t.Fatalf("review attempts = %d, want 2: %+v", reviewAttempts, attempts)
	}
	counters, err := repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(counters) != 1 || counters[0].LoopName != "review_repair" || counters[0].Iterations != 1 {
		t.Fatalf("loop counters = %+v", counters)
	}
}

func TestUnlimitedAttemptsUnlimitedLoopStoppedByDeadline(t *testing.T) {
	// Unlimited caps are legal at runtime, but the admission deadline still
	// bounds the run: once the deadline passes, no implement#2 is dispatched.
	wf := &definition.WorkflowFile{
		Version: 1, Name: "unlimited", InitialStep: "implement",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 0, MaxDurationSeconds: 3600},
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "dev", OnFailure: "failure",
				Context: []definition.ContextBinding{{From: "inputs.task", As: "task"}}},
			{ID: "review", Kind: "agent_gate", Agent: "rev", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
			{From: "review", To: "implement", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "changes_requested"}}, Loop: "review_repair", MaxIterations: -1},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#*": json.RawMessage(`{"summary":"v"}`),
		"review#*":    json.RawMessage(`{"verdict":"changes_requested","findings":[]}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, compiled, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-unlimited-deadline", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	if err := ctrl.SetTimeSource(func() time.Time { return start }); err != nil {
		t.Fatal(err)
	}
	deadline := start.Add(time.Minute)
	if err := ctrl.SetAdmission(Admission{DeadlineAt: &deadline}); err != nil {
		t.Fatal(err)
	}
	// Admit the run explicitly; Run() cannot be used because the scripted
	// changes_requested outputs would spin until the wall-clock context expiry.
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	// implement#1 executes; run is not done.
	got, done, err := ctrl.Advance(context.Background())
	if err != nil || done {
		t.Fatalf("advance 1 = %+v done=%v err=%v", got, done, err)
	}
	// review#1 routes back to implement; run is still not done.
	got, done, err = ctrl.Advance(context.Background())
	if err != nil || done {
		t.Fatalf("advance 2 = %+v done=%v err=%v", got, done, err)
	}
	// Past the admission deadline: the next advance times out before
	// dispatching implement#2.
	ctrl.now = func() time.Time { return start.Add(2 * time.Minute) }
	got, done, err = ctrl.Advance(context.Background())
	if !done || got.Status != workflowledger.RunStatusTimedOut {
		t.Fatalf("advance 3 = %+v done=%v err=%v", got, done, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 2 {
		t.Fatalf("runner calls = %d, want 2 (no implement#2 dispatched): %+v", len(runner.calls), runner.calls)
	}
}
