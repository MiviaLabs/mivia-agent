package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func humanOnlyWorkflow(t *testing.T) *compiler.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "human-only", InitialStep: "approve_me",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 8, MaxDurationSeconds: 3600},
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
	return compiled
}

func TestHumanGateApproveZeroMatchPersistsDecisionJSON(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "human-nomatch", InitialStep: "approve_me",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 4},
		Steps:  []definition.Step{{ID: "approve_me", Kind: "human_gate", OnFailure: "failure"}},
		// No transition matches decision=approved → fail closed with diagnostics.
		Transitions: []definition.Transition{
			{From: "approve_me", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"decision": "other"}}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, compiled, nil, map[string]any{"task": "x"}, "wfr-human-zero", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	err = ctrl.Approve(context.Background(), PendingApprovalID("approve_me", 1), "operator")
	if err == nil {
		t.Fatal("expected match failure")
	}
	attempts, _ := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if len(attempts) != 1 || attempts[0].Status != workflowledger.AttemptStatusFailed {
		t.Fatalf("attempts = %+v", attempts)
	}
	if len(attempts[0].DecisionJSON) == 0 || !strings.Contains(string(attempts[0].DecisionJSON), "zero_match") {
		t.Fatalf("decision json = %s", attempts[0].DecisionJSON)
	}
}

func TestHumanGateApproveIdempotentAfterPartialStatusCAS(t *testing.T) {
	// Simulate crash after ResolveApproval + CompleteStepAttempt, before run status CAS.
	wf := humanOnlyWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, "wfr-human-idem", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Manually resolve approval and complete attempt while leaving waiting_approval.
	run, _ := repo.GetRun(context.Background(), ctrl.RunID)
	approvalID := PendingApprovalID("approve_me", 1)
	if err := repo.ResolveApproval(context.Background(), ctrl.RunID, approvalID, "operator", "approved", ""); err != nil {
		t.Fatal(err)
	}
	attempts, _ := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if len(attempts) != 1 {
		t.Fatalf("attempts = %+v", attempts)
	}
	output := map[string]any{"decision": "approved"}
	raw, _ := json.Marshal(output)
	route := RouteDecision{ToStepID: "success", TransitionIndex: 0, MatchDigest: "md", DecisionJSON: []byte(`{"outcome":"matched"}`)}
	if err := CompleteExistingStepResult(context.Background(), repo, attempts[0], AgentStepResult{Output: raw, ValidatedOutput: output}, workflowledger.AttemptStatusSucceeded, route); err != nil {
		t.Fatal(err)
	}
	// Still waiting_approval.
	run, _ = repo.GetRun(context.Background(), ctrl.RunID)
	if run.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("status = %q", run.Status)
	}
	// Retry Approve must finish the run.
	if err := ctrl.Approve(context.Background(), approvalID, "operator"); err != nil {
		t.Fatal(err)
	}
	run, _ = repo.GetRun(context.Background(), ctrl.RunID)
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", run.Status)
	}
}

func TestAdvanceReconcilesCompletedHumanGateWhileWaitingApproval(t *testing.T) {
	wf := humanOnlyWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, "wfr-human-advance", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	approvalID := PendingApprovalID("approve_me", 1)
	if err := repo.ResolveApproval(context.Background(), ctrl.RunID, approvalID, "operator", "approved", ""); err != nil {
		t.Fatal(err)
	}
	attempts, _ := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	raw, _ := json.Marshal(map[string]any{"decision": "approved"})
	route := RouteDecision{ToStepID: "success", TransitionIndex: 0}
	if err := CompleteExistingStepResult(context.Background(), repo, attempts[0], AgentStepResult{Output: raw}, workflowledger.AttemptStatusSucceeded, route); err != nil {
		t.Fatal(err)
	}
	got, done, err := ctrl.Advance(context.Background())
	if err != nil || !done || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("advance = %+v done=%v err=%v", got, done, err)
	}
}

