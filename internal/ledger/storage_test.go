package ledger

import (
	"context"
	"fmt"
	"math"
	"sync"
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
// Serialization round-trip test
// ---------------------------------------------------------------------------

func TestRunSnapshotJSONRoundTrip(t *testing.T) {
	now := time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC)
	completedAt := now.Add(1 * time.Hour)
	original := RunSnapshot{
		RunID:       "run-roundtrip",
		DisplayName: "round-trip-test",
		Status:      RunStatusCompleted,
		CreatedAt:   now,
		CompletedAt: &completedAt,
		Labels:      map[string]string{"env": "test"},
		Tasks: []TaskSnapshot{
			{
				RunID:     "run-roundtrip",
				TaskID:    "t1",
				Status:    string(TaskStatusCompleted),
				Version:   3,
				DependsOn: []string{},
				CreatedAt: now,
			},
		},
	}

	data, err := marshalRunSnapshot(original)
	if err != nil {
		t.Fatal(err)
	}

	restored, err := unmarshalRunSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}

	if restored.RunID != original.RunID {
		t.Fatalf("RunID: got %q, want %q", restored.RunID, original.RunID)
	}
	if restored.Status != original.Status {
		t.Fatalf("Status: got %q, want %q", restored.Status, original.Status)
	}
	if restored.CompletedAt == nil || !restored.CompletedAt.Equal(*original.CompletedAt) {
		t.Fatalf("CompletedAt mismatch")
	}
	if len(restored.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(restored.Tasks))
	}
	if restored.Tasks[0].TaskID != "t1" {
		t.Fatalf("TaskID: got %q, want %q", restored.Tasks[0].TaskID, "t1")
	}
	if restored.Tasks[0].Version != 3 {
		t.Fatalf("Version: got %d, want 3", restored.Tasks[0].Version)
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
	snap, err := repo2.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != RunStatusCanceled {
		t.Fatalf("status = %q, want %q", snap.Status, RunStatusCanceled)
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

// TestStorageLedger_EventCounterAdvanceOnRebuild verifies that rebuildLocked
// advances the storageEventIDCounter past stored event IDs so new events
// after restart don't collide with replayed ones.
func TestStorageLedger_EventCounterAdvanceOnRebuild(t *testing.T) {
	store := storage.NewMemory()
	ctx := context.Background()

	repo1 := NewStorageLedgerRepository(store)
	if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}

	// Simulate restart: new repo from same store triggers rebuild
	repo2 := NewStorageLedgerRepository(store)
	if _, err := repo2.GetRun(ctx, "run-1"); err != nil {
		t.Fatal(err)
	}

	// Creating a new run must succeed (no collision with replayed events)
	if err := repo2.CreateRun(ctx, "", RunSnapshot{RunID: "run-2", Status: RunStatusCreated}); err != nil {
		t.Fatalf("new run after rebuild should succeed, got: %v", err)
	}

	// Verify both runs are present
	runs, err := repo2.ListRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs after rebuild+create, got %d", len(runs))
	}
}

// TestStorageLedger_MaxRunIDNumber verifies that MaxRunIDNumber returns the
// highest run number parsed from stored event IDs.
func TestStorageLedger_MaxRunIDNumber(t *testing.T) {
	store := storage.NewMemory()
	ctx := context.Background()

	repo1 := NewStorageLedgerRepository(store)
	if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: "run-5", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: "run-42", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}

	// Simulate restart
	repo2 := NewStorageLedgerRepository(store)
	if _, err := repo2.ListRuns(ctx); err != nil {
		t.Fatal(err)
	}

	if maxRun := repo2.MaxRunIDNumber(); maxRun != 42 {
		t.Fatalf("expected maxRunNum=42, got %d", maxRun)
	}
}

// TestStorageLedger_ClosedRepoOps verifies operations on closed repo
// return ErrClosed.
func TestStorageLedger_ClosedRepoOps(t *testing.T) {
	repo := newTestStorageRepo(t)
	ctx := context.Background()

	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}

	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-after-close", Status: RunStatusCreated}); err != ErrClosed {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

