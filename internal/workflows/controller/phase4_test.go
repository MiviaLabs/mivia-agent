package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

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
