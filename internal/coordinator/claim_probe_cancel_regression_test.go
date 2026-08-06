package coordinator

// Regression coverage for the claim-liveness-probe cancel hole (dag.go:87-106):
// a canceled run whose ClaimRun probe fails with a NON-ErrClaimHeld error
// (SQLite ExecContext returns "context canceled") must settle every
// never-executed task as canceled — never "missing", never completed — and a
// "missing" result must never be CASed to completed by recordRunResults
// (record_results.go mapStatus default).

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// claimProbeCanceledRepo wraps a ledger repository and makes ClaimRun fail
// with a NON-ErrClaimHeld error once the passed context is canceled — exactly
// the SQLite ExecContext behavior ("context canceled") the DAG hits when the
// run is canceled while the claim liveness probe is in flight. All other
// methods delegate to the wrapped repository.
type claimProbeCanceledRepo struct {
	ledger.LedgerRepository
}

func (r *claimProbeCanceledRepo) ClaimRun(ctx context.Context, runID, holderID string) error {
	if ctx.Err() != nil {
		// Non-ErrClaimHeld: a canceled probe context is a transient probe
		// failure, not a theft signal.
		return fmt.Errorf("SQLite ExecContext: %w", ctx.Err())
	}
	return r.LedgerRepository.ClaimRun(ctx, runID, holderID)
}

// TestRunDAGSeededClaimProbeCanceledSettlesTasksCanceled pins dag.go:87-106:
// when the claim liveness probe fails with a NON-ErrClaimHeld error while the
// run's pool context is canceled, the loop must settle every never-executed
// task as canceled BEFORE breaking. Without markCanceledWithoutResults,
// finalizeDAG emits "missing" for those tasks and recordRunResults (mapStatus
// default) CASes them running -> completed — a canceled run durably recording
// never-executed tasks as completed.
func TestRunDAGSeededClaimProbeCanceledSettlesTasksCanceled(t *testing.T) {
	ctx := context.Background()
	repo := &claimProbeCanceledRepo{LedgerRepository: ledger.NewMemoryLedgerRepository()}
	c := newIdempotencyCoordinator(repo).(*coordinator)
	const runID = "claim-probe-cancel-run"
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now()
	if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: runID, TaskID: "t1", Status: string(ledger.TaskStatusQueued), Version: 1, CreatedAt: now,
		Attempts: []ledger.AttemptSnapshot{{AttemptID: "attempt-1", TaskID: "t1", RunID: runID, AttemptNum: 1, StartedAt: now, Status: string(ledger.TaskStatusQueued)}},
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	h := c.newRunHandle(runID, "", map[string]string{"t1": "attempt-1"}, "", false)
	h.cancel() // the run is canceled; the next ClaimRun probe fails with the canceled-ctx error

	tasks := []subagents.Task{{ID: "t1", Name: "worker"}}
	results, runErr := c.runDAGSeeded(h, tasks, nil)
	if runErr == nil {
		t.Fatal("runDAGSeeded: expected the probe failure in the run error")
	}
	if len(results) != 1 {
		t.Fatalf("result count = %d, want 1", len(results))
	}
	if results[0].Status != "canceled" {
		t.Fatalf("t1 result status = %q, want canceled (never completed, never %q)", results[0].Status, "missing")
	}

	// Drive the ledger writes exactly as executeResumedRun does after the DAG.
	persistErr := c.recordRunResults(h, tasks, results, runErr)
	if persistErr == nil {
		t.Fatal("recordRunResults: expected the run error to carry the probe failure")
	}
	snap, err := repo.GetTask(ctx, runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status == string(ledger.TaskStatusCompleted) {
		t.Fatal("MUTATION FAIL: never-executed task was durably recorded completed on a canceled run")
	}
	if snap.Status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("ledger t1 status = %q, want canceled", snap.Status)
	}
}

// TestRecordRunResultsMissingResultNeverTerminalizes pins record_results.go
// defense in depth: a "missing" result — a never-executed task on a
// stolen/aborted run — must never be terminalized. mapStatus's default maps a
// missing result (no error) to completed, so without the skip recordRunResults
// CASes a running task running -> completed (a durable false terminal) and
// attempts queued -> completed (an invalid-transition artifact in the run
// error). Both must be skipped: no CAS, no error, tasks stay non-terminal for
// the owner/resume to reconcile.
func TestRecordRunResultsMissingResultNeverTerminalizes(t *testing.T) {
	ctx := context.Background()
	repo := ledger.NewMemoryLedgerRepository()
	c := newIdempotencyCoordinator(repo).(*coordinator)
	const runID = "missing-never-terminalizes"
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now()
	for id, status := range map[string]string{
		"running": string(ledger.TaskStatusRunning),
		"queued":  string(ledger.TaskStatusQueued),
	} {
		if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
			RunID: runID, TaskID: id, Status: status, Version: 1, CreatedAt: now,
			Attempts: []ledger.AttemptSnapshot{{AttemptID: "attempt-1", TaskID: id, RunID: runID, AttemptNum: 1, StartedAt: now, Status: status}},
		}); err != nil {
			t.Fatalf("CreateTask %s: %v", id, err)
		}
	}
	h := c.newRunHandle(runID, "", map[string]string{"running": "attempt-1", "queued": "attempt-1"}, "", false)

	tasks := []subagents.Task{{ID: "running", Name: "worker"}, {ID: "queued", Name: "worker"}}
	results := []subagents.Result{
		{TaskID: "running", Status: "missing"},
		{TaskID: "queued", Status: "missing"},
	}
	runErr := c.recordRunResults(h, tasks, results, nil)
	if runErr != nil {
		t.Fatalf("recordRunResults: %v (a skipped missing result must join no error)", runErr)
	}
	for _, id := range []string{"running", "queued"} {
		snap, err := repo.GetTask(ctx, runID, id)
		if err != nil {
			t.Fatal(err)
		}
		if snap.Status == string(ledger.TaskStatusCompleted) {
			t.Fatalf("MUTATION FAIL: missing result for %q was CASed to completed", id)
		}
	}
	runningSnap, err := repo.GetTask(ctx, runID, "running")
	if err != nil {
		t.Fatal(err)
	}
	if runningSnap.Status != string(ledger.TaskStatusRunning) {
		t.Fatalf("running task status = %q, want running (missing result must leave it non-terminal)", runningSnap.Status)
	}
	queuedSnap, err := repo.GetTask(ctx, runID, "queued")
	if err != nil {
		t.Fatal(err)
	}
	if queuedSnap.Status != string(ledger.TaskStatusQueued) {
		t.Fatalf("queued task status = %q, want queued (missing result must leave it non-terminal)", queuedSnap.Status)
	}
}
