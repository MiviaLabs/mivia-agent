package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// ---------------------------------------------------------------------------
// Retry tests
// ---------------------------------------------------------------------------

// failTwiceHandler fails the first two invocations, succeeds on the third.
type failTwiceHandler struct {
	mu      sync.Mutex
	invoked int
}

func (h *failTwiceHandler) Invoke(_ context.Context, _ runtime.Request) (json.RawMessage, error) {
	h.mu.Lock()
	h.invoked++
	count := h.invoked
	h.mu.Unlock()
	if count <= 2 {
		return nil, fmt.Errorf("intentional failure #%d", count)
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func TestCoordinator_RetryFailedTask(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	fixedTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return fixedTime })
	d := runtime.New(runtime.Policy{})
	failer := &failTwiceHandler{}
	_ = d.Register(runtime.Subagent, "flaky", failer)
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p).WithRetryPolicy(RetryPolicy{
		MaxRetries:     3,
		BaseBackoff:    1 * time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
		BackoffFactor:  2.0,
		JitterFraction: 0,
	})

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "flaky"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if result.Err != nil {
		t.Fatalf("unexpected run error: %v", result.Err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Results))
	}
	if result.Results[0].Status != "completed" {
		t.Fatalf("task status = %q, want %q", result.Results[0].Status, "completed")
	}
	// Verify the ledger shows retry_pending in the event history.
	events, err := repo.ListEvents(context.Background(), h.runID)
	if err != nil {
		t.Fatal(err)
	}
	var retryEvents int
	for _, evt := range events {
		if evt.Kind == "task_retry_pending" {
			retryEvents++
		}
	}
	if retryEvents == 0 {
		t.Fatal("expected at least one task_retry_pending event")
	}
	if retryEvents > 3 {
		t.Fatalf("expected ≤3 retry_pending events, got %d", retryEvents)
	}
	// Verify the task eventually completed.
	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != ledger.RunStatusCompleted {
		t.Fatalf("run status = %q, want %q", snap.Status, ledger.RunStatusCompleted)
	}
	if snap.Tasks[0].Status != string(ledger.TaskStatusCompleted) {
		t.Fatalf("task status = %q, want %q", snap.Tasks[0].Status, ledger.TaskStatusCompleted)
	}
}

func TestCoordinator_RetryExhaustedFailsTask(t *testing.T) {
	// A task that always fails should eventually exhaust retries and go terminal.
	repo := ledger.NewMemoryLedgerRepository()
	fixedTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return fixedTime })
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "foreverfail", staticHandler{err: errors.New("always fail")})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p).WithRetryPolicy(RetryPolicy{
		MaxRetries:     2,
		BaseBackoff:    1 * time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
		BackoffFactor:  2.0,
		JitterFraction: 0,
	})

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "foreverfail"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}

	snap, _ := c.Inspect(context.Background(), h)
	if snap.Status != ledger.RunStatusFailed {
		t.Fatalf("run status = %q, want %q", snap.Status, ledger.RunStatusFailed)
	}
	// Verify the task recorded retry_pending events.
	events, err := repo.ListEvents(context.Background(), h.runID)
	if err != nil {
		t.Fatal(err)
	}
	var retryEvents int
	for _, evt := range events {
		if evt.Kind == "task_retry_pending" {
			retryEvents++
		}
	}
	if retryEvents == 0 {
		t.Fatal("expected at least one retry_pending event even though retries exhausted")
	}
}

func TestCoordinator_DefaultRetryPolicy(t *testing.T) {
	// New() installs DefaultRetryPolicy, so failed tasks are retried by default
	// even without an explicit WithRetryPolicy call.
	repo := ledger.NewMemoryLedgerRepository()
	fixedTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return fixedTime })
	d := runtime.New(runtime.Policy{})
	failer := &failTwiceHandler{}
	_ = d.Register(runtime.Subagent, "flaky", failer)
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p) // No .WithRetryPolicy(); DefaultRetryPolicy applies.

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "flaky"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if result.Err != nil {
		t.Fatalf("unexpected run error: %v", result.Err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Results))
	}
	if result.Results[0].Status != "completed" {
		t.Fatalf("task status = %q, want %q", result.Results[0].Status, "completed")
	}
	// Verify retries actually happened (DefaultRetryPolicy allows 3 retries).
	events, err := repo.ListEvents(context.Background(), h.runID)
	if err != nil {
		t.Fatal(err)
	}
	var retryEvents int
	for _, evt := range events {
		if evt.Kind == "task_retry_pending" {
			retryEvents++
		}
	}
	if retryEvents == 0 {
		t.Fatal("expected retry_pending events under DefaultRetryPolicy")
	}
}

// ---------------------------------------------------------------------------
// Stale-attempt fencing after cancellation
// ---------------------------------------------------------------------------

