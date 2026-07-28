package ledger

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestDisplayNameGenerator_Uniqueness(t *testing.T) {
	g := NewDisplayNameGenerator()
	const n = 100
	names := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		name := g.Generate("agent")
		if names[name] {
			t.Fatalf("duplicate name: %s", name)
		}
		names[name] = true
	}
}

func TestDisplayNameGenerator_Concurrent(t *testing.T) {
	g := NewDisplayNameGenerator()
	const workers = 10
	const perWorker = 100
	var wg sync.WaitGroup
	mu := sync.Mutex{}
	names := map[string]bool{}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				name := g.Generate("agent")
				mu.Lock()
				if names[name] {
					t.Errorf("duplicate concurrent name: %s", name)
				}
				names[name] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(names) != workers*perWorker {
		t.Errorf("expected %d unique names, got %d", workers*perWorker, len(names))
	}
}

func TestDisplayNameGenerator_Reserve(t *testing.T) {
	g := NewDisplayNameGenerator()
	g.Reserve("agent-1")
	name := g.Generate("agent")
	if name == "agent-1" {
		t.Fatal("generated reserved name")
	}
}

func TestRunSnapshot_Clone(t *testing.T) {
	now := time.Now()
	completedAt := now.Add(time.Hour)
	snap := RunSnapshot{
		RunID:       "r1",
		DisplayName: "test-run",
		Status:      RunStatusRunning,
		CreatedAt:   now,
		CompletedAt: &completedAt,
		Labels:      map[string]string{"key": "val"},
		Tasks: []TaskSnapshot{
			{
				RunID:     "r1",
				TaskID:    "t1",
				Status:    string(TaskStatusRunning),
				Version:   1,
				DependsOn: []string{},
			},
		},
	}

	clone := snap.Clone()
	// Mutate original
	snap.Status = RunStatusCompleted
	snap.Labels["key"] = "changed"
	snap.Tasks[0].Status = string(TaskStatusCompleted)

	if clone.Status != RunStatusRunning {
		t.Fatal("clone was mutated")
	}
	if clone.Labels["key"] != "val" {
		t.Fatal("clone labels were mutated")
	}
	if clone.Tasks[0].Status != string(TaskStatusRunning) {
		t.Fatal("clone task was mutated")
	}
}

func TestRunSnapshot_ClonePreservesNil(t *testing.T) {
	snap := RunSnapshot{
		RunID: "r1",
	}
	clone := snap.Clone()
	if clone.Labels != nil {
		t.Fatal("clone should preserve nil Labels")
	}
	if clone.Tasks != nil {
		t.Fatal("clone should preserve nil Tasks")
	}
}

func TestTaskSnapshot_ClonePreservesNil(t *testing.T) {
	snap := TaskSnapshot{
		RunID:  "r1",
		TaskID: "t1",
	}
	clone := snap.Clone()
	if clone.Attempts != nil {
		t.Fatal("clone should preserve nil Attempts")
	}
	if clone.DependsOn != nil {
		t.Fatal("clone should preserve nil DependsOn")
	}
}

func TestLifecycleEvent_ClonePreservesNilPayload(t *testing.T) {
	evt := LifecycleEvent{
		ID:    "e1",
		RunID: "r1",
		// Payload is nil
	}
	clone := evt.Clone()
	if clone.Payload != nil {
		t.Fatal("clone should preserve nil Payload")
	}
}

func TestTaskSnapshot_Clone(t *testing.T) {
	now := time.Now()
	snap := TaskSnapshot{
		RunID:        "r1",
		TaskID:       "t1",
		ParentTaskID: "",
		DisplayName:  "test-task",
		Status:       string(TaskStatusRunning),
		DependsOn:    []string{"dep1"},
		CreatedAt:    now,
		Version:      1,
	}
	clone := snap.Clone()
	snap.Status = string(TaskStatusCompleted)
	snap.DependsOn[0] = "changed"

	if clone.Status != string(TaskStatusRunning) {
		t.Fatal("clone was mutated")
	}
	if clone.DependsOn[0] != "dep1" {
		t.Fatal("clone depends_on was mutated")
	}
}

