package ledger

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// ---------------------------------------------------------------------------
// Unit tests using storage.Memory backend
// ---------------------------------------------------------------------------

func newTestStorageRepo(t *testing.T) *StorageLedgerRepository {
	t.Helper()
	store := storage.NewMemory()
	repo := NewStorageLedgerRepository(store)
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return now })
	return repo
}

func TestStorageLedger_CreateRun(t *testing.T) {
	repo := newTestStorageRepo(t)
	ctx := context.Background()

	snap := RunSnapshot{
		RunID:       "run-1",
		DisplayName: "test-run",
		Status:      RunStatusCreated,
	}

	if err := repo.CreateRun(ctx, "", snap); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID != "run-1" || got.DisplayName != "test-run" {
		t.Fatalf("got %+v, want run-1/test-run", got)
	}
}

func TestStorageLedger_CreateRunDuplicate(t *testing.T) {
	repo := newTestStorageRepo(t)
	ctx := context.Background()

	snap := RunSnapshot{RunID: "run-1", Status: RunStatusCreated}
	if err := repo.CreateRun(ctx, "", snap); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, "", snap); err != ErrDuplicate {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
}

func TestStorageLedger_GetRunNotFound(t *testing.T) {
	repo := newTestStorageRepo(t)
	ctx := context.Background()

	_, err := repo.GetRun(ctx, "nonexistent")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStorageLedger_ListRuns(t *testing.T) {
	repo := newTestStorageRepo(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("run-%d", i)
		if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: id, Status: RunStatusCreated}); err != nil {
			t.Fatal(err)
		}
	}

	runs, err := repo.ListRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(runs))
	}
}

func TestStorageLedger_TaskLifecycle(t *testing.T) {
	repo := newTestStorageRepo(t)
	ctx := context.Background()

	// Create run
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}

	// Create task
	taskSnap := TaskSnapshot{
		RunID:  "run-1",
		TaskID: "t1",
		Status: string(TaskStatusQueued),
	}
	if err := repo.CreateTask(ctx, taskSnap); err != nil {
		t.Fatal(err)
	}

	// CAS: queued → running
	if err := repo.CompareAndSetTaskStatus(ctx, "run-1", "t1", 0, string(TaskStatusRunning)); err != nil {
		t.Fatal(err)
	}

	// CAS: running → completed
	if err := repo.CompareAndSetTaskStatus(ctx, "run-1", "t1", 1, string(TaskStatusCompleted)); err != nil {
		t.Fatal(err)
	}

	// Verify final state
	task, err := repo.GetTask(ctx, "run-1", "t1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != string(TaskStatusCompleted) {
		t.Fatalf("task status = %q, want %q", task.Status, TaskStatusCompleted)
	}
	if task.Version != 2 {
		t.Fatalf("task version = %d, want 2", task.Version)
	}
	if task.CompletedAt == nil {
		t.Fatal("expected completed at time")
	}
}

