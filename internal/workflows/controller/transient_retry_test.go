package controller

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// transientFailRunner fails the first N calls with a transient provider error
// and then succeeds, recording every request so the test can assert the retry
// count and that each retry carries a fresh task identity.
type transientFailRunner struct {
	mu       sync.Mutex
	calls    []AgentStepRequest
	failures int
	err      error
}

func (r *transientFailRunner) RunStep(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, req)
	if len(r.calls) <= r.failures {
		return AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID, Status: "failed"}, r.err
	}
	return AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID, Status: "completed", Output: json.RawMessage(`{"ok":true}`)}, nil
}

// TestAgentStepRetriesTransientProviderError pins the step-level retry for
// transient LLM-provider failures (the zai 429 overload that killed workflow
// runs): the step re-runs with a fresh task identity and a fresh child
// context, and the attempt is persisted once, succeeded.
func TestAgentStepRetriesTransientProviderError(t *testing.T) {
	orig := stepTransientRetryBackoff
	stepTransientRetryBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { stepTransientRetryBackoff = orig })

	runner := &transientFailRunner{
		failures: 2,
		err:      errors.New("zai: provider error (HTTP 429, code 1305: service temporarily overloaded)"),
	}
	ctrl, repo := newErrorController(t, runner, "wfr-transient-retry")
	got, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("run failed after transient retries: %v", err)
	}
	if got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", got.Status)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 3 {
		t.Fatalf("RunStep calls = %d, want 3 (1 + 2 retries)", len(runner.calls))
	}
	ids := map[string]bool{}
	for _, call := range runner.calls {
		ids[call.TaskID] = true
	}
	if len(ids) != 3 {
		t.Fatalf("retries reused a task identity: %v", ids)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != workflowledger.AttemptStatusSucceeded {
		t.Fatalf("attempts = %+v, want one succeeded attempt (retries must not leak attempts)", attempts)
	}
}

// TestAgentStepTransientRetryStopsAtCap pins the bound: after the retry
// budget is exhausted the step fails (no infinite retry), and the failed
// attempt is persisted exactly once.
func TestAgentStepTransientRetryStopsAtCap(t *testing.T) {
	orig := stepTransientRetryBackoff
	stepTransientRetryBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { stepTransientRetryBackoff = orig })

	runner := &transientFailRunner{
		failures: 10, // exceed the cap
		err:      errors.New("zai: provider error (HTTP 503, code 1305: service temporarily overloaded)"),
	}
	ctrl, repo := newErrorController(t, runner, "wfr-transient-cap")
	got, err := ctrl.Run(context.Background())
	if err == nil {
		t.Fatalf("run succeeded = %+v; want failure after retry budget exhausted", got)
	}
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != maxTransientStepRetries+1 {
		t.Fatalf("RunStep calls = %d, want %d (initial + retries)", len(runner.calls), maxTransientStepRetries+1)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != workflowledger.AttemptStatusFailed {
		t.Fatalf("attempts = %+v, want exactly one failed attempt", attempts)
	}
}

// TestAgentStepDoesNotRetryNonTransientFailure pins the contract: a real
// agent failure (schema/binding/refusal) does not match the transient markers
// and is NOT retried — it fails immediately through on_failure.
func TestAgentStepDoesNotRetryNonTransientFailure(t *testing.T) {
	orig := stepTransientRetryBackoff
	stepTransientRetryBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { stepTransientRetryBackoff = orig })

	runner := &transientFailRunner{
		failures: 1,
		err:      errors.New("workflow step output schema validation failed"),
	}
	ctrl, _ := newErrorController(t, runner, "wfr-nontransient")
	got, err := ctrl.Run(context.Background())
	if err == nil {
		t.Fatalf("run succeeded = %+v; want failure", got)
	}
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 1 {
		t.Fatalf("RunStep calls = %d, want exactly 1 (non-transient failures are not retried)", len(runner.calls))
	}
}

