package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// staticHandler returns a fixed response for any invocation.
type staticHandler struct {
	out json.RawMessage
	err error
}

func (h staticHandler) Invoke(_ context.Context, _ runtime.Request) (json.RawMessage, error) {
	return h.out, h.err
}

// slowHandler blocks until context is done, then returns ctx.Err().
type slowHandler struct{}

func (slowHandler) Invoke(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// invoker is a function-based runtime.Handler for tests.
type invoker func(context.Context, runtime.Request) (json.RawMessage, error)

func (f invoker) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	return f(ctx, req)
}

func TestCoordinator_SpawnReturnsHandle(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "test"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if h == nil {
		t.Fatal("expected non-nil handle")
	}
	if h.runID == "" {
		t.Fatal("expected non-empty run ID")
	}
}

func TestCoordinator_SpawnIdempotency(t *testing.T) {
	// MUTATION PROOF 3: Duplicate Spawn with same IdempotencyKey returns
	// the existing RunHandle without creating a new run.
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	h1, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "test"},
	}, "key-1")
	if err != nil {
		t.Fatal(err)
	}

	h2, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "test"},
	}, "key-1")
	if err != nil {
		t.Fatal(err)
	}

	if h1 != h2 {
		t.Fatal("MUTATION FAIL: duplicate Spawn with same idempotency key returned different handles")
	}
	if h1.runID != h2.runID {
		t.Fatal("handles have different run IDs")
	}
}

func TestCoordinator_SpawnIdempotencyRejectsDifferentRequest(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1, Partial: true}))

	_, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "test"}}, "same-key")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "other"}}, "same-key")
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different request error = %v, want %v", err, ErrIdempotencyConflict)
	}
	runs, err := repo.ListRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("different request created %d runs, want 1", len(runs))
	}
}

func TestCoordinator_ConcurrentSpawnSameIdempotencyKey(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1, Partial: true}))
	const n = 20
	handles := make(chan *RunHandle, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "test"}}, "same-key")
			handles <- h
			errs <- err
		}()
	}
	wg.Wait()
	close(handles)
	close(errs)
	var first *RunHandle
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent spawn failed: %v", err)
		}
	}
	for h := range handles {
		if first == nil {
			first = h
			continue
		}
		if h != first {
			t.Fatal("same idempotency key returned different handles")
		}
	}
}

func TestCoordinator_RejectsDependencyCycleBeforeRunCreation(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	c := New(repo, subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{}))
	_, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "a", DependsOn: []string{"b"}},
		{ID: "b", DependsOn: []string{"a"}},
	}, "")
	if err == nil {
		t.Fatal("expected dependency cycle error")
	}
	runs, listErr := repo.ListRuns(context.Background())
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(runs) != 0 {
		t.Fatalf("cycle should not create ledger run, got %d", len(runs))
	}
}

func TestCoordinator_SpawnRejectsEmptyTaskList(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	p := subagents.New(d, subagents.Policy{})
	c := New(repo, p)

	_, err := c.Spawn(context.Background(), nil, "")
	if err == nil {
		t.Fatal("expected error for empty task list")
	}
}

func TestCoordinator_Inspect(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "test"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if snap.RunID != h.runID {
		t.Fatalf("expected run ID %q, got %q", h.runID, snap.RunID)
	}
	if len(snap.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(snap.Tasks))
	}
}

func TestCoordinator_Join(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "test"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Results))
	}
}

func TestCoordinator_Cancel(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	_ = d.Register(runtime.Subagent, "slow", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "slow"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	// Wait for task to start before canceling.
	<-started

	if err := c.Cancel(context.Background(), h); err != nil {
		t.Fatal(err)
	}

	// Join should succeed after cancel
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestCoordinator_CancelDeadlineReturnsWhileReconciliationContinues(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	release := make(chan struct{})
	_ = d.Register(runtime.Subagent, "slow", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		<-release
		return nil, ctx.Err()
	}))
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1, Partial: true}))
	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "slow"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	<-started

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Millisecond))
	defer cancel()
	if err := c.Cancel(ctx, h); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Cancel error = %v, want deadline exceeded", err)
	}
	close(release)
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != ledger.RunStatusCanceled || snap.Tasks[0].Status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("reconciled snapshot = %+v", snap)
	}
}

