package coordinator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestCancelTask_RetryPendingStatusRefuses pins requestSingleTaskCancel's
// "cannot be canceled" branch: a task in retry_pending is neither terminal
// (IsTaskTerminal excludes it) nor one of queued/running/awaiting_input,
// the one status shape existing tests do not drive. Seeded directly via
// the repo and a bare RunHandle, mirroring
// TestCoverageFlushRetriesReadFailureReschedulesFuture's fixture, since
// driving a real retry through the pool has no available knob here.
func TestCancelTask_RetryPendingStatusRefuses(t *testing.T) {
	ctx := context.Background()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	const runID = "retry-pending-cancel-run"
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: runID, TaskID: "t1", Status: string(ledger.TaskStatusRetryPending), Version: 1, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	h := c.newRunHandle(runID, "", map[string]string{"t1": "attempt-1"}, "", false)

	err := c.CancelTask(ctx, h, "t1")
	if err == nil || !strings.Contains(err.Error(), "cannot be canceled") {
		t.Fatalf("CancelTask on a retry_pending task = %v, want a cannot-be-canceled error", err)
	}
}

// requestErrorOnceRepo fails CompareAndSetTaskStatus exactly once for one
// (taskID, newStatus) pair with a non-conflict error, then delegates.
// Mirrors cancel_task_test.go's finalizeErrorOnceRepo but targets
// requestSingleTaskCancel's own CAS (to cancel_requested) rather than
// finalizeSingleTaskCancel's (to canceled).
type requestErrorOnceRepo struct {
	ledger.LedgerRepository
	taskID, toStatus string
	err              error
	fired            bool
}

func (r *requestErrorOnceRepo) CompareAndSetTaskStatus(ctx context.Context, runID, taskID string, expectedVersion uint64, newStatus string) error {
	if !r.fired && taskID == r.taskID && newStatus == r.toStatus {
		r.fired = true
		return r.err
	}
	return r.LedgerRepository.CompareAndSetTaskStatus(ctx, runID, taskID, expectedVersion, newStatus)
}

// TestRequestSingleTaskCancel_NonConflictCASErrorSurfaces pins
// requestSingleTaskCancel's own CAS-failure wrap, distinct from
// TestFinalizeSingleTaskCancelNonConflictErrorSurfaces (which targets the
// CAS to canceled, not cancel_requested).
func TestRequestSingleTaskCancel_NonConflictCASErrorSurfaces(t *testing.T) {
	ctx := context.Background()
	base := ledger.NewMemoryLedgerRepository()
	const runID = "request-cas-error-run"
	if err := base.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := base.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: runID, TaskID: "t1", Status: string(ledger.TaskStatusQueued), Version: 1, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom: storage unavailable")
	repo := &requestErrorOnceRepo{
		LedgerRepository: base, taskID: "t1",
		toStatus: string(ledger.TaskStatusCancelRequested), err: boom,
	}
	d := runtime.New(runtime.Policy{})
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	h := c.newRunHandle(runID, "", map[string]string{"t1": "attempt-1"}, "", false)

	err := c.CancelTask(ctx, h, "t1")
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("CancelTask() error = %v, want it to wrap %v", err, boom)
	}
}

// TestCancelTask_GetTaskErrorSurfaces pins requestSingleTaskCancel's own
// GetTask-error wrap.
func TestCancelTask_GetTaskErrorSurfaces(t *testing.T) {
	repo := &errGetTaskRepo{LedgerRepository: ledger.NewMemoryLedgerRepository(), failGetTask: true}
	d := runtime.New(runtime.Policy{})
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	h := c.newRunHandle("some-run", "", map[string]string{"t1": "attempt-1"}, "", false)

	if err := c.CancelTask(context.Background(), h, "t1"); err == nil {
		t.Fatal("CancelTask accepted a GetTask failure")
	}
}

// TestRequestSingleTaskCancel_AlreadyCancelRequestedIsANoop pins the
// already-cancel_requested short-circuit in requestSingleTaskCancel: a
// concurrent caller (or a retried CancelTask) already moved the task there,
// so this call must not re-attempt the CAS and must fall through to
// finalizeSingleTaskCancel cleanly.
func TestRequestSingleTaskCancel_AlreadyCancelRequestedIsANoop(t *testing.T) {
	ctx := context.Background()
	repo := ledger.NewMemoryLedgerRepository()
	const runID = "already-cancel-requested-run"
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: runID, TaskID: "t1", Status: string(ledger.TaskStatusCancelRequested), Version: 1, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	d := runtime.New(runtime.Policy{})
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	h := c.newRunHandle(runID, "", map[string]string{"t1": "attempt-1"}, "", false)

	if err := c.CancelTask(ctx, h, "t1"); err != nil {
		t.Fatalf("CancelTask() on an already-cancel_requested task = %v, want nil", err)
	}
}