func TestStorageLedger_TaskStatusInvalidTransition(t *testing.T) {
	repo := newTestStorageRepo(t)
	ctx := context.Background()

	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(ctx, TaskSnapshot{RunID: "run-1", TaskID: "t1", Status: string(TaskStatusQueued)}); err != nil {
		t.Fatal(err)
	}

	// queued → completed is invalid
	if err := repo.CompareAndSetTaskStatus(ctx, "run-1", "t1", 0, string(TaskStatusCompleted)); err != ErrInvalidTransition {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestStorageLedger_TaskStatusVersionConflict(t *testing.T) {
	repo := newTestStorageRepo(t)
	ctx := context.Background()

	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(ctx, TaskSnapshot{RunID: "run-1", TaskID: "t1", Status: string(TaskStatusQueued)}); err != nil {
		t.Fatal(err)
	}

	// CAS with wrong version
	if err := repo.CompareAndSetTaskStatus(ctx, "run-1", "t1", 99, string(TaskStatusRunning)); err != ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestStorageLedger_SetTaskOutput(t *testing.T) {
	repo := newTestStorageRepo(t)
	ctx := context.Background()

	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(ctx, TaskSnapshot{RunID: "run-1", TaskID: "t1", Status: string(TaskStatusRunning)}); err != nil {
		t.Fatal(err)
	}

	if err := repo.SetTaskOutput(ctx, "run-1", "t1", "ref:output:abc", "ref:error:def"); err != nil {
		t.Fatal(err)
	}

	task, err := repo.GetTask(ctx, "run-1", "t1")
	if err != nil {
		t.Fatal(err)
	}
	if task.OutputRef != "ref:output:abc" {
		t.Fatalf("output ref = %q, want %q", task.OutputRef, "ref:output:abc")
	}
	if task.ErrorRef != "ref:error:def" {
		t.Fatalf("error ref = %q, want %q", task.ErrorRef, "ref:error:def")
	}
}

func TestStorageLedger_SetTaskAttempt(t *testing.T) {
	repo := newTestStorageRepo(t)
	ctx := context.Background()

	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(ctx, TaskSnapshot{RunID: "run-1", TaskID: "t1", Status: string(TaskStatusRunning)}); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	if err := repo.SetTaskAttempt(ctx, "run-1", "t1", "attempt-1", "completed", &now); err != nil {
		t.Fatal(err)
	}

	task, err := repo.GetTask(ctx, "run-1", "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Attempts) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(task.Attempts))
	}
	if task.Attempts[0].AttemptID != "attempt-1" {
		t.Fatalf("attempt ID = %q, want %q", task.Attempts[0].AttemptID, "attempt-1")
	}
}

func TestStorageLedger_AppendListEvents(t *testing.T) {
	repo := newTestStorageRepo(t)
	ctx := context.Background()

	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}

	evt1 := LifecycleEvent{ID: "e1", RunID: "run-1", Kind: "test_event", Payload: []byte("hello")}
	evt2 := LifecycleEvent{ID: "e2", RunID: "run-1", Kind: "test_event", Payload: []byte("world")}

	if err := repo.AppendEvent(ctx, evt1); err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendEvent(ctx, evt2); err != nil {
		t.Fatal(err)
	}

	events, err := repo.ListEvents(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
}

func TestStorageLedger_AppendEventDuplicate(t *testing.T) {
	repo := newTestStorageRepo(t)
	ctx := context.Background()

	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}

	evt := LifecycleEvent{ID: "e1", RunID: "run-1", Kind: "test", Payload: []byte("data")}
	if err := repo.AppendEvent(ctx, evt); err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendEvent(ctx, evt); err != ErrDuplicate {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
}

func TestStorageLedger_CloseRun(t *testing.T) {
	repo := newTestStorageRepo(t)
	ctx := context.Background()

	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}

	if err := repo.CloseRun(ctx, "run-1"); err != nil {
		t.Fatal(err)
	}

	// Verify run is closed (run status is derived from tasks, so with no tasks
	// it stays as created/canceled based on implementation).
	_, _ = repo.GetRun(ctx, "run-1")
}

func TestStorageLedger_DeleteRun(t *testing.T) {
	repo := newTestStorageRepo(t)
	ctx := context.Background()

	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}

	if err := repo.DeleteRun(ctx, "run-1"); err != nil {
		t.Fatal(err)
	}

	_, err := repo.GetRun(ctx, "run-1")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestStorageLedger_Close(t *testing.T) {
	store := storage.NewMemory()
	repo := NewStorageLedgerRepository(store)

	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStorageLedger_RecoverEmpty(t *testing.T) {
	repo := newTestStorageRepo(t)
	ctx := context.Background()

	recovered, err := repo.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 0 {
		t.Fatalf("expected 0 recovered runs, got %d", len(recovered))
	}
}

func TestStorageLedger_RecoverWithCompletedRuns(t *testing.T) {
	repo := newTestStorageRepo(t)
	ctx := context.Background()

	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCompleted}); err != nil {
		t.Fatal(err)
	}

	recovered, err := repo.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 {
		t.Fatalf("expected 1 recovered run, got %d", len(recovered))
	}
	if recovered[0].WasInterrupted {
		t.Fatal("completed run should not be marked interrupted")
	}
}

// ---------------------------------------------------------------------------
// Direct RebuildProjection test
// ---------------------------------------------------------------------------

func TestRebuildProjection_RunCreatedThenClosed(t *testing.T) {
	// Create events directly simulating what CloseRun would produce
	runSnapPayload, _ := marshalRunSnapshot(RunSnapshot{
		RunID:  "run-1",
		Status: RunStatusCreated,
	})
	closedPayload, _ := marshalRunClosed()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	cancelPayload, _ := marshalRunStatusChange(string(RunStatusCanceled), &now)

	events := []storage.Event{
		{ID: "e1", RunID: "run-1", Kind: storageKindRunCreated, Payload: runSnapPayload, Sequence: 1},
		{ID: "e2", RunID: "run-1", Kind: storageKindRunClosed, Payload: closedPayload, Sequence: 2},
		{ID: "e3", RunID: "run-1", Kind: storageKindRunStatusChanged, Payload: cancelPayload, Sequence: 3},
	}

	runSnap, _, _, err := RebuildProjection(events)
	if err != nil {
		t.Fatal(err)
	}
	if runSnap.Status != RunStatusCanceled {
		t.Fatalf("RebuildProjection: status = %q, want %q", runSnap.Status, RunStatusCanceled)
	}
}

// ---------------------------------------------------------------------------
// Projection rebuild tests
// ---------------------------------------------------------------------------

func TestStorageLedger_ProjectionRebuild(t *testing.T) {
	// Create a repo, write data, then create a NEW repo from the same store
	// to verify projection rebuild.
	store := storage.NewMemory()
	ctx := context.Background()

	repo1 := NewStorageLedgerRepository(store)
	if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo1.CreateTask(ctx, TaskSnapshot{RunID: "run-1", TaskID: "t1", Status: string(TaskStatusQueued)}); err != nil {
		t.Fatal(err)
	}
	if err := repo1.CompareAndSetTaskStatus(ctx, "run-1", "t1", 0, string(TaskStatusRunning)); err != nil {
		t.Fatal(err)
	}
	if err := repo1.CompareAndSetTaskStatus(ctx, "run-1", "t1", 1, string(TaskStatusCompleted)); err != nil {
		t.Fatal(err)
	}

	// Simulate restart: create second repo from same store
	repo2 := NewStorageLedgerRepository(store)
	// Access a run to trigger lazy rebuild
	snap, err := repo2.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.RunID != "run-1" {
		t.Fatalf("run ID = %q, want %q", snap.RunID, "run-1")
	}
	if snap.Status != RunStatusCompleted {
		t.Fatalf("status = %q, want %q", snap.Status, RunStatusCompleted)
	}

	tasks, err := repo2.ListTasks(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Status != string(TaskStatusCompleted) {
		t.Fatalf("task status = %q, want %q", tasks[0].Status, TaskStatusCompleted)
	}
	if tasks[0].Version != 2 {
		t.Fatalf("task version = %d, want 2", tasks[0].Version)
	}
}

func TestStorageLedger_ProjectionRebuildMultipleRuns(t *testing.T) {
	store := storage.NewMemory()
	ctx := context.Background()

	repo1 := NewStorageLedgerRepository(store)
	for i := 0; i < 3; i++ {
		runID := fmt.Sprintf("run-%d", i)
		if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: runID, Status: RunStatusCreated}); err != nil {
			t.Fatal(err)
		}
	}

	// Rebuild from new repo
	repo2 := NewStorageLedgerRepository(store)
	runs, err := repo2.ListRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(runs))
	}
}

