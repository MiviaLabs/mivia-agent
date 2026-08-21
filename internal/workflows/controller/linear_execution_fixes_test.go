package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// deadlineRecordingRunner records the context deadline it observes on each
// RunStep call, mirroring how the production coordinator runner's dispatch
// consumes the step context (RunStep -> joinWithCancellation -> coordinator.Join).
type deadlineRecordingRunner struct {
	mu          sync.Mutex
	calls       int
	deadline    time.Time
	hasDeadline bool
}

func (r *deadlineRecordingRunner) RunStep(ctx context.Context, req AgentStepRequest) (AgentStepResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if d, ok := ctx.Deadline(); ok {
		r.deadline = d
		r.hasDeadline = true
	}
	return AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID, Status: "completed", Output: json.RawMessage(`{"ok":true}`)}, nil
}

// TestRedispatchRunsFreshChildUnderRunDeadlineNotPersistenceCtx pins bug (1):
// interruptAndRedispatch must pass the CALLER's run-loop context (carrying the
// step deadline) into executeAgentAttempt, not the 5-second write-bound
// stepPersistenceContext. A fresh re-dispatched child that runs under a 5s
// context is canceled at the coordinator.Join boundary after 5s and the
// re-dispatched attempt times out, so the whole run times out even though the
// run deadline is much longer.
func TestRedispatchRunsFreshChildUnderRunDeadlineNotPersistenceCtx(t *testing.T) {
	runner := &deadlineRecordingRunner{}
	ctrl, repo := newErrorController(t, runner, "wfr-redispatch-deadline")
	start := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	if err := ctrl.SetTimeSource(func() time.Time { return start }); err != nil {
		t.Fatal(err)
	}
	deadline := start.Add(30 * time.Second)
	if err := ctrl.SetAdmission(Admission{DeadlineAt: &deadline}); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), ctrl.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	// A RUNNING attempt with a recorded coordinator identity forces
	// joinInFlightAttempt -> interruptAndRedispatch (this runner has no join
	// capability), exactly the crash-resume path that re-dispatches a fresh
	// child with a new identity.
	if err := repo.CreateStepAttempt(context.Background(), workflowledger.StepAttempt{
		AttemptID: "wfa-one-1", RunID: ctrl.RunID, StepID: "one", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning, CoordinatorRunID: "coord-stale", TaskID: "task-stale",
	}); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	if _, err := ctrl.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.calls != 1 {
		t.Fatalf("RunStep calls = %d, want exactly 1 fresh dispatch", runner.calls)
	}
	if !runner.hasDeadline {
		t.Fatal("fresh child ran without a context deadline; want the run deadline")
	}
	// The fresh child must run under the RUN deadline (~30s from now), NOT the
	// 5-second stepPersistenceContext window. A 5s-bound child would be
	// canceled at the coordinator.Join boundary before the step completes.
	if remaining := runner.deadline.Sub(before); remaining < 20*time.Second {
		t.Fatalf("fresh child ctx deadline %s is only ~%s from test start; want the ~30s run deadline, not the 5s persistence window", runner.deadline.Format(time.RFC3339Nano), remaining)
	}
}

// TestCompletedChildSchemaInvalidOutputFailsClosed pins bug (2): a child that
// reports "completed" but whose output fails the declared OutputSchema is a
// SCHEMA failure, not a success. The attempt must classify as failed and use
// the on_failure route — the run fails closed instead of routing the
// schema-invalid output onward as Succeeded (which would bypass the
// OutputSchema guard and contradict the "schema failures use on_failure"
// contract).
func TestCompletedChildSchemaInvalidOutputFailsClosed(t *testing.T) {
	runner := &errorRunner{
		results: map[string]AgentStepResult{
			"one": {Output: json.RawMessage(`{"bad":true}`), Status: "completed"},
		},
		errors: map[string]error{
			"one": &SchemaValidationError{StepID: "one", Err: errors.New("property 'ok' is required")},
		},
	}
	ctrl, repo := newErrorController(t, runner, "wfr-schema-invalid")
	got, err := ctrl.Run(context.Background())
	if err == nil {
		t.Fatalf("run succeeded = %+v; want failure for schema-invalid output", got)
	}
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %q, want failed", got.Status)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want exactly 1", len(attempts))
	}
	if attempts[0].Status != workflowledger.AttemptStatusFailed {
		t.Fatalf("attempt status = %q, want failed (schema-invalid output must fail closed)", attempts[0].Status)
	}
	if attempts[0].ToStepID != "failure" {
		t.Fatalf("attempt route = %q, want on_failure target %q", attempts[0].ToStepID, "failure")
	}
	if attempts[0].ErrorRef == "" {
		t.Fatal("attempt ErrorRef is empty; schema validation detail must be recorded")
	}
}

