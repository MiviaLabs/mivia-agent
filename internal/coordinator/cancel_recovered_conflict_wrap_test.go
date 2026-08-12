package coordinator_test

// Regression coverage for cancelRecovered discarding ledger.ErrConflict
// instead of wrapping it: a per-child CAS conflict during a recovered
// cancel must remain errors.Is-detectable as ledger.ErrConflict so
// workflowledger.PanelCoordinator.isPanelCancelContention (D15) can keep
// classifying a racing concurrent-cancel attempt as benign, retryable
// contention rather than a hard cancel failure.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// conflictInjectingRepo forwards every call to the wrapped repository except
// CompareAndSetTaskStatus for one designated task, which it forces to
// report ledger.ErrConflict - simulating a concurrent cancel caller that won
// the CAS race on that child first.
type conflictInjectingRepo struct {
	ledger.LedgerRepository
	conflictTaskID string
}

func (r *conflictInjectingRepo) CompareAndSetTaskStatus(ctx context.Context, runID, taskID string, expectedVersion uint64, newStatus string) error {
	if taskID == r.conflictTaskID {
		return ledger.ErrConflict
	}
	return r.LedgerRepository.CompareAndSetTaskStatus(ctx, runID, taskID, expectedVersion, newStatus)
}

// TestCancelRecoveredWrapsErrConflict pins the fix for cancelRecovered's
// per-task CAS conflict branch: it must wrap ledger.ErrConflict (%w) rather
// than discard it, so callers that classify errors with errors.Is (like
// D15's isPanelCancelContention) can still recognize benign concurrent-cancel
// contention on a recovered task.
func TestCancelRecoveredWrapsErrConflict(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	repoA := ledger.NewBorrowedStorageLedgerRepository(store)
	now := time.Now()
	if err := repoA.CreateRun(ctx, "K", ledger.RunSnapshot{RunID: "run-x", Status: ledger.RunStatusQueued, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task := ledger.TaskSnapshot{
		RunID: "run-x", TaskID: "t1", HandlerName: "worker", Input: json.RawMessage(`{}`),
		Status: string(ledger.TaskStatusQueued), Version: 1, CreatedAt: now,
		Attempts: []ledger.AttemptSnapshot{{AttemptID: "att-1", TaskID: "t1", RunID: "run-x", AttemptNum: 1, Status: string(ledger.TaskStatusQueued)}},
	}
	if err := repoA.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := repoA.Close(); err != nil {
		t.Fatal(err)
	}

	repoB := &conflictInjectingRepo{
		LedgerRepository: ledger.NewBorrowedStorageLedgerRepository(store),
		conflictTaskID:   "t1",
	}
	d := runtime.New(runtime.Policy{})
	pool := subagents.New(d, subagents.Policy{Workers: 1})
	c2 := coordinator.New(repoB, pool)
	h, err := c2.Spawn(ctx, []subagents.Task{{Name: "worker"}}, "K")
	if err != nil {
		t.Fatalf("spawn dedup onto existing run: %v", err)
	}

	cancelErr := c2.Cancel(ctx, h)
	if cancelErr == nil {
		t.Fatal("cancelRecovered on a losing CAS: err = nil, want a wrapped ledger.ErrConflict")
	}
	if !errors.Is(cancelErr, ledger.ErrConflict) {
		t.Fatalf("cancelRecovered error = %v, want errors.Is(err, ledger.ErrConflict) to hold (sentinel must be wrapped, not discarded)", cancelErr)
	}
	if !strings.Contains(cancelErr.Error(), "t1") {
		t.Fatalf("cancelRecovered error = %v, want it to identify the conflicting task", cancelErr)
	}
}