func TestLifecycleEvent_Clone(t *testing.T) {
	evt := LifecycleEvent{
		ID:      "e1",
		RunID:   "r1",
		Kind:    "task_created",
		Payload: []byte("data"),
	}
	clone := evt.Clone()
	evt.Payload[0] = 'X'

	if string(clone.Payload) != "data" {
		t.Fatal("clone payload was mutated")
	}
}

func TestValidTaskTransitions(t *testing.T) {
	tests := []struct {
		from, to string
		valid    bool
	}{
		{string(TaskStatusQueued), string(TaskStatusRunning), true},
		{string(TaskStatusQueued), string(TaskStatusCompleted), false},
		{string(TaskStatusQueued), string(TaskStatusCancelRequested), true},
		{string(TaskStatusQueued), string(TaskStatusCanceled), true},
		{string(TaskStatusRunning), string(TaskStatusCompleted), true},
		{string(TaskStatusRunning), string(TaskStatusFailed), true},
		{string(TaskStatusRunning), string(TaskStatusTimedOut), true},
		{string(TaskStatusRunning), string(TaskStatusCancelRequested), true},
		{string(TaskStatusRunning), string(TaskStatusCanceled), true},
		{string(TaskStatusRunning), string(TaskStatusBlocked), true},
		{string(TaskStatusRunning), string(TaskStatusQueued), false},
		{string(TaskStatusCancelRequested), string(TaskStatusCanceled), true},
		{string(TaskStatusCancelRequested), string(TaskStatusCompleted), false},
		{string(TaskStatusFailed), string(TaskStatusRetryPending), true},
		{string(TaskStatusFailed), string(TaskStatusBlocked), true},
		{string(TaskStatusTimedOut), string(TaskStatusRetryPending), true},
		{string(TaskStatusTimedOut), string(TaskStatusBlocked), true},
		{string(TaskStatusRetryPending), string(TaskStatusQueued), true},
		{string(TaskStatusCompleted), string(TaskStatusQueued), false},
		{string(TaskStatusCanceled), string(TaskStatusQueued), false},
		{string(TaskStatusBlocked), string(TaskStatusQueued), false},
	}
	for _, tt := range tests {
		got := ValidTaskTransition(tt.from, tt.to)
		if got != tt.valid {
			t.Errorf("ValidTaskTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.valid)
		}
	}
}

func TestValidRunTransitions(t *testing.T) {
	tests := []struct {
		from, to RunStatus
		valid    bool
	}{
		{RunStatusCreated, RunStatusQueued, true},
		{RunStatusCreated, RunStatusCanceled, true},
		{RunStatusCreated, RunStatusRunning, false},
		{RunStatusQueued, RunStatusRunning, true},
		{RunStatusQueued, RunStatusCanceled, true},
		{RunStatusQueued, RunStatusCompleted, false},
		{RunStatusRunning, RunStatusCompleted, true},
		{RunStatusRunning, RunStatusFailed, true},
		{RunStatusRunning, RunStatusCanceled, true},
		{RunStatusCompleted, RunStatusQueued, false},
		{RunStatusFailed, RunStatusRunning, false},
		{RunStatusCanceled, RunStatusCreated, false},
	}
	for _, tt := range tests {
		got := ValidRunTransitions(tt.from, tt.to)
		if got != tt.valid {
			t.Errorf("ValidRunTransitions(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.valid)
		}
	}
}

// ---- MemoryLedgerRepository tests ----

func TestMemory_CreateAndGetRun(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryLedgerRepository()
	fixedTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return fixedTime })

	snap := RunSnapshot{
		RunID:       "r1",
		DisplayName: "test-run",
		Status:      RunStatusCreated,
		Labels:      map[string]string{"env": "test"},
	}

	if err := repo.CreateRun(ctx, "", snap); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetRun(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID != "r1" || got.DisplayName != "test-run" || got.Status != RunStatusCreated {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("created time not set")
	}

	// Duplicate create should fail
	if err := repo.CreateRun(ctx, "", snap); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
}

func TestMemory_GetRunNotFound(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryLedgerRepository()
	_, err := repo.GetRun(ctx, "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMemory_ListRuns(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryLedgerRepository()
	fixedTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return fixedTime })

	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("r%d", i+1)
		_ = repo.CreateRun(ctx, "", RunSnapshot{
			RunID:  id,
			Status: RunStatusCreated,
		})
	}

	runs, err := repo.ListRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(runs))
	}

	// Filter by status
	filtered, err := repo.ListRuns(ctx, RunStatusCreated)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 3 {
		t.Fatalf("expected 3 created runs, got %d", len(filtered))
	}

	noMatch, err := repo.ListRuns(ctx, RunStatusCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if len(noMatch) != 0 {
		t.Fatalf("expected 0 completed runs, got %d", len(noMatch))
	}
}

