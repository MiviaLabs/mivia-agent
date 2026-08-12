package coordinator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// seededCancelRaceRun creates a run and a single task at the given ledger
// status, then returns a coordinator and a live (not yet canceled) run handle
// over that run. Direct unit tests drive the DAG methods below deterministically
// instead of racing a goroutine.
func seededCancelRaceRun(t *testing.T, repo ledger.LedgerRepository, taskStatus string) (*coordinator, *RunHandle) {
	t.Helper()
	ctx := context.Background()
	c := newIdempotencyCoordinator(repo).(*coordinator)
	const runID = "cancel-race-run"
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now()
	if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: runID, TaskID: "t1", Status: taskStatus, Version: 1, CreatedAt: now,
		Attempts: []ledger.AttemptSnapshot{{AttemptID: "attempt-1", TaskID: "t1", RunID: runID, AttemptNum: 1, StartedAt: now, Status: taskStatus}},
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	h := c.newRunHandle(runID, "", map[string]string{"t1": "attempt-1"}, "", false)
	return c, h
}

// TestStartReadyCancelClaimedWithoutPoolCancel pins the startReady side of the
// cancel race (dag.go:205-215): the queued -> running dispatch CAS loses to
// reconcileCancellation's queued -> cancel_requested CAS, so the task must
// surface as canceled. Because the ledger already claimed the task while
// poolCtx has not been canceled yet, the error falls back to context.Canceled
// in the cancelErr == nil window (dag.go:210-212).
func TestStartReadyCancelClaimedWithoutPoolCancel(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	c, h := seededCancelRaceRun(t, repo, string(ledger.TaskStatusCancelRequested))

	pending := map[string]subagents.Task{"t1": {ID: "t1", Name: "worker"}}
	results := map[string]subagents.Result{}
	queue := map[string]time.Time{}
	states := map[string]*RetryState{}
	ready := []subagents.Task{{ID: "t1", Name: "worker"}}

	if err := c.startReady(h, ready, pending, results, queue, states); err != nil {
		t.Fatalf("startReady: %v", err)
	}
	res, ok := results["t1"]
	if !ok {
		t.Fatal("no result recorded for t1")
	}
	if res.Status != "canceled" {
		t.Fatalf("t1 status = %q, want canceled (cancel-claimed task must not surface as failed)", res.Status)
	}
	if !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("t1 error = %v, want context.Canceled (poolCtx not yet canceled)", res.Err)
	}
	if _, ok := pending["t1"]; ok {
		t.Fatal("t1 still pending after startReady surfaced it canceled")
	}
}

// getTaskFailingRepo fails every GetTask so isCancelClaimed's probe cannot see
// the ledger (dag.go:230-232). It drives the unreadable-ledger branch of the
// cancel race: a task that cannot be proven cancel-claimed is treated as a
// genuine failure, not silently swallowed.
type getTaskFailingRepo struct {
	*ledger.MemoryLedgerRepository
}

func (getTaskFailingRepo) GetTask(context.Context, string, string) (ledger.TaskSnapshot, error) {
	return ledger.TaskSnapshot{}, errors.New("simulated ledger read failure")
}

// TestIsCancelClaimedTreatsReadFailureAsNotClaimed pins dag.go:231-232: a task
// whose current status cannot be read is NOT treated as cancel-claimed, so the
// caller falls through to the normal failure path instead of inventing a cancel.
func TestIsCancelClaimedTreatsReadFailureAsNotClaimed(t *testing.T) {
	repo := &getTaskFailingRepo{MemoryLedgerRepository: ledger.NewMemoryLedgerRepository()}
	c := newIdempotencyCoordinator(repo).(*coordinator)
	h := c.newRunHandle("run", "", map[string]string{}, "", false)
	if c.isCancelClaimed(h, "t1") {
		t.Fatal("isCancelClaimed = true for an unreadable ledger; a read failure must be treated as not claimed")
	}
}

