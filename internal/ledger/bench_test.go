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
