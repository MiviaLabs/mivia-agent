package ledger

import (
	"context"
	"fmt"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func BenchmarkMemoryLedger_CreateRun(b *testing.B) {
	repo := NewMemoryLedgerRepository()
	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		snap := RunSnapshot{
			RunID:  fmt.Sprintf("bench-run-%d", i),
			Status: RunStatusCreated,
		}
		_ = repo.CreateRun(ctx, "", snap)
	}
}

func BenchmarkMemoryLedger_TaskLifecycle(b *testing.B) {
	repo := NewMemoryLedgerRepository()
	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		runID := fmt.Sprintf("bench-lifecycle-%d", i)
		_ = repo.CreateRun(ctx, "", RunSnapshot{RunID: runID, Status: RunStatusCreated})
		_ = repo.CreateTask(ctx, TaskSnapshot{RunID: runID, TaskID: "t1", Status: string(TaskStatusQueued)})
		_ = repo.CompareAndSetTaskStatus(ctx, runID, "t1", 0, string(TaskStatusRunning))
		_ = repo.CompareAndSetTaskStatus(ctx, runID, "t1", 1, string(TaskStatusCompleted))
	}
}

// BenchmarkMemoryLedger_AppendEvent measures the duplicate-detection path in
// AppendEvent: a fixed large batch of distinct appends followed by one
// duplicate. The pre-fix linear scan paid O(n) on every append (O(n²) for the
// batch); the per-run map index pays O(1). Timing is evidence for the host
// gate, never an assertion.
func BenchmarkMemoryLedger_AppendEvent(b *testing.B) {
	repo := NewMemoryLedgerRepository()
	ctx := context.Background()
	const batch = 4096
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		runID := fmt.Sprintf("bench-append-%d", i)
		if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: runID, Status: RunStatusCreated}); err != nil {
			b.Fatal(err)
		}
		for j := 0; j < batch; j++ {
			if err := repo.AppendEvent(ctx, LifecycleEvent{
				ID: fmt.Sprintf("run-%d-ev-%d", i, j), RunID: runID, Kind: "task_started",
			}); err != nil {
				b.Fatal(err)
			}
		}
		if err := repo.AppendEvent(ctx, LifecycleEvent{
			ID: fmt.Sprintf("run-%d-ev-0", i), RunID: runID, Kind: "task_started",
		}); err != ErrDuplicate {
			b.Fatalf("duplicate append error = %v, want ErrDuplicate", err)
		}
	}
}

func BenchmarkStorageLedger_CreateRun(b *testing.B) {
	store := storage.NewMemory()
	repo := NewStorageLedgerRepository(store)
	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		snap := RunSnapshot{
			RunID:  fmt.Sprintf("bench-store-run-%d", i),
			Status: RunStatusCreated,
		}
		_ = repo.CreateRun(ctx, "", snap)
	}
}

func BenchmarkStorageLedger_TaskLifecycle(b *testing.B) {
	store := storage.NewMemory()
	repo := NewStorageLedgerRepository(store)
	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		runID := fmt.Sprintf("bench-store-lifecycle-%d", i)
		_ = repo.CreateRun(ctx, "", RunSnapshot{RunID: runID, Status: RunStatusCreated})
		_ = repo.CreateTask(ctx, TaskSnapshot{RunID: runID, TaskID: "t1", Status: string(TaskStatusQueued)})
		_ = repo.CompareAndSetTaskStatus(ctx, runID, "t1", 0, string(TaskStatusRunning))
		_ = repo.CompareAndSetTaskStatus(ctx, runID, "t1", 1, string(TaskStatusCompleted))
	}
}

// BenchmarkStorageLedger_GetRun and _ListRuns cover the read path, where
// projection catch-up puts a store probe in front of a memory-only read.
func BenchmarkStorageLedger_GetRun(b *testing.B) {
	repo := benchStorageRepoWithRuns(b, 100)
	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := repo.GetRun(ctx, "bench-read-run-50"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStorageLedger_ListRuns(b *testing.B) {
	repo := benchStorageRepoWithRuns(b, 100)
	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := repo.ListRuns(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func benchStorageRepoWithRuns(b *testing.B, runs int) *StorageLedgerRepository {
	b.Helper()
	store := storage.NewMemory()
	repo := NewStorageLedgerRepository(store)
	ctx := context.Background()
	for i := 0; i < runs; i++ {
		runID := fmt.Sprintf("bench-read-run-%d", i)
		if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: runID, Status: RunStatusCreated}); err != nil {
			b.Fatal(err)
		}
		if err := repo.CreateTask(ctx, TaskSnapshot{RunID: runID, TaskID: "t1", Status: string(TaskStatusQueued)}); err != nil {
			b.Fatal(err)
		}
		if err := repo.CompareAndSetTaskStatus(ctx, runID, "t1", 0, string(TaskStatusRunning)); err != nil {
			b.Fatal(err)
		}
	}
	return repo
}