// TestStartReadyTreatsUnreadableLedgerAsFailure drives the same unreadable
// ledger through startReady: the dispatch read fails, recovery retry is not
// possible, isCancelClaimed returns false on the read failure, and the task is
// surfaced as failed with the read error joined into the run error.
func TestStartReadyTreatsUnreadableLedgerAsFailure(t *testing.T) {
	repo := &getTaskFailingRepo{MemoryLedgerRepository: ledger.NewMemoryLedgerRepository()}
	c := newIdempotencyCoordinator(repo).(*coordinator)
	h := c.newRunHandle("run", "", map[string]string{"t1": "attempt-1"}, "", false)

	pending := map[string]subagents.Task{"t1": {ID: "t1", Name: "worker"}}
	results := map[string]subagents.Result{}
	queue := map[string]time.Time{}
	states := map[string]*RetryState{}
	ready := []subagents.Task{{ID: "t1", Name: "worker"}}

	if err := c.startReady(h, ready, pending, results, queue, states); err == nil {
		t.Fatal("startReady: expected an error from the unreadable ledger")
	}
	if res := results["t1"]; res.Status != "failed" {
		t.Fatalf("t1 status = %q, want failed (unreadable task is a genuine failure)", res.Status)
	}
}

// TestMarkCanceledWithoutResultsSkipsLiveRun pins dag.go:262-263: a run whose
// context is still live is not being canceled, so markCanceledWithoutResults
// must leave every result untouched.
func TestMarkCanceledWithoutResultsSkipsLiveRun(t *testing.T) {
	c := newIdempotencyCoordinator(ledger.NewMemoryLedgerRepository()).(*coordinator)
	h := c.newRunHandle("run", "", map[string]string{}, "", false)
	results := map[string]subagents.Result{"t1": {TaskID: "t1", Status: "completed"}}
	tasks := []subagents.Task{{ID: "t1", Name: "worker"}}

	markCanceledWithoutResults(h, tasks, results)
	if got := results["t1"]; got.Status != "completed" {
		t.Fatalf("t1 status = %q, want completed (live run must not be touched)", got.Status)
	}
}

// TestMarkCanceledWithoutResultsLedgerAware pins the ledger-consulting
// behavior of markCanceledWithoutResults: a failed/timed_out result whose
// ledger already records that terminal status (the R9 early finalize fence won
// the CAS — a durable genuine outcome predating the cancel) is KEPT, so
// recordRunResults short-circuits on the matching status and emits the proper
// terminal event instead of attempting a forbidden failed/timed_out -> canceled
// CAS. retry_pending results are still overwritten to canceled (not a terminal
// outcome of a canceled run), and terminal completed outcomes stay untouched.
func TestMarkCanceledWithoutResultsLedgerAware(t *testing.T) {
	ctx := context.Background()
	repo := ledger.NewMemoryLedgerRepository()
	c := newIdempotencyCoordinator(repo).(*coordinator)
	const runID = "cancel-ledger-run"
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now()
	for id, status := range map[string]string{
		"failed":        string(ledger.TaskStatusFailed),
		"timed_out":     string(ledger.TaskStatusTimedOut),
		"retry_pending": string(ledger.TaskStatusRetryPending),
		"completed":     string(ledger.TaskStatusCompleted),
	} {
		if err := repo.CreateTask(ctx, ledger.TaskSnapshot{RunID: runID, TaskID: id, Status: status, Version: 1, CreatedAt: now}); err != nil {
			t.Fatalf("CreateTask %s: %v", id, err)
		}
	}
	h := c.newRunHandle(runID, "", map[string]string{}, "", false)
	h.cancel() // poolCtx is now canceled, as it is whenever the DAG calls this

	results := map[string]subagents.Result{
		"failed":        {TaskID: "failed", Status: "failed", Err: errors.New("boom")},
		"timed_out":     {TaskID: "timed_out", Status: "timed_out", Err: errors.New("deadline")},
		"retry_pending": {TaskID: "retry_pending", Status: "retry_pending"},
		"completed":     {TaskID: "completed", Status: "completed"},
	}
	tasks := []subagents.Task{{ID: "failed"}, {ID: "timed_out"}, {ID: "retry_pending"}, {ID: "completed"}}

	markCanceledWithoutResults(h, tasks, results)
	// The ledger already records the durable terminal outcomes: kept as-is,
	// with their original errors.
	if got := results["failed"]; got.Status != "failed" || !strings.Contains(got.Err.Error(), "boom") {
		t.Fatalf("failed = %#v, want kept failed with its original error", got)
	}
	if got := results["timed_out"]; got.Status != "timed_out" || !strings.Contains(got.Err.Error(), "deadline") {
		t.Fatalf("timed_out = %#v, want kept timed_out with its original error", got)
	}
	// retry_pending is not a terminal outcome of a canceled run: overwritten.
	if got := results["retry_pending"]; got.Status != "canceled" || !errors.Is(got.Err, context.Canceled) {
		t.Fatalf("retry_pending = %#v, want canceled with context.Canceled", got)
	}
	if got := results["completed"]; got.Status != "completed" {
		t.Fatalf("completed status = %q, want unchanged (terminal outcomes stay)", got.Status)
	}
}

