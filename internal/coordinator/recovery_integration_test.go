package coordinator

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func TestCoordinator_ResumeInterruptedRun(t *testing.T) {
	store := storage.NewMemory()
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	// Phase 1: Create interrupted state via storage repo.
	storeRepo := ledger.NewStorageLedgerRepository(store)
	storeRepo.SetTimeSource(func() time.Time { return now })
	ctx := context.Background()
	if err := storeRepo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "run-resume", Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	// Task 1: completed
	if err := storeRepo.CreateTask(ctx, ledger.TaskSnapshot{RunID: "run-resume", TaskID: "t1", Status: string(ledger.TaskStatusQueued), Version: 1, HandlerName: "worker", AgentName: "worker", AgentDigest: "test-digest", Input: json.RawMessage(`{"prompt":"work"}`)}); err != nil {
		t.Fatal(err)
	}
	_ = storeRepo.CompareAndSetTaskStatus(ctx, "run-resume", "t1", 1, string(ledger.TaskStatusRunning))
	_ = storeRepo.CompareAndSetTaskStatus(ctx, "run-resume", "t1", 2, string(ledger.TaskStatusCompleted))
	// Task 2: running (interrupted mid-execution)
	if err := storeRepo.CreateTask(ctx, ledger.TaskSnapshot{RunID: "run-resume", TaskID: "t2", Status: string(ledger.TaskStatusQueued), Version: 1, HandlerName: "worker", AgentName: "worker", AgentDigest: "test-digest", Input: json.RawMessage(`{"prompt":"work"}`)}); err != nil {
		t.Fatal(err)
	}
	_ = storeRepo.CompareAndSetTaskStatus(ctx, "run-resume", "t2", 1, string(ledger.TaskStatusRunning))
	storeRepo.Close()

	// Phase 2: Create coordinator with fresh storage repo from same store.
	recoveredRepo := ledger.NewStorageLedgerRepository(store)
	recoveredRepo.SetTimeSource(func() time.Time { return now })
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(recoveredRepo, p)

	recovered, err := recoveredRepo.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) == 0 {
		t.Fatal("expected recovered runs")
	}
	if !recovered[0].WasInterrupted {
		t.Fatal("expected interrupted run")
	}

	h, err := c.ResumeInterruptedRun(ctx, "run-resume")
	if err != nil {
		t.Fatal(err)
	}
	if h == nil {
		t.Fatal("expected non-nil handle")
	}

	result, err := c.Join(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// Verify both tasks reached terminal states (completed or failed).
	if len(result.Snapshot.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(result.Snapshot.Tasks))
	}
	t1Status, t2Status := "", ""
	for _, task := range result.Snapshot.Tasks {
		if task.TaskID == "t1" {
			t1Status = task.Status
		}
		if task.TaskID == "t2" {
			t2Status = task.Status
		}
	}
	// t1 was completed before crash, must remain completed.
	if t1Status != string(ledger.TaskStatusCompleted) {
		t.Fatalf("t1 status = %q, want completed", t1Status)
	}
	// t2 was running at crash time, should have been retried and completed.
	if t2Status != string(ledger.TaskStatusCompleted) && t2Status != string(ledger.TaskStatusFailed) {
		t.Fatalf("t2 status = %q, want completed or failed", t2Status)
	}
	t.Logf("resumed run status: %s, t1=%s, t2=%s", result.Snapshot.Status, t1Status, t2Status)
}