func TestMemory_CreateTask(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryLedgerRepository()
	now := time.Now()

	_ = repo.CreateRun(ctx, "", RunSnapshot{RunID: "r1", Status: RunStatusCreated})

	snap := TaskSnapshot{
		RunID:     "r1",
		TaskID:    "t1",
		Status:    string(TaskStatusQueued),
		CreatedAt: now,
		Version:   1,
	}
	if err := repo.CreateTask(ctx, snap); err != nil {
		t.Fatal(err)
	}

	// Duplicate task should fail
	if err := repo.CreateTask(ctx, snap); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}

	// Task on nonexistent run should fail
	snap2 := TaskSnapshot{RunID: "nonexistent", TaskID: "t2", Status: string(TaskStatusQueued), Version: 1}
	if err := repo.CreateTask(ctx, snap2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMemory_GetTask(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryLedgerRepository()
	_ = repo.CreateRun(ctx, "", RunSnapshot{RunID: "r1", Status: RunStatusCreated})
	_ = repo.CreateTask(ctx, TaskSnapshot{RunID: "r1", TaskID: "t1", Status: string(TaskStatusQueued), Version: 1})

	got, err := repo.GetTask(ctx, "r1", "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskID != "t1" || got.Status != string(TaskStatusQueued) || got.Version != 1 {
		t.Fatalf("unexpected task: %+v", got)
	}

	// Not found
	_, err = repo.GetTask(ctx, "r1", "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMemory_ListTasks(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryLedgerRepository()
	_ = repo.CreateRun(ctx, "", RunSnapshot{RunID: "r1", Status: RunStatusCreated})
	_ = repo.CreateTask(ctx, TaskSnapshot{RunID: "r1", TaskID: "t1", Status: string(TaskStatusQueued), Version: 1})
	_ = repo.CreateTask(ctx, TaskSnapshot{RunID: "r1", TaskID: "t2", Status: string(TaskStatusQueued), Version: 1})

	tasks, err := repo.ListTasks(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
}

func TestMemory_AppendAndListEvents(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryLedgerRepository()
	fixedTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return fixedTime })
	_ = repo.CreateRun(ctx, "", RunSnapshot{RunID: "r1", Status: RunStatusCreated})

	evt1 := LifecycleEvent{ID: "e1", RunID: "r1", Kind: "task_created", TaskID: "t1"}
	evt2 := LifecycleEvent{ID: "e2", RunID: "r1", Kind: "task_completed", TaskID: "t1"}

	if err := repo.AppendEvent(ctx, evt1); err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendEvent(ctx, evt2); err != nil {
		t.Fatal(err)
	}

	// Duplicate event
	if err := repo.AppendEvent(ctx, evt1); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}

	events, err := repo.ListEvents(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Sequence != 1 || events[1].Sequence != 2 {
		t.Fatalf("unexpected sequences: %d, %d", events[0].Sequence, events[1].Sequence)
	}
}

func TestMemory_CompareAndSetTaskStatus(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryLedgerRepository()
	fixedTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return fixedTime })
	_ = repo.CreateRun(ctx, "", RunSnapshot{RunID: "r1", Status: RunStatusCreated})
	_ = repo.CreateTask(ctx, TaskSnapshot{RunID: "r1", TaskID: "t1", Status: string(TaskStatusQueued), Version: 1})

	// Valid transition: queued -> running
	if err := repo.CompareAndSetTaskStatus(ctx, "r1", "t1", 1, string(TaskStatusRunning)); err != nil {
		t.Fatal(err)
	}
	task, _ := repo.GetTask(ctx, "r1", "t1")
	if task.Version != 2 {
		t.Fatalf("expected version 2, got %d", task.Version)
	}

	// Stale version (CAS protection)
	if err := repo.CompareAndSetTaskStatus(ctx, "r1", "t1", 1, string(TaskStatusCompleted)); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for stale version, got %v", err)
	}

	// Valid transition: running -> completed
	if err := repo.CompareAndSetTaskStatus(ctx, "r1", "t1", 2, string(TaskStatusCompleted)); err != nil {
		t.Fatal(err)
	}

	// Invalid transition: completed -> running
	if err := repo.CompareAndSetTaskStatus(ctx, "r1", "t1", 3, string(TaskStatusRunning)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}

	// Not found
	if err := repo.CompareAndSetTaskStatus(ctx, "nonexistent", "t1", 1, string(TaskStatusRunning)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMemory_CASStaleCompletionProtection(t *testing.T) {
	// MUTATION PROOF 1: CAS version guard — a stale worker that tries to
	// complete a task with an outdated version must be rejected.
	ctx := context.Background()
	repo := NewMemoryLedgerRepository()
	fixedTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return fixedTime })
	_ = repo.CreateRun(ctx, "", RunSnapshot{RunID: "r1", Status: RunStatusCreated})
	_ = repo.CreateTask(ctx, TaskSnapshot{RunID: "r1", TaskID: "t1", Status: string(TaskStatusQueued), Version: 1})

	// Advance to running (v1 -> v2)
	if err := repo.CompareAndSetTaskStatus(ctx, "r1", "t1", 1, string(TaskStatusRunning)); err != nil {
		t.Fatal(err)
	}

	// Advance to completed (v2 -> v3) — simulates real worker finishing
	if err := repo.CompareAndSetTaskStatus(ctx, "r1", "t1", 2, string(TaskStatusCompleted)); err != nil {
		t.Fatal(err)
	}

	// Stale worker tries to complete with old version 1 -> should fail
	if err := repo.CompareAndSetTaskStatus(ctx, "r1", "t1", 1, string(TaskStatusFailed)); !errors.Is(err, ErrConflict) {
		t.Fatalf("MUTATION FAIL: stale worker with version 1 was accepted, got %v", err)
	}

	// Stale worker tries to complete with old version 2 -> should fail
	if err := repo.CompareAndSetTaskStatus(ctx, "r1", "t1", 2, string(TaskStatusFailed)); !errors.Is(err, ErrConflict) {
		t.Fatalf("MUTATION FAIL: stale worker with version 2 was accepted, got %v", err)
	}

	// Verify task is still completed
	task, err := repo.GetTask(ctx, "r1", "t1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != string(TaskStatusCompleted) {
		t.Fatalf("task status was overwritten to %q by stale worker", task.Status)
	}
}