// TestMarkCanceledWithoutResultsOverwritesCancelClaimed pins the cancel-won
// branch of the ledger consultation: a failed result whose ledger already shows
// cancel_requested (reconcileCancellation CASed running -> cancel_requested
// before the early fence) must be overwritten to canceled as before, so
// recordRunResults' cancel override agrees on a clean cancel.
func TestMarkCanceledWithoutResultsOverwritesCancelClaimed(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	_, h := seededCancelRaceRun(t, repo, string(ledger.TaskStatusCancelRequested))
	h.cancel()
	results := map[string]subagents.Result{"t1": {TaskID: "t1", Status: "failed", Err: errors.New("boom")}}
	tasks := []subagents.Task{{ID: "t1", Name: "worker"}}

	markCanceledWithoutResults(h, tasks, results)
	if got := results["t1"]; got.Status != "canceled" || !errors.Is(got.Err, context.Canceled) {
		t.Fatalf("t1 = %#v, want canceled (cancel won the race before the early fence)", got)
	}
}

// TestMarkCanceledWithoutResultsOverwritesRunningLedger pins the any-other-
// status branch of the ledger consultation: a failed result whose ledger is
// still running (the early fence did not win; recordRunResults can still CAS
// running -> canceled) is overwritten to canceled as before.
func TestMarkCanceledWithoutResultsOverwritesRunningLedger(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	_, h := seededCancelRaceRun(t, repo, string(ledger.TaskStatusRunning))
	h.cancel()
	results := map[string]subagents.Result{"t1": {TaskID: "t1", Status: "failed", Err: errors.New("boom")}}
	tasks := []subagents.Task{{ID: "t1", Name: "worker"}}

	markCanceledWithoutResults(h, tasks, results)
	if got := results["t1"]; got.Status != "canceled" || !errors.Is(got.Err, context.Canceled) {
		t.Fatalf("t1 = %#v, want canceled (ledger still running: overwrite as before)", got)
	}
}

// TestMarkCanceledWithoutResultsKeepsOnLedgerReadError drives the unreadable-
// ledger branch of the ledger consultation (same getTaskFailingRepo as
// TestIsCancelClaimedTreatsReadFailureAsNotClaimed): a failed result whose
// ledger cannot be read is KEPT, failing safe in the direction that avoids the
// invalid failed -> canceled CAS instead of reproducing the defect by
// overwriting blind.
func TestMarkCanceledWithoutResultsKeepsOnLedgerReadError(t *testing.T) {
	repo := &getTaskFailingRepo{MemoryLedgerRepository: ledger.NewMemoryLedgerRepository()}
	c := newIdempotencyCoordinator(repo).(*coordinator)
	h := c.newRunHandle("run", "", map[string]string{}, "", false)
	h.cancel()
	results := map[string]subagents.Result{"t1": {TaskID: "t1", Status: "failed", Err: errors.New("boom")}}
	tasks := []subagents.Task{{ID: "t1", Name: "worker"}}

	markCanceledWithoutResults(h, tasks, results)
	if got := results["t1"]; got.Status != "failed" || !strings.Contains(got.Err.Error(), "boom") {
		t.Fatalf("t1 = %#v, want kept failed (unreadable ledger fails safe away from the invalid CAS)", got)
	}
}

