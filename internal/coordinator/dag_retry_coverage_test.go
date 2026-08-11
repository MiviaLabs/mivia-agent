package coordinator

// Direct unit coverage for the retry-scheduling paths that the integration
// suites leave uncovered:
//
//   - dag.go queueRecoveredRetry (dag.go:266): a task whose ledger status is
//     already failed/timed_out cannot dispatch (failed -> running is invalid),
//     so startReady hands it to queueRecoveredRetry, which re-queues it as
//     retry_pending with a fresh backoff.
//   - dag_retry.go flushRetries error/edge branches (lines 23-24 read failure,
//     27-28 stale non-retry_pending entry, 31-32 re-queue CAS failure, 45
//     retry-event append failure).
//   - dag_retry.go waitForRetry select: the timer branch (the backoff elapses
//     and the select takes timer.C) and the pool-context branch (the run is
//     canceled while the backoff is pending and the select takes
//     poolCtx.Done()).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestCoverageQueueRecoveredRetryRequeuesFailedTask pins dag.go:266: when a
// task's ledger status is already failed (a recovered/retried run re-dispatching
// it), the queued -> running CAS loses and startReady must hand the task to
// queueRecoveredRetry instead of surfacing a spurious failure. The task leaves
// the pending set, enters the retry queue with a backoff, and the ledger moves
// failed -> retry_pending.
func TestCoverageQueueRecoveredRetryRequeuesFailedTask(t *testing.T) {
	ctx := context.Background()
	repo := ledger.NewMemoryLedgerRepository()
	// The retry policy must be set on the coordinator BEFORE the handle is
	// created: newRunHandle snapshots c.retryPolicy into h.retryPolicy, and
	// queueRecoveredRetry reads it via h.policy().
	c := newIdempotencyCoordinator(repo).WithRetryPolicy(RetryPolicy{
		MaxRetries: 2, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond, BackoffFactor: 2, JitterFraction: 0,
	}).(*coordinator)
	const runID = "recovered-retry-run"
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now()
	if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: runID, TaskID: "t1", Status: string(ledger.TaskStatusFailed), Version: 1, CreatedAt: now,
		Attempts: []ledger.AttemptSnapshot{{AttemptID: "attempt-1", TaskID: "t1", RunID: runID, AttemptNum: 1, StartedAt: now, Status: string(ledger.TaskStatusFailed)}},
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	h := c.newRunHandle(runID, "", map[string]string{"t1": "attempt-1"}, "", false)

	pending := map[string]subagents.Task{"t1": {ID: "t1", Name: "worker", SessionID: "sess-1"}}
	results := map[string]subagents.Result{}
	queue := map[string]time.Time{}
	states := map[string]*RetryState{}
	ready := []subagents.Task{{ID: "t1", Name: "worker", SessionID: "sess-1"}}

	if err := c.startReady(h, ready, pending, results, queue, states); err != nil {
		t.Fatalf("startReady: %v", err)
	}
	// queueRecoveredRetry re-queued the task: removed from pending, present in
	// the retry queue, no failed/canceled result recorded.
	if _, ok := pending["t1"]; ok {
		t.Fatal("t1 still pending after startReady; queueRecoveredRetry should have re-queued it")
	}
	if _, retrying := queue["t1"]; !retrying {
		t.Fatal("t1 was not queued for retry by queueRecoveredRetry")
	}
	if _, ok := results["t1"]; ok {
		t.Fatalf("t1 unexpectedly recorded a result: %#v", results["t1"])
	}
	// The ledger must reflect the failed -> retry_pending transition.
	snap, err := repo.GetTask(ctx, runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusRetryPending) {
		t.Fatalf("ledger t1 status = %q, want %q", snap.Status, ledger.TaskStatusRetryPending)
	}
}

// flushRetriesGetTaskFailingRepo fails every GetTask, driving the read-failure
// branch of flushRetries (dag_retry.go:22-24).
type flushRetriesGetTaskFailingRepo struct {
	ledger.LedgerRepository
}

func (flushRetriesGetTaskFailingRepo) GetTask(context.Context, string, string) (ledger.TaskSnapshot, error) {
	return ledger.TaskSnapshot{}, errors.New("simulated retry task read failure")
}