// TestClassifyStepStatusSchemaValidationErrorFailsClosed pins the
// classification rule directly: a schema-validation error (direct or wrapped)
// paired with a "completed" child must classify as failed, never succeeded.
func TestClassifyStepStatusSchemaValidationErrorFailsClosed(t *testing.T) {
	schemaErr := &SchemaValidationError{StepID: "one", Err: errors.New("schema")}
	for _, tc := range []struct {
		name  string
		err   error
		child string
	}{
		{name: "direct completed", err: schemaErr, child: "completed"},
		{name: "wrapped completed", err: fmt.Errorf("join boundary: %w", schemaErr), child: "completed"},
		{name: "direct empty status", err: schemaErr, child: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyStepStatus(tc.err, tc.child); got != workflowledger.AttemptStatusFailed {
				t.Fatalf("classifyStepStatus(%v, %q) = %q, want failed", tc.err, tc.child, got)
			}
		})
	}
}

// expiringChildRunner waits for the step context to expire, then reports the
// child "completed" paired with the context error. It models a child whose
// work finishes exactly as the run deadline fires.
type expiringChildRunner struct{}

func (r *expiringChildRunner) RunStep(ctx context.Context, req AgentStepRequest) (AgentStepResult, error) {
	<-ctx.Done()
	return AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID, Status: "completed", Output: json.RawMessage(`{"ok":true}`)}, ctx.Err()
}

// ctxAwareLoopRepository mimics the production store, whose reads honor the
// caller context. The in-memory store ignores ctx, so without this wrapper a
// loop-counter read would never surface an expired run deadline in a test.
type ctxAwareLoopRepository struct {
	workflowledger.Repository
}

func (r *ctxAwareLoopRepository) GetLoopCounters(ctx context.Context, runID string) ([]workflowledger.LoopCounter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.Repository.GetLoopCounters(ctx, runID)
}

// TestSettleAgentAttemptRoutesBackEdgeWithExpiredRunDeadline pins the route
// computation context in settleAgentAttempt: when the run deadline expires
// while a completed child settles, route selection must read the ledger under
// the detached stepPersistenceContext, not the expired caller ctx. With the
// caller ctx, GetLoopCounters fails with context.DeadlineExceeded, the attempt
// is recorded Failed on the on_failure route, and the run settles failed
// instead of timed_out, discarding the completed child's success.
func TestSettleAgentAttemptRoutesBackEdgeWithExpiredRunDeadline(t *testing.T) {
	wf, err := definition.Compile(&definition.WorkflowFile{
		Version: 1, Name: "deadline-loop", InitialStep: "work",
		Steps: []definition.Step{{ID: "work", Kind: "agent", Agent: "worker"}},
		Transitions: []definition.Transition{
			{From: "work", To: "work", Match: definition.MatchCriteria{Status: "succeeded"}, Loop: "retry", MaxIterations: 5},
			// Satisfies the compiler's success-terminal rule; the output gate
			// never matches the child's output, so the back-edge always routes.
			{From: "work", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
		},
		Limits: definition.Limits{MaxDurationSeconds: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	base := workflowledger.NewMemoryRepository()
	repo := &ctxAwareLoopRepository{Repository: base}
	ctrl, err := NewLinearController(repo, &expiringChildRunner{}, wf, map[string]StepRuntime{
		"work": {Agent: agents.ResolvedAgent{Name: "worker"}},
	}, nil, "wfr-deadline-loop-route", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ctrl.Run(context.Background())
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run error = %v, want context.DeadlineExceeded", err)
	}
	stored, err := base.GetRun(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != workflowledger.RunStatusTimedOut {
		t.Fatalf("run status = %q, want timed_out", stored.Status)
	}
	attempts, err := base.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want exactly 1", len(attempts))
	}
	attempt := attempts[0]
	if attempt.Status != workflowledger.AttemptStatusSucceeded {
		t.Fatalf("attempt status = %q, want succeeded (the completed child must not be discarded)", attempt.Status)
	}
	if attempt.ToStepID != "work" {
		t.Fatalf("attempt route = %q, want the loop back-edge %q, not the on_failure route", attempt.ToStepID, "work")
	}
}