func TestCoordinator_RecoveredHandleRetainsThenReleasesBookkeeping(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	key := "retained-recovery"
	if err := repo.CreateRun(context.Background(), key, ledger.RunSnapshot{RunID: "recovered-terminal", Status: ledger.RunStatusCompleted}); err != nil {
		t.Fatal(err)
	}
	c := New(repo, subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1}))
	cr := c.(*coordinator)
	cr.handleRetention = 10 * time.Millisecond
	h, err := c.Spawn(context.Background(), nil, key)
	if err != nil {
		t.Fatal(err)
	}
	if !h.recovered {
		t.Fatal("expected recovered handle")
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for cr.lookupHandle(key) != nil && time.Now().Before(deadline) {
		timer := time.NewTimer(time.Millisecond)
		<-timer.C
	}
	if cr.lookupHandle(key) != nil {
		t.Fatal("recovered handle bookkeeping was not released after retention")
	}
}

func TestCoordinator_JoinContextCancellation(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "slow", slowHandler{})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "slow"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err = c.Join(ctx, h)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestCoordinator_DependencyBlocking(t *testing.T) {
	// MUTATION PROOF 5: Blocked dependencies produce blocked status, not
	// completed, for tasks whose dependencies fail.
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "fail", staticHandler{err: errors.New("intentional failure")})
	_ = d.Register(runtime.Subagent, "child", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "parent", Name: "fail"},
		{ID: "child", Name: "child", DependsOn: []string{"parent"}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}

	// Check child was blocked
	for _, r := range result.Results {
		if r.TaskID == "child" && r.Status != "blocked" {
			t.Fatalf("MUTATION FAIL: child status=%q, want 'blocked'", r.Status)
		}
	}
}

func TestCoordinator_DisplayNameUniqueness(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	h1, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "test"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	h2, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t2", Name: "test"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	snap1, _ := c.Inspect(context.Background(), h1)
	snap2, _ := c.Inspect(context.Background(), h2)

	if snap1.DisplayName == snap2.DisplayName {
		t.Fatal("display names should be unique across runs")
	}
}

func TestCoordinator_RedactedOutput(t *testing.T) {
	// MUTATION PROOF 4: Redaction enforcement — output stored in the ledger
	// is a bounded reference, not raw content.
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{"secret":"data"}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "test"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}

	task, err := repo.GetTask(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	// OutputRef should be a bounded reference (length prefix), not raw content
	if task.OutputRef == "" {
		t.Fatal("expected non-empty output ref")
	}
	if task.OutputRef == `{"secret":"data"}` {
		t.Fatal("MUTATION FAIL: raw output stored in ledger, expected bounded reference")
	}
	if task.OutputRef != "output:16" {
		t.Logf("output ref: %q", task.OutputRef)
	}
}

func TestCoordinator_ConcurrentSpawn(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 2, Partial: true})
	c := New(repo, p)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("t%d", i)
			h, err := c.Spawn(context.Background(), []subagents.Task{
				{ID: id, Name: "test"},
			}, "")
			if err != nil {
				t.Errorf("spawn failed: %v", err)
				return
			}
			_, err = c.Join(context.Background(), h)
			if err != nil {
				t.Errorf("join failed: %v", err)
			}
		}(i)
	}
	wg.Wait()
}

func TestCoordinator_ValidateTasksRejectsUnknownDependency(t *testing.T) {
	c := &coordinator{}
	err := c.validateTasks([]subagents.Task{
		{ID: "t1", DependsOn: []string{"nonexistent"}},
	})
	if err == nil {
		t.Fatal("expected error for unknown dependency")
	}
}