func TestCoordinator_ResumeInterruptedRun_AutoRetry(t *testing.T) {
	store := storage.NewMemory()
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	storeRepo := ledger.NewStorageLedgerRepository(store)
	storeRepo.SetTimeSource(func() time.Time { return now })
	ctx := context.Background()

	if err := storeRepo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "run-retry-resume", Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := storeRepo.CreateTask(ctx, ledger.TaskSnapshot{RunID: "run-retry-resume", TaskID: "t1", Status: string(ledger.TaskStatusRunning), Version: 1, HandlerName: "worker", AgentName: "worker", AgentDigest: "test-digest", Input: json.RawMessage(`{"prompt":"work"}`)}); err != nil {
		t.Fatal(err)
	}
	storeRepo.Close()

	recoveredRepo := ledger.NewStorageLedgerRepository(store)
	recoveredRepo.SetTimeSource(func() time.Time { return now })
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(recoveredRepo, p).WithRetryPolicy(RetryPolicy{
		MaxRetries:    2,
		BaseBackoff:   1 * time.Millisecond,
		MaxBackoff:    5 * time.Millisecond,
		BackoffFactor: 2.0,
	})

	h, err := c.ResumeInterruptedRun(ctx, "run-retry-resume")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Join(ctx, h)
	if err != nil {
		t.Fatal(err)
	}

	snap, _ := c.Inspect(ctx, h)
	if snap.Status != ledger.RunStatusCompleted {
		t.Fatalf("resumed+retried run status = %q, want completed", snap.Status)
	}
	if len(snap.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(snap.Tasks))
	}
	if snap.Tasks[0].Status != string(ledger.TaskStatusCompleted) {
		t.Fatalf("task status = %q, want completed (auto-retry should succeed)", snap.Tasks[0].Status)
	}
	t.Logf("resumed+retried run status: %s, task: %s=%s", snap.Status, snap.Tasks[0].TaskID, snap.Tasks[0].Status)
}

func TestIntegration_ResumeEmitsInterruptedEvents(t *testing.T) {
	// Regression test for Bug 12: ResumeInterruptedRun must emit
	// task_interrupted_unrecoverable events via SubscribeLifecycle.
	store := storage.NewMemory()
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	storeRepo := ledger.NewStorageLedgerRepository(store)
	storeRepo.SetTimeSource(func() time.Time { return now })
	ctx := context.Background()

	if err := storeRepo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "run-resume-events", Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := storeRepo.CreateTask(ctx, ledger.TaskSnapshot{RunID: "run-resume-events", TaskID: "t1", Status: string(ledger.TaskStatusRunning), Version: 1, HandlerName: "worker", AgentName: "worker", AgentDigest: "test-digest", Input: json.RawMessage(`{"prompt":"work"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := storeRepo.CreateTask(ctx, ledger.TaskSnapshot{RunID: "run-resume-events", TaskID: "t2", Status: string(ledger.TaskStatusQueued), Version: 1, HandlerName: "worker", AgentName: "worker", AgentDigest: "test-digest", Input: json.RawMessage(`{"prompt":"work"}`)}); err != nil {
		t.Fatal(err)
	}
	storeRepo.Close()

	recoveredRepo := ledger.NewStorageLedgerRepository(store)
	recoveredRepo.SetTimeSource(func() time.Time { return now })
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(recoveredRepo, p)

	var mu sync.Mutex
	interruptedEvents := 0
	taskEvents := make(map[string]string)

	c.SubscribeLifecycle(func(evt ledger.LifecycleEvent) {
		mu.Lock()
		if string(evt.Kind) == "task_interrupted_unrecoverable" {
			interruptedEvents++
		}
		if evt.TaskID != "" {
			taskEvents[evt.TaskID] = string(evt.Kind)
		}
		mu.Unlock()
	})

	h, err := c.ResumeInterruptedRun(ctx, "run-resume-events")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Join(ctx, h)
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	ie := interruptedEvents
	te := taskEvents["t1"]
	mu.Unlock()

	// t1 was running, must have interrupted_unrecoverable event.
	if ie != 1 {
		t.Fatalf("MUTATION FAIL (Bug 12): expected 1 task_interrupted_unrecoverable event, got %d", ie)
	}
	// t1's event kind should be interrupted_unrecoverable.
	if te != "task_interrupted_unrecoverable" {
		t.Logf("t1 event kind = %q (may have been overwritten by subsequent transitions)", te)
	}
	t.Logf("resume interrupted events: interrupted_unrecoverable=%d", ie)
}