// TestCoverageFlushRetriesReadFailureJoinsError pins dag_retry.go:23-24: a
// retry task whose ledger entry cannot be read joins an error into the run
// error and stays in the retry queue (the entry is NOT dropped on a read
// failure).
func TestCoverageFlushRetriesReadFailureJoinsError(t *testing.T) {
	repo := &flushRetriesGetTaskFailingRepo{LedgerRepository: ledger.NewMemoryLedgerRepository()}
	c := newIdempotencyCoordinator(repo).(*coordinator)
	h := c.newRunHandle("flush-read-run", "", map[string]string{}, "", false)

	pending := map[string]subagents.Task{}
	queue := map[string]time.Time{"t1": time.Now().Add(-time.Minute)} // backoff elapsed
	tasks := []subagents.Task{{ID: "t1", Name: "worker"}}

	err := c.flushRetries(h, tasks, pending, queue)
	if err == nil {
		t.Fatal("flushRetries: expected the read failure to be joined into the run error")
	}
	if !strings.Contains(err.Error(), `read retry task "t1"`) {
		t.Fatalf("flushRetries error = %v, want read retry task error", err)
	}
	if _, ok := queue["t1"]; !ok {
		t.Fatal("t1 was dropped from the retry queue despite the read failure")
	}
	if _, ok := pending["t1"]; ok {
		t.Fatal("t1 must not re-enter pending when its ledger entry cannot be read")
	}
}

// TestCoverageFlushRetriesDropsNonRetryPendingTask pins dag_retry.go:27-28: a
// retry queue entry whose ledger status is no longer retry_pending (e.g. the
// task was canceled while waiting out its backoff) is stale: flushRetries must
// drop it from the queue instead of re-queuing it.
func TestCoverageFlushRetriesDropsNonRetryPendingTask(t *testing.T) {
	ctx := context.Background()
	repo := ledger.NewMemoryLedgerRepository()
	c := newIdempotencyCoordinator(repo).(*coordinator)
	const runID = "flush-drop-run"
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now()
	if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: runID, TaskID: "t1", Status: string(ledger.TaskStatusCanceled), Version: 1, CreatedAt: now,
		Attempts: []ledger.AttemptSnapshot{{AttemptID: "attempt-1", TaskID: "t1", RunID: runID, AttemptNum: 1, StartedAt: now, Status: string(ledger.TaskStatusCanceled)}},
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	h := c.newRunHandle(runID, "", map[string]string{"t1": "attempt-1"}, "", false)

	pending := map[string]subagents.Task{}
	queue := map[string]time.Time{"t1": time.Now().Add(-time.Minute)}
	tasks := []subagents.Task{{ID: "t1", Name: "worker"}}

	if err := c.flushRetries(h, tasks, pending, queue); err != nil {
		t.Fatalf("flushRetries: %v", err)
	}
	if _, ok := queue["t1"]; ok {
		t.Fatal("stale retry entry with a non-retry_pending ledger status must be dropped")
	}
	if _, ok := pending["t1"]; ok {
		t.Fatal("t1 must not re-enter pending when its ledger status is not retry_pending")
	}
}

// flushRetriesCASFailingRepo fails every CompareAndSetTaskStatus, driving the
// re-queue CAS failure branch of flushRetries (dag_retry.go:30-32).
type flushRetriesCASFailingRepo struct {
	ledger.LedgerRepository
}

func (flushRetriesCASFailingRepo) CompareAndSetTaskStatus(context.Context, string, string, uint64, string) error {
	return errors.New("simulated re-queue CAS failure")
}