// TestMarkCanceledWithoutResultsFillsMissingResult pins the missing-result
// branch: a task with no result at all when the run is canceled (never reached
// the pool) is given a canceled result so finalizeDAG never emits "missing".
func TestMarkCanceledWithoutResultsFillsMissingResult(t *testing.T) {
	c := newIdempotencyCoordinator(ledger.NewMemoryLedgerRepository()).(*coordinator)
	h := c.newRunHandle("run", "", map[string]string{}, "", false)
	h.cancel()
	results := map[string]subagents.Result{}
	tasks := []subagents.Task{{ID: "t1", Name: "worker"}}

	markCanceledWithoutResults(h, tasks, results)
	if got := results["t1"]; got.Status != "canceled" || !errors.Is(got.Err, context.Canceled) {
		t.Fatalf("t1 = %#v, want canceled with context.Canceled (missing result on a canceled run)", got)
	}
}

// TestCanceledResultFallsBackToContextCanceled pins dag.go:306-308: a canceled
// result carries the run's cancellation error when available and falls back to
// context.Canceled for the window where the ledger has already claimed the task
// but poolCtx has not been canceled yet.
func TestCanceledResultFallsBackToContextCanceled(t *testing.T) {
	c := newIdempotencyCoordinator(ledger.NewMemoryLedgerRepository()).(*coordinator)
	h := c.newRunHandle("run", "", map[string]string{}, "", false)
	res := canceledResult(h, "t1")
	if res.Status != "canceled" || !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("canceledResult = %#v, want canceled with context.Canceled", res)
	}
}

// retryCancelRaceRepo simulates reconcileCancellation winning the
// running -> cancel_requested CAS between processResults' retry CAS and its
// post-CAS re-check: GetTask reports running twice (the initial isCancelClaimed
// probe and transitionTaskToStatus's read), then cancel_requested (the re-check
// read), and every retry CAS loses to the cancellation.
type retryCancelRaceRepo struct {
	*ledger.MemoryLedgerRepository
	reads int
}

func (r *retryCancelRaceRepo) GetTask(ctx context.Context, runID, taskID string) (ledger.TaskSnapshot, error) {
	snap, err := r.MemoryLedgerRepository.GetTask(ctx, runID, taskID)
	if err != nil {
		return snap, err
	}
	r.reads++
	if r.reads >= 3 {
		snap.Status = string(ledger.TaskStatusCancelRequested)
	} else {
		snap.Status = string(ledger.TaskStatusRunning)
	}
	return snap, nil
}

func (retryCancelRaceRepo) CompareAndSetTaskStatus(context.Context, string, string, uint64, string) error {
	return errors.New("simulated CAS loss to reconcileCancellation")
}

// TestProcessResultsCancelClaimedAfterRetryCASLoss pins dag.go:365-367: when
// the retry_pending CAS loses to reconcileCancellation between the initial
// isCancelClaimed check and the CAS, the post-CAS re-check must surface the
// task as canceled and join no spurious transition error into the run error.
func TestProcessResultsCancelClaimedAfterRetryCASLoss(t *testing.T) {
	repo := &retryCancelRaceRepo{MemoryLedgerRepository: ledger.NewMemoryLedgerRepository()}
	c, h := seededCancelRaceRun(t, repo, string(ledger.TaskStatusRunning))
	results := map[string]subagents.Result{}
	queue := map[string]time.Time{}
	states := map[string]*RetryState{}
	// Transient-marked: shouldRetryTask now requires provider.IsTransient for
	// a "failed" task's error before treating it as retryable at all, which
	// is what routes processResults into the retry-CAS path this test pins.
	batch := []subagents.Result{{TaskID: "t1", Status: "failed", Err: &provider.TransientError{Err: errors.New("boom")}}}

	runErr := c.processResults(h, batch, results, queue, states)
	if runErr != nil {
		t.Fatalf("processResults error = %v, want nil (cancel-claimed task must not pollute the run error)", runErr)
	}
	res := results["t1"]
	if res.Status != "canceled" || !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("t1 = %#v, want canceled with context.Canceled", res)
	}
	if _, retrying := queue["t1"]; retrying {
		t.Fatal("t1 was queued for retry despite being cancel-claimed")
	}
}
