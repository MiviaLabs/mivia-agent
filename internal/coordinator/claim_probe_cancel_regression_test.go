package coordinator

// Regression coverage for the claim-liveness-probe cancel hole (dag.go:87-106):
// a canceled run whose ClaimRun probe fails with a NON-ErrClaimHeld error
// (SQLite ExecContext returns "context canceled") must settle every
// never-executed task as canceled — never "missing", never completed — and a
// "missing" result must never be CASed to completed by recordRunResults
// (record_results.go mapStatus default).

import (
	"context"
	"errors"
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

// claimProbeLiveFailingRepo wraps a ledger repository and makes every ClaimRun
// fail with a generic NON-ErrClaimHeld error while the run's pool context stays
// live — the probe-failure-on-a-live-run path, as opposed to
// claimProbeCanceledRepo above, which fails only once the passed context is
// canceled. All other methods delegate to the wrapped repository.
type claimProbeLiveFailingRepo struct {
	ledger.LedgerRepository
}

func (claimProbeLiveFailingRepo) ClaimRun(context.Context, string, string) error {
	return fmt.Errorf("simulated live claim probe failure")
}

// TestRunDAGSeededProbeFailureLiveCtxTerminalizesRetryQueueCanceled pins the
// retry-queue terminalization on a claim probe failure while the run's pool
// context stays live (dag.go finalizeDAG + record_results.go): a recovered
// retry candidate t1 (ledger status failed) is routed by startReady through
// queueRecoveredRetry (failed -> retry_pending + a future queue entry), t2
// (ledger queued) is batched, the probe fails with a generic non-ErrClaimHeld
// error (markCanceledWithoutResults no-ops on a live context), the loop breaks,
// and finalizeDAG terminalizes the no-result retry entry as canceled — never
// "failed". recordRunResults then CASes retry_pending -> canceled (the only
// valid terminal target besides queued) instead of attempting the forbidden
// retry_pending -> failed transition, so no ErrInvalidTransition artifact joins
// the run error and the ledger task ends canceled. t2's pre-existing "missing"
// skip (a never-executed batched task on a live-ctx probe failure stays
// non-terminal) is unchanged and out of scope. Fails before the fix: t1's
// result is "failed", ErrInvalidTransition is joined, and the ledger stays
// retry_pending.
func TestRunDAGSeededProbeFailureLiveCtxTerminalizesRetryQueueCanceled(t *testing.T) {
	ctx := context.Background()
	repo := &claimProbeLiveFailingRepo{LedgerRepository: ledger.NewMemoryLedgerRepository()}
	c := newIdempotencyCoordinator(repo).WithRetryPolicy(RetryPolicy{
		MaxRetries: 1, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond, BackoffFactor: 2, JitterFraction: 0,
	}).(*coordinator)
	const runID = "probe-live-retry-run"
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now()
	for id, status := range map[string]string{
		"t1": string(ledger.TaskStatusFailed), // recovered retry candidate
		"t2": string(ledger.TaskStatusQueued), // batched, never executed
	} {
		if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
			RunID: runID, TaskID: id, Status: status, Version: 1, CreatedAt: now,
			Attempts: []ledger.AttemptSnapshot{{AttemptID: "attempt-1", TaskID: id, RunID: runID, AttemptNum: 1, StartedAt: now, Status: status}},
		}); err != nil {
			t.Fatalf("CreateTask %s: %v", id, err)
		}
	}
	h := c.newRunHandle(runID, "", map[string]string{"t1": "attempt-1", "t2": "attempt-1"}, "", false)

	tasks := []subagents.Task{{ID: "t1", Name: "worker"}, {ID: "t2", Name: "worker"}}
	results, runErr := c.runDAGSeeded(h, tasks, nil)
	if runErr == nil {
		t.Fatal("runDAGSeeded: expected the probe failure in the run error")
	}
	var t1Result *subagents.Result
	for i := range results {
		if results[i].TaskID == "t1" {
			t1Result = &results[i]
		}
	}
	if t1Result == nil {
		t.Fatal("t1 missing from the DAG result set")
	}
	if t1Result.Status != "canceled" {
		t.Fatalf("t1 result status = %q, want canceled (the run ended before retry re-dispatch; never %q)", t1Result.Status, "failed")
	}

	// Drive the ledger writes exactly as executeResumedRun does after the DAG.
	persistErr := c.recordRunResults(h, tasks, results, runErr)
	if persistErr == nil {
		t.Fatal("recordRunResults: expected the run error to carry the probe failure")
	}
	if errors.Is(persistErr, ledger.ErrInvalidTransition) {
		t.Fatalf("MUTATION FAIL: recordRunResults joined an invalid-transition artifact: %v", persistErr)
	}
	snap, err := repo.GetTask(ctx, runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("ledger t1 status = %q, want canceled (the forbidden retry_pending -> failed CAS must never land)", snap.Status)
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
