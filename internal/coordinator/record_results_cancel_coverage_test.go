package coordinator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestRecordRunResultsCancelOverrideWithoutPoolCancel pins the recordRunResults
// side of the cancel/startReady race (record_results.go:71-78): when a task is
// already claimed for cancellation (cancel_requested or canceled) but poolCtx
// has not been canceled yet, the stale pool outcome must be overridden to
// canceled with the context.Canceled fallback (record_results.go:74-76), and
// the ledger must agree on the clean cancel instead of attempting an invalid
// running/queued -> completed/failed CAS.
func TestRecordRunResultsCancelOverrideWithoutPoolCancel(t *testing.T) {
	ctx := context.Background()
	repo := ledger.NewMemoryLedgerRepository()
	c := newIdempotencyCoordinator(repo).(*coordinator)
	const runID = "cancel-override-run"
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: runID, TaskID: "t1", Status: string(ledger.TaskStatusCanceled), Version: 1, CreatedAt: now,
		Attempts: []ledger.AttemptSnapshot{{AttemptID: "attempt-1", TaskID: "t1", RunID: runID, AttemptNum: 1, StartedAt: now, Status: string(ledger.TaskStatusCanceled)}},
	}); err != nil {
		t.Fatal(err)
	}
	h := c.newRunHandle(runID, "", map[string]string{"t1": "attempt-1"}, "", false)

	tasks := []subagents.Task{{ID: "t1", Name: "worker"}}
	results := []subagents.Result{{TaskID: "t1", Status: "completed"}}
	runErr := c.recordRunResults(h, tasks, results, nil)
	if runErr != nil {
		t.Fatalf("recordRunResults: %v", runErr)
	}
	if results[0].Status != "canceled" || !errors.Is(results[0].Err, context.Canceled) {
		t.Fatalf("result = %#v, want canceled with context.Canceled (stale pool outcome overridden)", results[0])
	}
	snap, err := repo.GetTask(ctx, runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("ledger task status = %q, want canceled", snap.Status)
	}
}