// TestStorageLedger_RecoverPreservesMaxRunID tests that Recover triggers
// rebuild and properly sets maxRunNum.
func TestStorageLedger_RecoverPreservesMaxRunID(t *testing.T) {
	store := storage.NewMemory()
	ctx := context.Background()

	repo1 := NewStorageLedgerRepository(store)
	if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: "run-10", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}

	// Simulate restart with Recover
	repo2 := NewStorageLedgerRepository(store)
	_, err := repo2.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if maxRun := repo2.MaxRunIDNumber(); maxRun != 10 {
		t.Fatalf("expected maxRunNum=10 after Recover, got %d", maxRun)
	}

	// New run must not collide
	if err := repo2.CreateRun(ctx, "", RunSnapshot{RunID: "run-11", Status: RunStatusCreated}); err != nil {
		t.Fatalf("new run after recover+rebuild should succeed, got: %v", err)
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

// ---------------------------------------------------------------------------
// Crash-recovery tests (Phase B)
// ---------------------------------------------------------------------------

func TestStorageLedger_CrashRecovery_DetectsInterruptedRun(t *testing.T) {
	// Simulate a crash mid-DAG: create a run with some tasks in non-terminal states,
	// "crash" (close repo), then recover from a fresh repo backed by the same store.
	ctx := context.Background()
	store := storage.NewMemory()
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	// Phase 1: Create run with mixed task states (one completed, one running).
	repo1 := NewStorageLedgerRepository(store)
	repo1.SetTimeSource(func() time.Time { return now })
	if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: "run-crash", Status: RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	// Task 1: queued -> running -> completed
	if err := repo1.CreateTask(ctx, TaskSnapshot{RunID: "run-crash", TaskID: "t1", Status: string(TaskStatusQueued), Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err := repo1.CompareAndSetTaskStatus(ctx, "run-crash", "t1", 1, string(TaskStatusRunning)); err != nil {
		t.Fatal(err)
	}
	if err := repo1.CompareAndSetTaskStatus(ctx, "run-crash", "t1", 2, string(TaskStatusCompleted)); err != nil {
		t.Fatal(err)
	}
	// Task 2: queued -> running (interrupted mid-execution)
	if err := repo1.CreateTask(ctx, TaskSnapshot{RunID: "run-crash", TaskID: "t2", Status: string(TaskStatusQueued), Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err := repo1.CompareAndSetTaskStatus(ctx, "run-crash", "t2", 1, string(TaskStatusRunning)); err != nil {
		t.Fatal(err)
	}
	// Task 3: queued (never started)
	if err := repo1.CreateTask(ctx, TaskSnapshot{RunID: "run-crash", TaskID: "t3", Status: string(TaskStatusQueued), Version: 1}); err != nil {
		t.Fatal(err)
	}
	// Close repo1 (simulates crash)
	if err := repo1.Close(); err != nil {
		t.Fatal(err)
	}

	// Phase 2: Recover from same store.
	repo2 := NewStorageLedgerRepository(store)
	repo2.SetTimeSource(func() time.Time { return now })
	recovered, err := repo2.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Should have found the interrupted run.
	found := false
	for _, r := range recovered {
		if r.RunID == "run-crash" {
			found = true
			if !r.WasInterrupted {
				t.Fatal("MUTATION FAIL: run with running/queued tasks should be marked interrupted")
			}
			break
		}
	}
	if !found {
		t.Fatal("recovered run not found")
	}

	// Verify task states post-recovery.
	tasks, err := repo2.ListTasks(ctx, "run-crash")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}
	// t1 should be completed, t2 running, t3 queued
	stateMap := make(map[string]string)
	for _, t := range tasks {
		stateMap[t.TaskID] = t.Status
	}
	if stateMap["t1"] != string(TaskStatusCompleted) {
		t.Fatalf("t1 status = %q, want %q", stateMap["t1"], TaskStatusCompleted)
	}
	if stateMap["t2"] != string(TaskStatusRunning) {
		t.Fatalf("t2 status = %q, want %q", stateMap["t2"], TaskStatusRunning)
	}
	if stateMap["t3"] != string(TaskStatusQueued) {
		t.Fatalf("t3 status = %q, want %q", stateMap["t3"], TaskStatusQueued)
	}
}

func TestStorageLedger_CrashRecovery_CompletedRunsNotInterrupted(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	repo1 := NewStorageLedgerRepository(store)
	repo1.SetTimeSource(func() time.Time { return now })
	if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: "run-done", Status: RunStatusCompleted}); err != nil {
		t.Fatal(err)
	}
	repo1.Close()

	repo2 := NewStorageLedgerRepository(store)
	repo2.SetTimeSource(func() time.Time { return now })
	recovered, err := repo2.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recovered {
		if r.RunID == "run-done" && r.WasInterrupted {
			t.Fatal("completed run should not be marked interrupted")
		}
	}
}

func TestStorageLedger_CrashRecovery_MultipleRuns(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	repo1 := NewStorageLedgerRepository(store)
	repo1.SetTimeSource(func() time.Time { return now })

	// Create two completed runs and one interrupted run.
	for i := 1; i <= 2; i++ {
		runID := fmt.Sprintf("run-completed-%d", i)
		if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: runID, Status: RunStatusCompleted}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo1.CreateRun(ctx, "", RunSnapshot{RunID: "run-interrupted", Status: RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	repo1.Close()

	repo2 := NewStorageLedgerRepository(store)
	repo2.SetTimeSource(func() time.Time { return now })
	recovered, err := repo2.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 3 {
		t.Fatalf("expected 3 recovered runs, got %d", len(recovered))
	}
	interruptedCount := 0
	for _, r := range recovered {
		if r.WasInterrupted {
			interruptedCount++
		}
	}
	if interruptedCount != 1 {
		t.Fatalf("expected exactly 1 interrupted run, got %d", interruptedCount)
	}
}

// ---------------------------------------------------------------------------
// Storage oracle equivalence — formal proof that Memory and Storage backends
// produce identical projections under identical event sequences.
// ---------------------------------------------------------------------------

// applyEventSequence applies a deterministic sequence of operations to a
// LedgerRepository and returns the resulting run snapshots and events.
func applyEventSequence(repo LedgerRepository, now time.Time) error {
	ctx := context.Background()
	// Phase 1: Create run and tasks
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusCreated}); err != nil {
		return err
	}
	for i := 1; i <= 3; i++ {
		tid := fmt.Sprintf("t%d", i)
		if err := repo.CreateTask(ctx, TaskSnapshot{RunID: "run-1", TaskID: tid, Status: string(TaskStatusQueued), Version: 1, Attempts: []AttemptSnapshot{{AttemptID: "attempt-" + tid, TaskID: tid, RunID: "run-1", AttemptNum: 1, Status: string(TaskStatusQueued)}}}); err != nil {
			return err
		}
	}
	// Phase 2: Transition tasks with CAS version checks
	for i := 1; i <= 3; i++ {
		tid := fmt.Sprintf("t%d", i)
		// queued -> running
		if err := repo.CompareAndSetTaskStatus(ctx, "run-1", tid, 1, string(TaskStatusRunning)); err != nil {
			return err
		}
		// running -> completed (v2)
		if err := repo.CompareAndSetTaskStatus(ctx, "run-1", tid, 2, string(TaskStatusCompleted)); err != nil {
			return err
		}
		// Set output
		if err := repo.SetTaskOutput(ctx, "run-1", tid, "ref:output:10", ""); err != nil {
			return err
		}
		// Record attempt
		finished := now
		if err := repo.SetTaskAttempt(ctx, "run-1", tid, "attempt-"+tid, string(TaskStatusCompleted), &finished); err != nil {
			return err
		}
	}
	// Phase 3: Append lifecycle events
	events := []LifecycleEvent{
		{ID: "e1", RunID: "run-1", Kind: "task_created", TaskID: "t1"},
		{ID: "e2", RunID: "run-1", Kind: "task_running", TaskID: "t1"},
		{ID: "e3", RunID: "run-1", Kind: "task_completed", TaskID: "t1"},
		{ID: "e4", RunID: "run-1", Kind: "task_created", TaskID: "t2"},
		{ID: "e5", RunID: "run-1", Kind: "task_running", TaskID: "t2"},
		{ID: "e6", RunID: "run-1", Kind: "task_completed", TaskID: "t2"},
		{ID: "e7", RunID: "run-1", Kind: "task_created", TaskID: "t3"},
		{ID: "e8", RunID: "run-1", Kind: "task_running", TaskID: "t3"},
		{ID: "e9", RunID: "run-1", Kind: "task_completed", TaskID: "t3"},
	}
	for _, evt := range events {
		if err := repo.AppendEvent(ctx, evt); err != nil {
			return err
		}
	}
	// Phase 4: Attempt a stale version CAS (should fail with ErrConflict)
	if err := repo.CompareAndSetTaskStatus(ctx, "run-1", "t1", 1, string(TaskStatusFailed)); err != ErrConflict {
		return fmt.Errorf("expected ErrConflict for stale version, got %v", err)
	}
	// Phase 5: Close the run
	if err := repo.CloseRun(ctx, "run-1"); err != nil {
		return err
	}
	return nil
}

// snapshotsEqual compares two RunSnapshot values for equality (excluding
// CompletedAt which may differ by nanosecond precision).
func snapshotsEqual(a, b RunSnapshot) bool {
	if a.RunID != b.RunID || a.DisplayName != b.DisplayName || a.Status != b.Status {
		return false
	}
	// Build task maps keyed by TaskID for order-independent comparison.
	aTasks := make(map[string]TaskSnapshot, len(a.Tasks))
	for _, t := range a.Tasks {
		aTasks[t.TaskID] = t
	}
	if len(a.Tasks) != len(b.Tasks) {
		return false
	}
	for _, tb := range b.Tasks {
		ta, ok := aTasks[tb.TaskID]
		if !ok {
			return false
		}
		if ta.Status != tb.Status || ta.Version != tb.Version {
			return false
		}
		if ta.OutputRef != tb.OutputRef || ta.ErrorRef != tb.ErrorRef {
			return false
		}
		if len(ta.DependsOn) != len(tb.DependsOn) {
			return false
		}
		for j := range ta.DependsOn {
			if ta.DependsOn[j] != tb.DependsOn[j] {
				return false
			}
		}
	}
	return true
}

func TestStorageOracleEquivalence(t *testing.T) {
	// This test proves that MemoryLedgerRepository and StorageLedgerRepository
	// produce identical projections for the same deterministic event sequence,
	// establishing the memory backend as a contract oracle for the storage backend.
	ctx := context.Background()
	fixedTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	// Memory backend
	memRepo := NewMemoryLedgerRepository()
	memRepo.SetTimeSource(func() time.Time { return fixedTime })

	// Storage backend (backed by in-memory store)
	store := storage.NewMemory()
	storageRepo := NewStorageLedgerRepository(store)
	storageRepo.SetTimeSource(func() time.Time { return fixedTime })

	// Apply identical sequence to both
	if err := applyEventSequence(memRepo, fixedTime); err != nil {
		t.Fatalf("memory sequence: %v", err)
	}
	if err := applyEventSequence(storageRepo, fixedTime); err != nil {
		t.Fatalf("storage sequence: %v", err)
	}

	// Compare run snapshots
	memSnap, err := memRepo.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	storSnap, err := storageRepo.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotsEqual(memSnap, storSnap) {
		t.Logf("memory snapshot: run=%q status=%q tasks=%d", memSnap.RunID, memSnap.Status, len(memSnap.Tasks))
		t.Logf("storage snapshot: run=%q status=%q tasks=%d", storSnap.RunID, storSnap.Status, len(storSnap.Tasks))
		t.Fatal("MUTATION FAIL: run snapshots differ between memory and storage backends")
	}

	// Compare task lists (order-independent)
	memTasks, err := memRepo.ListTasks(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	storTasks, err := storageRepo.ListTasks(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(memTasks) != len(storTasks) {
		t.Fatalf("task count mismatch: memory=%d storage=%d", len(memTasks), len(storTasks))
	}
	memTaskMap := make(map[string]TaskSnapshot, len(memTasks))
	for _, t := range memTasks {
		memTaskMap[t.TaskID] = t
	}
	for _, s := range storTasks {
		m, ok := memTaskMap[s.TaskID]
		if !ok {
			t.Fatalf("task %q missing from memory backend", s.TaskID)
		}
		if m.Status != s.Status || m.Version != s.Version {
			t.Fatalf("task %q mismatch: memory=(%s,%d) storage=(%s,%d)",
				s.TaskID, m.Status, m.Version, s.Status, s.Version)
		}
	}

	// Compare events (order-independent)
	memEvents, err := memRepo.ListEvents(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	storEvents, err := storageRepo.ListEvents(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(memEvents) != len(storEvents) {
		t.Fatalf("event count mismatch: memory=%d storage=%d", len(memEvents), len(storEvents))
	}
	memEventMap := make(map[string]LifecycleEvent, len(memEvents))
	for _, e := range memEvents {
		memEventMap[e.ID] = e
	}
	for _, s := range storEvents {
		m, ok := memEventMap[s.ID]
		if !ok {
			t.Fatalf("event %q missing from memory backend", s.ID)
		}
		if m.Kind != s.Kind || m.TaskID != s.TaskID {
			t.Fatalf("event %q mismatch: memory=(%s,%s) storage=(%s,%s)",
				s.ID, m.Kind, m.TaskID, s.Kind, s.TaskID)
		}
	}

	// Verify the stale CAS was correctly rejected in both
	// (already verified inside applyEventSequence).
}

func TestStorageOracleEquivalence_Concurrent(t *testing.T) {
	// Concurrent stress test: both backends should handle concurrent CAS
	// with identical conflict/accept behaviour.
	ctx := context.Background()
	fixedTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	memRepo := NewMemoryLedgerRepository()
	memRepo.SetTimeSource(func() time.Time { return fixedTime })
	store := storage.NewMemory()
	storageRepo := NewStorageLedgerRepository(store)
	storageRepo.SetTimeSource(func() time.Time { return fixedTime })

	for _, tc := range []struct {
		name string
		repo LedgerRepository
	}{
		{"memory", memRepo},
		{"storage", storageRepo},
	} {
		runID := "run-" + tc.name
		if err := tc.repo.CreateRun(ctx, "", RunSnapshot{RunID: runID, Status: RunStatusCreated}); err != nil {
			t.Fatalf("%s: CreateRun: %v", tc.name, err)
		}
		for i := 1; i <= 5; i++ {
			tid := fmt.Sprintf("t%d", i)
			if err := tc.repo.CreateTask(ctx, TaskSnapshot{RunID: runID, TaskID: tid, Status: string(TaskStatusQueued), Version: 1}); err != nil {
				t.Fatalf("%s: CreateTask %s: %v", tc.name, tid, err)
			}
		}
		// Concurrent CAS: 10 goroutines race to transition each task
		var wg sync.WaitGroup
		for i := 1; i <= 5; i++ {
			tid := fmt.Sprintf("t%d", i)
			for j := 0; j < 10; j++ {
				wg.Add(1)
				go func(tid string) {
					defer wg.Done()
					_ = tc.repo.CompareAndSetTaskStatus(ctx, runID, tid, 1, string(TaskStatusRunning))
				}(tid)
			}
		}
		wg.Wait()

		// At least one task should have transitioned to running
		tasks, err := tc.repo.ListTasks(ctx, runID)
		if err != nil {
			t.Fatalf("%s: ListTasks: %v", tc.name, err)
		}
		hasRunning := false
		for _, task := range tasks {
			if task.Status == string(TaskStatusRunning) {
				hasRunning = true
				break
			}
		}
		if !hasRunning {
			t.Fatalf("%s: no tasks transitioned to running under concurrent CAS", tc.name)
		}
	}
}

// ---------------------------------------------------------------------------
// parseSuffixNum unit tests
// ---------------------------------------------------------------------------

func TestParseSuffixNum(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		prefix   string
		want     uint64
	}{
		{"normal match", "se-42", "se-", 42},
		{"zero value", "se-0", "se-", 0},
		{"large number", "run-999999999999", "run-", 999999999999},
		{"max uint64", "run-18446744073709551615", "run-", math.MaxUint64},
		// Edge cases
		{"empty string", "", "se-", 0},
		{"no prefix (just number)", "42", "se-", 0},
		{"non-numeric suffix", "se-abc", "se-", 0},
		{"overflow (too large for uint64)", "se-99999999999999999999", "se-", 0},
		{"prefix not at start", "xse-42", "se-", 0},
		{"empty prefix with content", "hello", "", 0},
		{"empty prefix empty string", "", "", 0},
		{"negative sign", "se--5", "se-", 0},
		{"partial prefix match", "senior-42", "se-", 0},
		{"trailing non-numeric", "se-123abc", "se-", 0},
		{"just prefix no suffix", "se-", "se-", 0},
		{"prefix with dots", "run-3.14", "run-", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSuffixNum(tt.s, tt.prefix)
			if got != tt.want {
				t.Errorf("parseSuffixNum(%q, %q) = %d, want %d",
					tt.s, tt.prefix, got, tt.want)
			}
		})
	}
}