func TestMemory_CancellationOrdering(t *testing.T) {
	// MUTATION PROOF 2: cancellation ordering — cancel_requested is observable
	// and distinct from terminal canceled.
	ctx := context.Background()
	repo := NewMemoryLedgerRepository()
	fixedTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return fixedTime })
	_ = repo.CreateRun(ctx, "", RunSnapshot{RunID: "r1", Status: RunStatusCreated})
	_ = repo.CreateTask(ctx, TaskSnapshot{RunID: "r1", TaskID: "t1", Status: string(TaskStatusQueued), Version: 1})
	_ = repo.CreateTask(ctx, TaskSnapshot{RunID: "r1", TaskID: "t2", Status: string(TaskStatusRunning), Version: 2})

	// Set cancel_requested on queued task
	if err := repo.CompareAndSetTaskStatus(ctx, "r1", "t1", 1, string(TaskStatusCancelRequested)); err != nil {
		t.Fatal(err)
	}
	t1, _ := repo.GetTask(ctx, "r1", "t1")
	if t1.Status != string(TaskStatusCancelRequested) {
		t.Fatalf("expected cancel_requested, got %q", t1.Status)
	}
	if t1.CompletedAt != nil {
		t.Fatal("cancel_requested should not set CompletedAt")
	}

	// Now transition to terminal canceled
	if err := repo.CompareAndSetTaskStatus(ctx, "r1", "t1", 2, string(TaskStatusCanceled)); err != nil {
		t.Fatal(err)
	}
	t1, _ = repo.GetTask(ctx, "r1", "t1")
	if t1.Status != string(TaskStatusCanceled) {
		t.Fatalf("expected canceled, got %q", t1.Status)
	}
	if t1.CompletedAt == nil {
		t.Fatal("canceled should set CompletedAt")
	}
}