// secondGetTaskErrorRepo fails the Nth GetTask call (1-indexed) with a fixed
// error, delegating every other call. Used to target
// finalizeSingleTaskCancel's own GetTask (the second call CancelTask makes)
// separately from requestSingleTaskCancel's first one.
type secondGetTaskErrorRepo struct {
	ledger.LedgerRepository
	failOnCall int
	calls      int
	err        error
}

func (r *secondGetTaskErrorRepo) GetTask(ctx context.Context, runID, taskID string) (ledger.TaskSnapshot, error) {
	r.calls++
	if r.calls == r.failOnCall {
		return ledger.TaskSnapshot{}, r.err
	}
	return r.LedgerRepository.GetTask(ctx, runID, taskID)
}

// TestFinalizeSingleTaskCancel_GetTaskErrorSurfaces pins
// finalizeSingleTaskCancel's own GetTask-error wrap, distinct from
// requestSingleTaskCancel's (TestCancelTask_GetTaskErrorSurfaces above),
// by letting the first GetTask (in requestSingleTaskCancel) succeed and
// only failing the second (in finalizeSingleTaskCancel).
func TestFinalizeSingleTaskCancel_GetTaskErrorSurfaces(t *testing.T) {
	ctx := context.Background()
	base := ledger.NewMemoryLedgerRepository()
	const runID = "finalize-gettask-error-run"
	if err := base.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := base.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: runID, TaskID: "t1", Status: string(ledger.TaskStatusQueued), Version: 1, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom: storage unavailable")
	repo := &secondGetTaskErrorRepo{LedgerRepository: base, failOnCall: 2, err: boom}
	d := runtime.New(runtime.Policy{})
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	h := c.newRunHandle(runID, "", map[string]string{"t1": "attempt-1"}, "", false)

	err := c.CancelTask(ctx, h, "t1")
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("CancelTask() error = %v, want it to wrap %v", err, boom)
	}
}

// alwaysConflictCASRepo makes every CompareAndSetTaskStatus call to
// toStatus return ledger.ErrConflict, forcing finalizeSingleTaskCancel's
// retry loop to run out its taskCancelWaitBudget and hit its own timeout
// wrap.
type alwaysConflictCASRepo struct {
	ledger.LedgerRepository
	toStatus string
}

func (r *alwaysConflictCASRepo) CompareAndSetTaskStatus(ctx context.Context, runID, taskID string, expectedVersion uint64, newStatus string) error {
	if newStatus == r.toStatus {
		return ledger.ErrConflict
	}
	return r.LedgerRepository.CompareAndSetTaskStatus(ctx, runID, taskID, expectedVersion, newStatus)
}

// TestFinalizeSingleTaskCancel_TimesOutReconcilingConcurrentUpdates pins
// finalizeSingleTaskCancel's deadline-exceeded wrap: every CAS to canceled
// conflicts, so the retry loop must give up once taskCancelWaitBudget
// elapses rather than spinning forever. Runs the real 5s budget - slow but
// deterministic, no shortcuts into unexported timing constants.
func TestFinalizeSingleTaskCancel_TimesOutReconcilingConcurrentUpdates(t *testing.T) {
	ctx := context.Background()
	base := ledger.NewMemoryLedgerRepository()
	const runID = "finalize-timeout-run"
	if err := base.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := base.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: runID, TaskID: "t1", Status: string(ledger.TaskStatusQueued), Version: 1, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	repo := &alwaysConflictCASRepo{LedgerRepository: base, toStatus: string(ledger.TaskStatusCanceled)}
	d := runtime.New(runtime.Policy{})
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	h := c.newRunHandle(runID, "", map[string]string{"t1": "attempt-1"}, "", false)

	err := c.CancelTask(ctx, h, "t1")
	if err == nil || !strings.Contains(err.Error(), "timed out reconciling concurrent updates") {
		t.Fatalf("CancelTask() error = %v, want a reconciliation timeout", err)
	}
}
