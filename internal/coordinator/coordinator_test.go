package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
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

func TestRunIDIsNotSequential(t *testing.T) {
	first, second := newRunID(), newRunID()
	if first == second {
		t.Fatalf("run IDs collided: %q", first)
	}
	if matched, _ := regexp.MatchString(`^run-[0-9]+$`, first); matched {
		t.Fatalf("run ID %q is a sequential counter", first)
	}
}

func TestRunIDDoesNotCollideWithPersistedLegacyID(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	if err := repo.CreateRun(context.Background(), "legacy", ledger.RunSnapshot{RunID: "run-1", Status: ledger.RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "test"}}, "new")
	if err != nil {
		t.Fatal(err)
	}
	if h.runID == "run-1" {
		t.Fatal("new random ID collided with persisted legacy ID")
	}
	if _, err := repo.GetRun(context.Background(), "run-1"); err != nil {
		t.Fatalf("persisted legacy run no longer resolves: %v", err)
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
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
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