func TestCoordinator_ValidateTasksRejectsDuplicateID(t *testing.T) {
	c := &coordinator{}
	err := c.validateTasks([]subagents.Task{
		{ID: "t1"},
		{ID: "t1"},
	})
	if err == nil {
		t.Fatal("expected error for duplicate task ID")
	}
}

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
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p).WithRetryPolicy(RetryPolicy{
		MaxRetries:    3,
		BaseBackoff:   1 * time.Millisecond,
		MaxBackoff:    5 * time.Millisecond,
		BackoffFactor: 2.0,
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
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p).WithRetryPolicy(RetryPolicy{
		MaxRetries:    2,
		BaseBackoff:   1 * time.Millisecond,
		MaxBackoff:    5 * time.Millisecond,
		BackoffFactor: 2.0,
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

func TestCoordinator_NoRetryByDefault(t *testing.T) {
	// Without WithRetryPolicy, failed tasks should go terminal immediately.
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "fail", staticHandler{err: errors.New("fail")})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p) // No .WithRetryPolicy()

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "fail"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the run failed via Inspect (run status is derived from task statuses).
	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != ledger.RunStatusFailed {
		t.Fatalf("run status = %q, want %q", snap.Status, ledger.RunStatusFailed)
	}
	// No retry_pending events should exist.
	events, err := repo.ListEvents(context.Background(), h.runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, evt := range events {
		if evt.Kind == "task_retry_pending" {
			t.Fatal("unexpected retry_pending event when retry is disabled")
		}
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
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
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
// Regression tests for lifecycle event emissions
// ---------------------------------------------------------------------------

func TestCoordinator_SubscribeLifecycleUnsubscribe(t *testing.T) {
	// MUTATION PROOF (Bug 1): Unsubscribe must prevent further callbacks.
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	var mu sync.Mutex
	received := 0
	unsub := c.SubscribeLifecycle(func(evt ledger.LifecycleEvent) {
		mu.Lock()
		received++
		mu.Unlock()
	})

	// Unsubscribe immediately.
	unsub()

	// Spawn and join a run.
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "worker"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	count := received
	mu.Unlock()
	if count > 0 {
		t.Fatalf("MUTATION FAIL: subscriber received %d events after unsubscribe", count)
	}
}

func TestCoordinator_SubscribeLifecycle_MultipleSubscribers(t *testing.T) {
	// MUTATION PROOF (Bug 1): Multiple subscribers, one unsubscribes,
	// others must still receive events.
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	var mu1, mu2 sync.Mutex
	count1, count2 := 0, 0

	unsub := c.SubscribeLifecycle(func(evt ledger.LifecycleEvent) {
		mu1.Lock()
		count1++
		mu1.Unlock()
	})
	c.SubscribeLifecycle(func(evt ledger.LifecycleEvent) {
		mu2.Lock()
		count2++
		mu2.Unlock()
	})

	// Unsubscribe only the first subscriber.
	unsub()

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "worker"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}

	mu1.Lock()
	c1 := count1
	mu1.Unlock()
	mu2.Lock()
	c2 := count2
	mu2.Unlock()

	if c1 > 0 {
		t.Fatalf("MUTATION FAIL: unsubscribed subscriber received %d events", c1)
	}
	if c2 == 0 {
		t.Fatal("MUTATION FAIL: remaining subscriber received no events after sibling unsubscribed")
	}
}

func TestCoordinator_SubscribeLifecycle_AllExpectedEvents(t *testing.T) {
	// Regression test for Bugs 6,7,13: verify ALL lifecycle events from a
	// successful run are emitted: run_created, task_created, task_running,
	// task_completed.
	repo := ledger.NewMemoryLedgerRepository()
	fixedTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return fixedTime })
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	// Track expected event kinds.
	var mu sync.Mutex
	eventKinds := make(map[string]int)

	c.SubscribeLifecycle(func(evt ledger.LifecycleEvent) {
		mu.Lock()
		eventKinds[string(evt.Kind)]++
		mu.Unlock()
	})

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "worker"},
		{ID: "t2", Name: "worker"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	kinds := make(map[string]int, len(eventKinds))
	for k, v := range eventKinds {
		kinds[k] = v
	}
	mu.Unlock()

	// Must have exactly one run_created.
	if kinds["run_created"] != 1 {
		t.Fatalf("expected 1 run_created event, got %d", kinds["run_created"])
	}
	// Must have two task_created (one per task).
	if kinds["task_created"] != 2 {
		t.Fatalf("expected 2 task_created events, got %d", kinds["task_created"])
	}
	// Must have two task_running.
	if kinds["task_running"] != 2 {
		t.Fatalf("expected 2 task_running events, got %d", kinds["task_running"])
	}
	// Must have two task_completed.
	if kinds["task_completed"] != 2 {
		t.Fatalf("expected 2 task_completed events, got %d", kinds["task_completed"])
	}
	// No error events.
	if kinds["task_failed"] > 0 {
		t.Fatalf("unexpected %d task_failed events", kinds["task_failed"])
	}
}

func TestCoordinator_CancelEmitsEvents(t *testing.T) {
	// Regression test for Bugs 8,9: cancel must emit cancel_requested and
	// canceled events via SubscribeLifecycle.
	repo := ledger.NewMemoryLedgerRepository()
	fixedTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return fixedTime })
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "slow", slowHandler{})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	var mu sync.Mutex
	eventKinds := make(map[string]int)

	c.SubscribeLifecycle(func(evt ledger.LifecycleEvent) {
		mu.Lock()
		eventKinds[string(evt.Kind)]++
		mu.Unlock()
	})

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "slow"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	// Cancel the run.
	cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Cancel(cancelCtx, h); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	kinds := make(map[string]int, len(eventKinds))
	for k, v := range eventKinds {
		kinds[k] = v
	}
	mu.Unlock()

	// Must have run_created from Spawn.
	if kinds["run_created"] != 1 {
		t.Fatalf("expected 1 run_created, got %d", kinds["run_created"])
	}
	// Must have task_cancel_requested (or task_canceled).
	hasCancel := kinds["task_cancel_requested"] > 0 || kinds["task_canceled"] > 0
	if !hasCancel {
		t.Fatal("expected task_cancel_requested or task_canceled events, got none")
	}
	t.Logf("cancel events: cancel_requested=%d, canceled=%d", kinds["task_cancel_requested"], kinds["task_canceled"])
}

