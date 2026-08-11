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

// TestBlockedTaskEmitsSingleTaskBlockedEvent pins the exactly-once contract for
// the blocked terminal announce (defect-taxonomy DC-9/DC-12: duplicated durable
// record). collectReady (dag.go) transitions a task whose dependency failed
// queued -> blocked via transitionTask, which appends AND emits task_blocked at
// block time; recordRunResults' recordTaskResult then re-reads the task,
// tryTaskStatusCAS's already-at-status skip returns true, and the code used to
// append+emit a SECOND durable task_blocked row. This test asserts the total is
// exactly one — once on the subscriber channel and once in the durable ledger —
// on the concrete inputs (parent fails, child depends on parent).
func TestBlockedTaskEmitsSingleTaskBlockedEvent(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "fail", staticHandler{err: errors.New("intentional failure")}); err != nil {
		t.Fatal(err)
	}
	if err := d.Register(runtime.Subagent, "ok", staticHandler{out: json.RawMessage(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))

	var mu sync.Mutex
	subscriberBlocked := 0
	c.SubscribeLifecycle(func(evt ledger.LifecycleEvent) {
		if evt.Kind != "task_blocked" || evt.TaskID != "child" {
			return
		}
		mu.Lock()
		subscriberBlocked++
		mu.Unlock()
	})

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "parent", Name: "fail"},
		{ID: "child", Name: "ok", DependsOn: []string{"parent"}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}

	// Prove the DAG actually produced the blocked outcome (the test exercises
	// the blocked path, not a silently non-blocked graph).
	childBlocked := false
	for _, r := range result.Results {
		if r.TaskID == "child" && r.Status == "blocked" {
			childBlocked = true
		}
	}
	if !childBlocked {
		t.Fatal("child task did not reach blocked status; the blocked announce path was not exercised")
	}

	mu.Lock()
	gotSubscriber := subscriberBlocked
	mu.Unlock()

	events, err := repo.ListEvents(context.Background(), h.runID)
	if err != nil {
		t.Fatal(err)
	}
	gotDurable := 0
	for _, evt := range events {
		if evt.Kind == "task_blocked" && evt.TaskID == "child" {
			gotDurable++
		}
	}

	if gotSubscriber != 1 {
		t.Fatalf("subscriber saw %d task_blocked events for child, want exactly 1 (duplicated announce)", gotSubscriber)
	}
	if gotDurable != 1 {
		t.Fatalf("ledger recorded %d durable task_blocked rows for child, want exactly 1 (duplicated durable record)", gotDurable)
	}
}

// TestDirectFinalizeBlockedEmitsSingleTaskBlockedEvent is the negative path
// that pins the already-at-status guard never suppresses a legitimate first
// announce: a queued task finalized directly through recordRunResults with a
// blocked result (tryTaskStatusCAS performs the queued -> blocked CAS; no prior
// announce by collectReady) must still append exactly one task_blocked event —
// via the subscriber AND in the durable ledger — and the ledger task must land
// blocked. This passes on the pre-fix code (1 event) and must keep passing
// after the fix, which suppresses only the already-announced duplicate.
func TestDirectFinalizeBlockedEmitsSingleTaskBlockedEvent(t *testing.T) {
	ctx := context.Background()
	repo := ledger.NewMemoryLedgerRepository()
	c := newIdempotencyCoordinator(repo).(*coordinator)
	const runID = "direct-blocked-finalize"
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: runID, TaskID: "t1", Status: string(ledger.TaskStatusQueued), Version: 1, CreatedAt: now,
		Attempts: []ledger.AttemptSnapshot{{AttemptID: "attempt-1", TaskID: "t1", RunID: runID, AttemptNum: 1, StartedAt: now, Status: string(ledger.TaskStatusQueued)}},
	}); err != nil {
		t.Fatal(err)
	}
	h := c.newRunHandle(runID, "", map[string]string{"t1": "attempt-1"}, "", false)

	var mu sync.Mutex
	subscriberBlocked := 0
	c.SubscribeLifecycle(func(evt ledger.LifecycleEvent) {
		if evt.Kind != "task_blocked" || evt.TaskID != "t1" {
			return
		}
		mu.Lock()
		subscriberBlocked++
		mu.Unlock()
	})

	tasks := []subagents.Task{{ID: "t1", Name: "worker"}}
	results := []subagents.Result{{TaskID: "t1", Status: "blocked", Err: fmt.Errorf("dependency parent failed")}}
	if err := c.recordRunResults(h, tasks, results, nil); err != nil {
		t.Fatalf("recordRunResults: %v", err)
	}

	mu.Lock()
	gotSubscriber := subscriberBlocked
	mu.Unlock()

	events, err := repo.ListEvents(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	gotDurable := 0
	for _, evt := range events {
		if evt.Kind == "task_blocked" && evt.TaskID == "t1" {
			gotDurable++
		}
	}

	if gotSubscriber != 1 {
		t.Fatalf("subscriber saw %d task_blocked events, want exactly 1 (the direct finalize must announce the first blocked transition)", gotSubscriber)
	}
	if gotDurable != 1 {
		t.Fatalf("ledger recorded %d durable task_blocked rows, want exactly 1", gotDurable)
	}

	snap, err := repo.GetTask(ctx, runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusBlocked) {
		t.Fatalf("ledger task status = %q, want blocked", snap.Status)
	}
}