// TestCoverageFlushRetriesCASFailureJoinsError pins dag_retry.go:31-32: when
// the retry_pending -> queued re-queue CAS fails, the error is joined into the
// run error and the task stays in the retry queue (a failed CAS must not drop
// or re-pend the task).
func TestCoverageFlushRetriesCASFailureJoinsError(t *testing.T) {
	ctx := context.Background()
	repo := &flushRetriesCASFailingRepo{LedgerRepository: ledger.NewMemoryLedgerRepository()}
	c := newIdempotencyCoordinator(repo).(*coordinator)
	const runID = "flush-cas-run"
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now()
	if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: runID, TaskID: "t1", Status: string(ledger.TaskStatusRetryPending), Version: 1, CreatedAt: now,
		Attempts: []ledger.AttemptSnapshot{{AttemptID: "attempt-1", TaskID: "t1", RunID: runID, AttemptNum: 1, StartedAt: now, Status: string(ledger.TaskStatusRetryPending)}},
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	h := c.newRunHandle(runID, "", map[string]string{"t1": "attempt-1"}, "", false)

	pending := map[string]subagents.Task{}
	queue := map[string]time.Time{"t1": time.Now().Add(-time.Minute)}
	tasks := []subagents.Task{{ID: "t1", Name: "worker"}}

	err := c.flushRetries(h, tasks, pending, queue)
	if err == nil {
		t.Fatal("flushRetries: expected the CAS failure to be joined into the run error")
	}
	if !strings.Contains(err.Error(), `re-queue retry task "t1"`) {
		t.Fatalf("flushRetries error = %v, want re-queue retry task error", err)
	}
	if _, ok := queue["t1"]; !ok {
		t.Fatal("t1 was dropped from the retry queue despite the failed re-queue CAS")
	}
	if _, ok := pending["t1"]; ok {
		t.Fatal("t1 must not re-enter pending when the re-queue CAS failed")
	}
}

// TestCoverageFlushRetriesCASFailureReschedulesFuture pins dag_retry.go:38-50:
// when the retry_pending -> queued re-queue CAS fails, the error is joined
// into the run error, the task stays in the retry queue, AND the queue entry
// is rescheduled to a strictly-future probe time so earliestRequeue/waitForRetry
// sleep instead of returning nil immediately. Without the reschedule an elapsed
// requeueAt is left behind and the DAG loop busy-spins flushRetries at 100% CPU
// against a persistently failing CAS. Fails before the fix (requeueAt stays
// elapsed). Negative path: the failed CAS must not spin (future requeueAt), and
// must not drop or re-pend the task.
func TestCoverageFlushRetriesCASFailureReschedulesFuture(t *testing.T) {
	ctx := context.Background()
	repo := &flushRetriesCASFailingRepo{LedgerRepository: ledger.NewMemoryLedgerRepository()}
	c := newIdempotencyCoordinator(repo).(*coordinator)
	const runID = "flush-cas-probe-run"
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now()
	if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: runID, TaskID: "t1", Status: string(ledger.TaskStatusRetryPending), Version: 1, CreatedAt: now,
		Attempts: []ledger.AttemptSnapshot{{AttemptID: "attempt-1", TaskID: "t1", RunID: runID, AttemptNum: 1, StartedAt: now, Status: string(ledger.TaskStatusRetryPending)}},
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	h := c.newRunHandle(runID, "", map[string]string{"t1": "attempt-1"}, "", false)

	pending := map[string]subagents.Task{}
	queue := map[string]time.Time{"t1": time.Now().Add(-time.Minute)} // backoff elapsed
	tasks := []subagents.Task{{ID: "t1", Name: "worker"}}

	err := c.flushRetries(h, tasks, pending, queue)
	if err == nil {
		t.Fatal("flushRetries: expected the CAS failure to be joined into the run error")
	}
	if !strings.Contains(err.Error(), `re-queue retry task "t1"`) {
		t.Fatalf("flushRetries error = %v, want re-queue retry task error", err)
	}
	// (a) The task stays in the retry queue — a failed CAS must not drop it...
	if _, ok := queue["t1"]; !ok {
		t.Fatal("t1 was dropped from the retry queue despite the failed re-queue CAS")
	}
	// ...nor re-pend it.
	if _, ok := pending["t1"]; ok {
		t.Fatal("t1 must not re-enter pending when the re-queue CAS failed")
	}
	// (c) The rescheduled requeueAt must be strictly in the future so
	// earliestRequeue/waitForRetry sleep instead of returning nil immediately
	// (the elapsed-requeueAt busy-spin negative path).
	requeueAt := queue["t1"]
	if !requeueAt.After(c.nowLocked()) {
		t.Fatalf("requeueAt = %v, want strictly after now (an elapsed requeueAt left in place would busy-spin)", requeueAt)
	}
	if sleep := time.Until(earliestRequeue(queue)); sleep <= 0 {
		t.Fatalf("earliestRequeue sleep = %v, want > 0 (waitForRetry would return nil immediately and spin)", sleep)
	}
}

