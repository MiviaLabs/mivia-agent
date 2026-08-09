package coordinator

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func TestCoordinator_SubscribeLifecycleUnsubscribe(t *testing.T) {
	// MUTATION PROOF (Bug 1): Unsubscribe must prevent further callbacks.
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1})
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
	p := subagents.New(d, subagents.Policy{Workers: 1})
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
	p := subagents.New(d, subagents.Policy{Workers: 1})
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

func TestCoordinator_LifecycleEventsCarrySessionID(t *testing.T) {
	// Cross-surface correlation: every task lifecycle event emitted for a
	// dispatched task must carry the task's SessionID so workflow-run ->
	// coordinator-run -> bus correlation is possible. run_created has no
	// single task in hand and must stay empty.
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	const sessID = "sess-abc-123"
	var mu sync.Mutex
	taskEvents := make(map[string]ledger.LifecycleEvent) // kind -> first event of that kind
	runCreatedSession := ""

	c.SubscribeLifecycle(func(evt ledger.LifecycleEvent) {
		mu.Lock()
		defer mu.Unlock()
		if evt.Kind == "run_created" {
			runCreatedSession = evt.SessionID
		}
		if _, seen := taskEvents[evt.Kind]; !seen {
			taskEvents[evt.Kind] = evt
		}
	})

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "worker", SessionID: sessID},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	events := make(map[string]ledger.LifecycleEvent, len(taskEvents))
	for k, v := range taskEvents {
		events[k] = v
	}
	runCreated := runCreatedSession
	mu.Unlock()

	if runCreated != "" {
		t.Fatalf("run_created event SessionID = %q, want empty (no single task in hand)", runCreated)
	}

	for _, kind := range []string{"task_created", "task_running", "task_completed"} {
		evt, ok := events[kind]
		if !ok {
			t.Fatalf("missing %s lifecycle event", kind)
		}
		if evt.SessionID != sessID {
			t.Errorf("%s event SessionID = %q, want %q (task %q)", kind, evt.SessionID, sessID, evt.TaskID)
		}
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
	p := subagents.New(d, subagents.Policy{Workers: 1})
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
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p).WithRetryPolicy(RetryPolicy{
		MaxRetries:     3,
		BaseBackoff:    1 * time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
		BackoffFactor:  2.0,
		JitterFraction: 0,
	})

	const sessID = "sess-retry-queued"
	var mu sync.Mutex
	eventKinds := make(map[string]int)
	queuedSessionID := ""

	c.SubscribeLifecycle(func(evt ledger.LifecycleEvent) {
		mu.Lock()
		defer mu.Unlock()
		eventKinds[string(evt.Kind)]++
		if evt.Kind == "task_retry_queued" && queuedSessionID == "" {
			queuedSessionID = evt.SessionID
		}
	})

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "flaky", SessionID: sessID},
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
	gotSession := queuedSessionID
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
	if gotSession != sessID {
		t.Fatalf("task_retry_queued SessionID = %q, want %q (the re-queued task's session must be carried)", gotSession, sessID)
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
	p := subagents.New(d, subagents.Policy{Workers: 1})
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
	p := subagents.New(d, subagents.Policy{Workers: 1})
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