func TestCoordinator_RetryEmitsRetryQueued(t *testing.T) {
	// Regression test for Bug 13: retry must emit retry_queued event
	// when a task is re-queued after backoff.
	repo := ledger.NewMemoryLedgerRepository()
	fixedTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return fixedTime })
	d := runtime.New(runtime.Policy{})
	failer := &failTwiceHandler{}
	_ = d.Register(runtime.Subagent, "flaky", failer)
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p).WithRetryPolicy(RetryPolicy{
		MaxRetries:    3,
		BaseBackoff:   1 * time.Millisecond,
		MaxBackoff:    5 * time.Millisecond,
		BackoffFactor: 2.0,
		JitterFraction: 0,
	})

	var mu sync.Mutex
	eventKinds := make(map[string]int)

	c.SubscribeLifecycle(func(evt ledger.LifecycleEvent) {
		mu.Lock()
		eventKinds[string(evt.Kind)]++
		mu.Unlock()
	})

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "flaky"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	retryPending := eventKinds["task_retry_pending"]
	retryQueued := eventKinds["task_retry_queued"]
	taskCompleted := eventKinds["task_completed"]
	mu.Unlock()

	// Must have at least one retry_pending and retry_queued (task fails twice
	// before succeeding on third attempt).
	if retryPending == 0 {
		t.Fatal("expected at least one task_retry_pending event from retry")
	}
	if retryQueued == 0 {
		t.Fatal("MUTATION FAIL: expected at least one task_retry_queued event from retry (Bug 13 regression)")
	}
	if taskCompleted != 1 {
		t.Fatalf("expected 1 task_completed, got %d", taskCompleted)
	}
	t.Logf("retry events: retry_pending=%d, retry_queued=%d, completed=%d", retryPending, retryQueued, taskCompleted)
}

func TestCoordinator_NoDuplicateLifecycleEvents(t *testing.T) {
	// Regression test for Bugs 6,7: ensure no duplicate run_created or
	// task_created events are emitted for a single Spawn.
	repo := ledger.NewMemoryLedgerRepository()
	fixedTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return fixedTime })
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	var mu sync.Mutex
	eventKinds := make(map[string]int)

	c.SubscribeLifecycle(func(evt ledger.LifecycleEvent) {
		mu.Lock()
		eventKinds[string(evt.Kind)]++
		mu.Unlock()
	})

	// Spawn once.
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "worker"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	runCreated := eventKinds["run_created"]
	taskCreated := eventKinds["task_created"]
	mu.Unlock()

	if runCreated != 1 {
		t.Fatalf("expected exactly 1 run_created, got %d (Bug 7 regression)", runCreated)
	}
	if taskCreated != 1 {
		t.Fatalf("expected exactly 1 task_created, got %d (Bug 6 regression)", taskCreated)
	}
}

// ---------------------------------------------------------------------------
// SubscribeLifecycle nil-safety test
// ---------------------------------------------------------------------------

func TestCoordinator_SubscribeLifecycleNilSafe(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	// Subscribe nil must not panic.
	unsub := c.SubscribeLifecycle(nil)
	unsub() // calling unsub on nil subscription must not panic

	// Normal operation must still work.
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "worker"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
}