// flushRetriesAppendFailingRepo fails every AppendEvent, driving the
// retry-event append failure branch of flushRetries (dag_retry.go:44-45).
type flushRetriesAppendFailingRepo struct {
	ledger.LedgerRepository
}

func (flushRetriesAppendFailingRepo) AppendEvent(context.Context, ledger.LifecycleEvent) error {
	return errors.New("simulated retry event append failure")
}

// TestCoverageFlushRetriesAppendEventFailureJoinsError pins dag_retry.go:45:
// after the re-queue CAS succeeds, a failed task_retry_queued event append
// joins an error into the run error, but the task still re-enters the pending
// set and leaves the retry queue (the re-queue itself is durable via the CAS).
func TestCoverageFlushRetriesAppendEventFailureJoinsError(t *testing.T) {
	ctx := context.Background()
	repo := &flushRetriesAppendFailingRepo{LedgerRepository: ledger.NewMemoryLedgerRepository()}
	c := newIdempotencyCoordinator(repo).(*coordinator)
	const runID = "flush-append-run"
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now()
	if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: runID, TaskID: "t1", Status: string(ledger.TaskStatusRetryPending), Version: 1, CreatedAt: now,
		Attempts: []ledger.AttemptSnapshot{{AttemptID: "attempt-1", TaskID: "t1", RunID: runID, AttemptNum: 1, StartedAt: now, Status: string(ledger.TaskStatusRetryPending)}},
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	h := c.newRunHandle(runID, "", map[string]string{"t1": "attempt-1"}, "", false)

	pending := map[string]subagents.Task{}
	queue := map[string]time.Time{"t1": time.Now().Add(-time.Minute)}
	tasks := []subagents.Task{{ID: "t1", Name: "worker", SessionID: "sess-1"}}

	err := c.flushRetries(h, tasks, pending, queue)
	if err == nil {
		t.Fatal("flushRetries: expected the append failure to be joined into the run error")
	}
	if !strings.Contains(err.Error(), `append retry event "t1"`) {
		t.Fatalf("flushRetries error = %v, want append retry event error", err)
	}
	// The CAS (retry_pending -> queued) succeeded even though the event append
	// failed: the task re-enters pending with its original task preserved and
	// leaves the retry queue.
	if _, ok := pending["t1"]; !ok {
		t.Fatal("t1 did not re-enter pending after a successful re-queue CAS")
	}
	if got := pending["t1"].SessionID; got != "sess-1" {
		t.Fatalf("re-entered t1 SessionID = %q, want %q (original task must be preserved)", got, "sess-1")
	}
	if _, ok := queue["t1"]; ok {
		t.Fatal("t1 still in the retry queue after being re-queued")
	}
	snap, err := repo.GetTask(ctx, runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusQueued) {
		t.Fatalf("ledger t1 status = %q, want queued (re-queue CAS succeeded)", snap.Status)
	}
}

// TestCoverageWaitForRetryTimerFires covers the timer branch of waitForRetry:
// with no cancellation, the select takes timer.C after the backoff elapses and
// returns nil (the pool context is not canceled).
func TestCoverageWaitForRetryTimerFires(t *testing.T) {
	c := newIdempotencyCoordinator(ledger.NewMemoryLedgerRepository()).(*coordinator)
	h := c.newRunHandle("wait-timer-run", "", map[string]string{}, "", false)
	queue := map[string]time.Time{"t1": time.Now().Add(5 * time.Millisecond)}
	err := waitForRetry(h, queue)
	if err != nil {
		t.Fatalf("waitForRetry error = %v, want nil (timer fired, pool not canceled)", err)
	}
}

// TestCoverageWaitForRetryCanceledStopsTimer covers the pool-context branch of
// waitForRetry: when the run is canceled before the backoff elapses, the select
// takes poolCtx.Done() and the deferred timer Stop discards the pending timer.
func TestCoverageWaitForRetryCanceledStopsTimer(t *testing.T) {
	c := newIdempotencyCoordinator(ledger.NewMemoryLedgerRepository()).(*coordinator)
	h := c.newRunHandle("wait-cancel-run", "", map[string]string{}, "", false)
	h.cancel() // poolCtx is canceled before waitForRetry blocks
	queue := map[string]time.Time{"t1": time.Now().Add(time.Minute)}
	err := waitForRetry(h, queue)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForRetry error = %v, want context.Canceled", err)
	}
}