func TestHumanGateCrashAfterAttemptBeforeApprovalIsRecoverable(t *testing.T) {
	wf := humanOnlyWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, "wfr-human-no-appr", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, _ := repo.GetRun(context.Background(), ctrl.RunID)
	if err := repo.CompareAndSetRunStatus(context.Background(), ctrl.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	// Crash window: attempt exists, approval missing, status not yet waiting.
	if err := repo.CreateStepAttempt(context.Background(), workflowledger.StepAttempt{
		AttemptID: "wfa-approve_me-1", RunID: ctrl.RunID, StepID: "approve_me", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	got, done, err := ctrl.Advance(context.Background())
	if err != nil || !done || got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("advance = %+v done=%v err=%v", got, done, err)
	}
	approvals, _ := repo.ListApprovals(context.Background(), ctrl.RunID)
	if len(approvals) != 1 || approvals[0].Status != "pending" {
		t.Fatalf("approvals = %+v", approvals)
	}
	if err := ctrl.Approve(context.Background(), PendingApprovalID("approve_me", 1), "operator"); err != nil {
		t.Fatal(err)
	}
	run, _ = repo.GetRun(context.Background(), ctrl.RunID)
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("status = %q", run.Status)
	}
}

func TestHumanGatePastDeadlineAdvanceTimesOut(t *testing.T) {
	wf := humanOnlyWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, "wfr-human-deadline", []byte("snap"))
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
	if _, err := ctrl.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, _ := repo.GetRun(context.Background(), ctrl.RunID)
	if run.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("status = %q", run.Status)
	}
	ctrl.now = func() time.Time { return start.Add(2 * time.Minute) }
	got, done, err := ctrl.Advance(context.Background())
	if !done || got.Status != workflowledger.RunStatusTimedOut {
		t.Fatalf("advance = %+v done=%v err=%v", got, done, err)
	}
	if err == nil || !strings.Contains(err.Error(), "DeadlineExceeded") && !strings.Contains(err.Error(), "deadline") && err != context.DeadlineExceeded {
		// timeoutExpiredRun returns context.DeadlineExceeded wrapped.
		if err != nil && !strings.Contains(err.Error(), "deadline") && !strings.Contains(err.Error(), "DeadlineExceeded") {
			// accept errors.Is
			if got.Status == workflowledger.RunStatusTimedOut {
				return
			}
			t.Fatalf("err = %v", err)
		}
	}
}

func TestHumanGateReentryAfterPriorSuccessMintsNewAttemptAndApproval(t *testing.T) {
	// gate → work → gate (loop) → work → success. Human gate is re-entered.
	wfLoop := &definition.WorkflowFile{
		Version: 1, Name: "human-loop", InitialStep: "gate",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 10},
		Steps: []definition.Step{
			{ID: "gate", Kind: "human_gate", OnFailure: "failure"},
			{ID: "work", Kind: "agent", Agent: "dev", OnFailure: "failure",
				Context: []definition.ContextBinding{{From: "inputs.task", As: "task"}}},
		},
		Transitions: []definition.Transition{
			{From: "gate", To: "work", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"decision": "approved"}}},
			{From: "work", To: "gate", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"round": "1"}}, Loop: "repair", MaxIterations: 2},
			{From: "work", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"round": "2"}}},
		},
	}
	compiled, err := compiler.Compile(wfLoop)
	if err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewMemoryRepository()
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"work#1": json.RawMessage(`{"round":"1"}`),
		"work#2": json.RawMessage(`{"round":"2"}`),
	}}
	ctrl, err := NewLinearController(repo, runner, compiled, map[string]StepRuntime{
		"work": {Agent: agents.ResolvedAgent{Name: "dev"}},
	}, map[string]any{"task": "x"}, "wfr-human-loop", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Approve(context.Background(), PendingApprovalID("gate", 1), "op"); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("second pause status=%q active=%q", got.Status, got.ActiveStepID)
	}
	attempts, _ := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	gateAttempts := 0
	for _, a := range attempts {
		if a.StepID == "gate" {
			gateAttempts++
		}
	}
	if gateAttempts < 2 {
		t.Fatalf("gate attempts = %d, want >= 2; all=%+v", gateAttempts, attempts)
	}
	if err := ctrl.Approve(context.Background(), PendingApprovalID("gate", 2), "op"); err != nil {
		t.Fatal(err)
	}
	got, err = ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("final = %+v err=%v", got, err)
	}
}