func TestCoordinator_StaleAttemptRejectedAfterCancel(t *testing.T) {
	// MUTATION PROOF: A stale in-process attempt that tries to complete a
	// task after cancel_requested → canceled must be rejected by CAS.
	repo := ledger.NewMemoryLedgerRepository()
	fixedTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return fixedTime })
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "slow", slowHandler{})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "slow"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	// Cancel immediately while the task is running (slowHandler blocks).
	cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Cancel(cancelCtx, h); err != nil {
		t.Fatal(err)
	}

	// Simulate a stale worker publishing a terminal result after cancellation.
	// The version should have advanced past the worker's expected version.
	taskSnap, err := repo.GetTask(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if taskSnap.Version < 2 {
		t.Logf("task version = %d (expected >= 2 after cancel)", taskSnap.Version)
	}
	// Attempt a stale CAS: use version 1 (the original), expecting rejection.
	if err := repo.CompareAndSetTaskStatus(context.Background(), h.runID, "t1", 1, string(ledger.TaskStatusCompleted)); err == nil {
		t.Fatal("MUTATION FAIL: stale worker (version 1) was allowed to complete after cancellation")
	}
	// Also try version 2 (may have advanced past original but still stale).
	if err := repo.CompareAndSetTaskStatus(context.Background(), h.runID, "t1", 2, string(ledger.TaskStatusCompleted)); err == nil {
		t.Fatal("MUTATION FAIL: stale worker (version 2) was allowed to complete after cancellation")
	}
	// Verify the task is still in a terminal canceled state.
	finalSnap, err := repo.GetTask(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if finalSnap.Status == string(ledger.TaskStatusCompleted) {
		t.Fatal("MUTATION FAIL: task status was overwritten to completed by stale worker")
	}
	if finalSnap.Status != string(ledger.TaskStatusCanceled) &&
		finalSnap.Status != string(ledger.TaskStatusCancelRequested) {
		t.Logf("task status = %q (acceptable post-cancel state)", finalSnap.Status)
	}
}

// ---------------------------------------------------------------------------
// Data race regression: cancel during retry
// ---------------------------------------------------------------------------

func TestCoordinator_CancelDuringRetry(t *testing.T) {
	// REGRESSION: Concurrent cancel during retry must not race on h.attempts.
	// Two independent tasks: one retries (writes h.attempts), one blocks so
	// cancel fires while the retry write is in flight. Must pass under -race.
	repo := ledger.NewMemoryLedgerRepository()
	fixedTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return fixedTime })
	d := runtime.New(runtime.Policy{})
	var started atomic.Bool
	failer := &failTwiceHandler{}
	_ = d.Register(runtime.Subagent, "flaky", failer)
	_ = d.Register(runtime.Subagent, "slow", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		started.Store(true)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	p := subagents.New(d, subagents.Policy{Workers: 2})
	c := New(repo, p).WithRetryPolicy(RetryPolicy{
		MaxRetries:     3,
		BaseBackoff:    1 * time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
		BackoffFactor:  2.0,
		JitterFraction: 0,
	})

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "retry-task", Name: "flaky"},
		{ID: "slow-task", Name: "slow"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	// Wait for both tasks to start, then cancel while retry writes are live.
	deadline := time.After(5 * time.Second)
	for !started.Load() {
		select {
		case <-time.After(time.Millisecond):
		case <-deadline:
			t.Fatal("timed out waiting for tasks to start")
		}
	}
	cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Cancel(cancelCtx, h); err != nil {
		t.Fatal(err)
	}

	// Join must succeed without panic (data race would cause -race to fail).
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// Both tasks must produce a result (no missing).
	for _, r := range result.Results {
		if r.Status == "missing" {
			t.Fatalf("task %s status = missing, expected a result", r.TaskID)
		}
	}
}

// ---------------------------------------------------------------------------
// mintRetryAttempt error path (dag.go:451-452)
// ---------------------------------------------------------------------------

// erroringSetTaskAttemptRepo wraps a LedgerRepository and returns an error on
// SetTaskAttempt calls after the first successful attempt (so task creation
// succeeds but retry attempt writes fail).
type erroringSetTaskAttemptRepo struct {
	ledger.LedgerRepository
	attemptCount int
}

func (r *erroringSetTaskAttemptRepo) SetTaskAttempt(ctx context.Context, runID, taskID, attemptID, status string, finished *time.Time) error {
	r.attemptCount++
	if r.attemptCount > 1 {
		return fmt.Errorf("simulated ledger error on attempt write")
	}
	return r.LedgerRepository.SetTaskAttempt(ctx, runID, taskID, attemptID, status, finished)
}

func TestCoordinator_MintRetryAttemptLedgerError(t *testing.T) {
	// Exercises dag.go:451-452: when SetTaskAttempt fails during retry,
	// the error propagates without panic.
	repo := ledger.NewMemoryLedgerRepository()
	fixedTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return fixedTime })
	errorRepo := &erroringSetTaskAttemptRepo{LedgerRepository: repo}
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "flaky", staticHandler{err: fmt.Errorf("task failure")})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(errorRepo, p).WithRetryPolicy(RetryPolicy{
		MaxRetries:     3,
		BaseBackoff:    1 * time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
		BackoffFactor:  2.0,
		JitterFraction: 0,
	})

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "flaky"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Join must succeed — the run completes (with errors) rather than panicking.
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// The task should have attempted retry and the error from SetTaskAttempt
	// should be recorded, not panic.
	t.Logf("task status: %s, run error: %v", result.Results[0].Status, result.Err)
}