// transientFailOnceHandler fails the first invocation with a transient
// provider error and succeeds on every later invocation. It is driven by the
// REAL coordinator in the regression test below, which mirrors production
// wiring (internal/cli/orchestration_state.go installs coordinator.NoRetry):
// the workflow-level transient retry is the sole retry layer.
type transientFailOnceHandler struct {
	mu      sync.Mutex
	invoked int
	err     error
}

func (h *transientFailOnceHandler) Invoke(_ context.Context, _ runtime.Request) (json.RawMessage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.invoked++
	if h.invoked == 1 {
		return nil, h.err
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func (h *transientFailOnceHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.invoked
}

// TestAgentStepTransientRetryThroughRealCoordinator pins the fix for the dead
// transient-retry path: the retry branch minted a new TaskID but reused
// req.CoordinatorRunID, so the retry's EnsureRun found the new idempotency
// key absent and createAndStartRunWithID hit ErrDuplicate on the existing run
// ID — every transient retry failed in production. Through the REAL
// coordinator, a step whose child fails once with a transient marker and
// succeeds on the second RunStep must complete, the workflow run must reach
// RunStatusSucceeded, and the retry must mint a NEW coordinator run: the
// coordinator ledger holds a second (completed) run for the step, and the
// workflow attempt records that run's ID. The first failed child run stays in
// the ledger as a terminal failed run (orphaned but harmless).
func TestAgentStepTransientRetryThroughRealCoordinator(t *testing.T) {
	orig := stepTransientRetryBackoff
	stepTransientRetryBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { stepTransientRetryBackoff = orig })

	d := runtime.New(runtime.Policy{})
	handler := &transientFailOnceHandler{err: errors.New("zai: provider error (HTTP 503, code 1305: service temporarily overloaded)")}
	if err := d.Register(runtime.Subagent, "dev", handler); err != nil {
		t.Fatal(err)
	}
	p := subagents.New(d, subagents.Policy{Workers: 1})
	coordRepo := ledger.NewMemoryLedgerRepository()
	coord := coordinator.New(coordRepo, p).WithRetryPolicy(coordinator.NoRetry)

	ctrl, repo := newErrorController(t, NewCoordinatorRunner(coord), "wfr-real-transient-retry")
	got, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("run failed after transient retry through the real coordinator: %v", err)
	}
	if got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", got.Status)
	}
	if n := handler.count(); n != 2 {
		t.Fatalf("child invocations = %d, want 2 (fail once, succeed on the second RunStep)", n)
	}

	// The retry minted a NEW coordinator run ID: the coordinator ledger holds
	// two runs for this step. The first is terminal failed (orphaned but
	// harmless); the second is completed and is the one the workflow attempt
	// records.
	runs, err := coordRepo.ListRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("coordinator ledger runs = %d, want 2 (failed first attempt + successful retry); runs = %+v", len(runs), runs)
	}
	var failedRun, okRun *ledger.RunSnapshot
	for i := range runs {
		switch runs[i].Status {
		case ledger.RunStatusFailed:
			failedRun = &runs[i]
		case ledger.RunStatusCompleted:
			okRun = &runs[i]
		}
	}
	if failedRun == nil || okRun == nil {
		t.Fatalf("coordinator runs = %+v; want exactly one failed and one completed", runs)
	}
	if failedRun.RunID == okRun.RunID {
		t.Fatalf("retry reused the failed coordinator run ID %q", failedRun.RunID)
	}

	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != workflowledger.AttemptStatusSucceeded {
		t.Fatalf("attempts = %+v, want exactly one succeeded attempt (retries must not leak attempts)", attempts)
	}
	if attempts[0].CoordinatorRunID != okRun.RunID {
		t.Fatalf("attempt CoordinatorRunID = %q, want the successful retry run %q", attempts[0].CoordinatorRunID, okRun.RunID)
	}
	if attempts[0].CoordinatorRunID == failedRun.RunID {
		t.Fatalf("attempt CoordinatorRunID = %q; the retry must not record the orphaned failed run", failedRun.RunID)
	}
	tasks, err := coordRepo.ListTasks(context.Background(), okRun.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TaskID != attempts[0].TaskID {
		t.Fatalf("coordinator retry tasks = %+v, want the attempt's TaskID %q", tasks, attempts[0].TaskID)
	}
}