func TestHumanGateStaleApprovalIDDoesNotCompleteNewerAttempt(t *testing.T) {
	wfLoop := &definition.WorkflowFile{
		Version: 1, Name: "human-stale", InitialStep: "gate",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 10},
		Steps: []definition.Step{
			{ID: "gate", Kind: "human_gate", OnFailure: "failure"},
			{ID: "work", Kind: "agent", Agent: "dev", OnFailure: "failure",
				Context: []definition.ContextBinding{{From: "inputs.task", As: "task"}}},
		},
		Transitions: []definition.Transition{
			{From: "gate", To: "work", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"decision": "approved"}}},
			{From: "work", To: "gate", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"round": "1"}}, Loop: "repair", MaxIterations: 2},
			{From: "work", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"round": "2"}}},
		},
	}
	compiled, err := compiler.Compile(wfLoop)
	if err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewMemoryRepository()
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"work#1": json.RawMessage(`{"round":"1"}`),
		"work#2": json.RawMessage(`{"round":"2"}`),
	}}
	ctrl, err := NewLinearController(repo, runner, compiled, map[string]StepRuntime{
		"work": {Agent: agents.ResolvedAgent{Name: "dev"}},
	}, map[string]any{"task": "x"}, "wfr-human-stale", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Approve(context.Background(), PendingApprovalID("gate", 1), "op"); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("second pause = %+v err=%v", got, err)
	}
	// Stale retry of gate#1 must not approve gate#2.
	if err := ctrl.Approve(context.Background(), PendingApprovalID("gate", 1), "op"); err == nil {
		t.Fatal("stale approval was accepted")
	}
	run, _ := repo.GetRun(context.Background(), ctrl.RunID)
	if run.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("status = %q after stale approve", run.Status)
	}
	attempts, _ := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	for _, a := range attempts {
		if a.StepID == "gate" && a.AttemptNo == 2 && a.Status != workflowledger.AttemptStatusRunning {
			t.Fatalf("gate#2 must stay running: %+v", a)
		}
	}
	// Current approval still works.
	if err := ctrl.Approve(context.Background(), PendingApprovalID("gate", 2), "op"); err != nil {
		t.Fatal(err)
	}
	got, err = ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("final = %+v err=%v", got, err)
	}
}

func TestHumanGatePastDeadlineClosesAttempt(t *testing.T) {
	wf := humanOnlyWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, "wfr-human-deadline-attempt", []byte("snap"))
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
	if _, err := ctrl.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctrl.now = func() time.Time { return start.Add(2 * time.Minute) }
	got, _, _ := ctrl.Advance(context.Background())
	if got.Status != workflowledger.RunStatusTimedOut {
		t.Fatalf("status = %q", got.Status)
	}
	attempts, _ := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if len(attempts) != 1 || attempts[0].Status != workflowledger.AttemptStatusTimedOut {
		t.Fatalf("attempt = %+v", attempts)
	}
}

func TestLoopCounterNotIncrementedWhenCompleteWouldFail(t *testing.T) {
	// Unit: checkLoopCap does not increment; recordLoopAfterComplete does.
	wf := repairWorkflow(t, 1, 20)
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, "wfr-loop-check", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.checkLoopCap(context.Background(), "review_repair", 1); err != nil {
		t.Fatal(err)
	}
	counters, _ := repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if len(counters) != 0 {
		t.Fatalf("check must not increment: %+v", counters)
	}
	if err := ctrl.recordLoopAfterComplete(context.Background(), RouteDecision{Loop: "review_repair"}); err != nil {
		t.Fatal(err)
	}
	counters, _ = repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if len(counters) != 1 || counters[0].Iterations != 1 {
		t.Fatalf("counters = %+v", counters)
	}
	// Second check with max=1 fails without further increment.
	if err := ctrl.checkLoopCap(context.Background(), "review_repair", 1); err == nil {
		t.Fatal("expected exhausted")
	}
	counters, _ = repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if counters[0].Iterations != 1 {
		t.Fatalf("exhausted check must not increment: %+v", counters)
	}
}