func TestMemory_SetTaskOutput(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryLedgerRepository()
	_ = repo.CreateRun(ctx, "", RunSnapshot{RunID: "r1", Status: RunStatusCreated})
	_ = repo.CreateTask(ctx, TaskSnapshot{RunID: "r1", TaskID: "t1", Status: string(TaskStatusQueued), Version: 1})

	if err := repo.SetTaskOutput(ctx, "r1", "t1", "output:42", ""); err != nil {
		t.Fatal(err)
	}
	task, _ := repo.GetTask(ctx, "r1", "t1")
	if task.OutputRef != "output:42" {
		t.Fatalf("expected output:42, got %q", task.OutputRef)
	}
}

func TestMemory_CloseAndDeleteRun(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryLedgerRepository()
	_ = repo.CreateRun(ctx, "", RunSnapshot{RunID: "r1", Status: RunStatusCreated})

	if err := repo.CloseRun(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	// Double close should fail
	if err := repo.CloseRun(ctx, "r1"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}

	// Closed run: tasks should fail with ErrClosed
	if err := repo.CreateTask(ctx, TaskSnapshot{RunID: "r1", TaskID: "t2", Status: string(TaskStatusQueued), Version: 1}); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}

	// Delete existing run
	if err := repo.DeleteRun(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetRun(ctx, "r1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}

	// Delete nonexistent
	if err := repo.DeleteRun(ctx, "nonexistent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMemory_ConcurrentSafe(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryLedgerRepository()
	fixedTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return fixedTime })

	_ = repo.CreateRun(ctx, "", RunSnapshot{RunID: "r1", Status: RunStatusCreated})
	for i := 0; i < 10; i++ {
		tid := fmt.Sprintf("t%d", i+1)
		_ = repo.CreateTask(ctx, TaskSnapshot{RunID: "r1", TaskID: tid, Status: string(TaskStatusQueued), Version: 1})
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tid := fmt.Sprintf("t%d", i+1)
			// CAS loop: try to advance from queued -> running -> completed
			task, err := repo.GetTask(ctx, "r1", tid)
			if err != nil {
				return
			}
			_ = repo.CompareAndSetTaskStatus(ctx, "r1", tid, task.Version, string(TaskStatusRunning))
		}(i)
	}
	wg.Wait()

	// Verify
	tasks, _ := repo.ListTasks(ctx, "r1")
	for _, tk := range tasks {
		if tk.Status != string(TaskStatusRunning) && tk.Status != string(TaskStatusQueued) {
			t.Errorf("unexpected task status %q for %s", tk.Status, tk.TaskID)
		}
	}
}