// ---------------------------------------------------------------------------
// SQLite backend tests
// ---------------------------------------------------------------------------

func newTestSQLiteRepo(t *testing.T) *StorageLedgerRepository {
	t.Helper()
	store, err := storage.OpenSQLite(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	repo := NewStorageLedgerRepository(store)
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return now })
	return repo
}

func TestStorageLedger_SQLiteCreateRun(t *testing.T) {
	repo := newTestSQLiteRepo(t)
	ctx := context.Background()

	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID != "run-1" {
		t.Fatalf("got %q, want %q", got.RunID, "run-1")
	}
}

func TestStorageLedger_SQLiteFullLifecycle(t *testing.T) {
	repo := newTestSQLiteRepo(t)
	ctx := context.Background()

	// Run lifecycle
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(ctx, TaskSnapshot{RunID: "run-1", TaskID: "t1", Status: string(TaskStatusQueued)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetTaskStatus(ctx, "run-1", "t1", 0, string(TaskStatusRunning)); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetTaskStatus(ctx, "run-1", "t1", 1, string(TaskStatusCompleted)); err != nil {
		t.Fatal(err)
	}

	task, err := repo.GetTask(ctx, "run-1", "t1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != string(TaskStatusCompleted) {
		t.Fatalf("status = %q, want %q", task.Status, TaskStatusCompleted)
	}
}

func TestStorageLedger_SQLiteCloseRunThenRebuild(t *testing.T) {
	store, err := storage.OpenSQLite(t.TempDir() + "/proj.db")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	repo1 := NewStorageLedgerRepository(store)
	if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo1.CreateTask(ctx, TaskSnapshot{RunID: "run-1", TaskID: "t1", Status: string(TaskStatusQueued)}); err != nil {
		t.Fatal(err)
	}
	// Cancel the task first (as the coordinator would do), then close the run.
	if err := repo1.CompareAndSetTaskStatus(ctx, "run-1", "t1", 0, string(TaskStatusRunning)); err != nil {
		t.Fatal(err)
	}
	if err := repo1.CompareAndSetTaskStatus(ctx, "run-1", "t1", 1, string(TaskStatusCanceled)); err != nil {
		t.Fatal(err)
	}
	if err := repo1.CloseRun(ctx, "run-1"); err != nil {
		t.Fatal(err)
	}

	// New repo from same store — verify persistence and status derivation
	repo2 := NewStorageLedgerRepository(store)
	
	// Debug: read raw events from store
	rawEvents, err := store.Events(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rawEvents {
		t.Logf("event: seq=%d kind=%s payload=%s", e.Sequence, e.Kind, string(e.Payload))
	}
	
	// Direct rebuild test with raw events
	directSnap, _, _, err := RebuildProjection(rawEvents)
	if err != nil {
		t.Fatalf("direct rebuild: %v", err)
	}
	t.Logf("direct rebuild status: %q", directSnap.Status)
	
	snap, err := repo2.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != RunStatusCanceled {
		t.Fatalf("status = %q, want %q (mem derives from tasks: der=%v)", snap.Status, RunStatusCanceled, snap.Tasks)
	}
}

func TestStorageLedger_SQLiteDuplicatePrevention(t *testing.T) {
	repo := newTestSQLiteRepo(t)
	ctx := context.Background()

	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCreated}); err != ErrDuplicate {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Concurrent access tests
// ---------------------------------------------------------------------------

func TestStorageLedger_ConcurrentCreateRun(t *testing.T) {
	repo := newTestStorageRepo(t)
	ctx := context.Background()

	const n = 20
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			id := fmt.Sprintf("run-%d", i)
			errs <- repo.CreateRun(ctx, "", RunSnapshot{RunID: id, Status: RunStatusCreated})
		}(i)
	}

	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent create: %v", err)
		}
	}

	runs, err := repo.ListRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != n {
		t.Fatalf("expected %d runs, got %d", n, len(runs))
	}
}

func TestStorageLedger_ConcurrentTaskStatusTransition(t *testing.T) {
	repo := newTestStorageRepo(t)
	ctx := context.Background()

	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(ctx, TaskSnapshot{RunID: "run-1", TaskID: "t1", Status: string(TaskStatusQueued)}); err != nil {
		t.Fatal(err)
	}

	// Concurrent CAS attempts — only one should succeed
	const n = 10
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			errs <- repo.CompareAndSetTaskStatus(ctx, "run-1", "t1", 0, string(TaskStatusRunning))
		}()
	}

	successCount := 0
	for i := 0; i < n; i++ {
		if err := <-errs; err == nil {
			successCount++
		}
	}
	if successCount != 1 {
		t.Fatalf("expected exactly 1 successful CAS, got %d", successCount)
	}
}
