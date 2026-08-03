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

// newNoRetryCancelFixture builds a coordinator under the production NoRetry
// retry config (New() defaults to DefaultRetryPolicy; production wiring applies
// NoRetry, which is what the R9 early fence sees) with a pool of two workers
// so a fast-failing/timing-out task and a still-running sibling execute
// concurrently. Handlers: "alwaysfail" fails immediately with a genuine error;
// "hold" blocks until its context is done (used both for the timed-out task,
// via a per-task Timeout, and for the sibling that keeps the run mid-flight
// until Cancel lands).
func newNoRetryCancelFixture(t *testing.T) (Coordinator, *ledger.MemoryLedgerRepository) {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "alwaysfail", staticHandler{err: errors.New("always fail")})
	_ = d.Register(runtime.Subagent, "hold", slowHandler{})
	p := subagents.New(d, subagents.Policy{Workers: 2})
	c := New(repo, p).WithRetryPolicy(NoRetry)
	return c, repo
}

// waitForTaskStatusByID polls Inspect until the task with id reaches status.
func waitForTaskStatusByID(t *testing.T, c Coordinator, h *RunHandle, id, status string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap, err := c.Inspect(context.Background(), h)
		if err != nil {
			t.Fatal(err)
		}
		if statusForSnapshotTask(snap, id) == status {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for task %s to reach %s; snapshot = %+v", id, status, snap)
		}
		select {
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// runErrHasCancelArtifact reports whether the run error carries the
// invalid-state-transition (or version-conflict) artifact this bug produced.
func runErrHasCancelArtifact(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "invalid state transition") || strings.Contains(msg, "version conflict")
}

// TestCancelNoRetryKeepsGenuineFailure is the R9 regression test (failed
// variant): under the production NoRetry config, the early finalize fence
// (Pool.OnTaskDone) CASes a genuinely-failed task to failed while the pool is
// still running. If the run is then canceled while a sibling still executes,
// markCanceledWithoutResults must consult the ledger and KEEP the durable
// failed outcome instead of overwriting it to canceled: overwriting made
// recordRunResults attempt a forbidden failed -> canceled CAS, polluting the
// run error with an "invalid state transition" artifact, leaving the result
// set disagreeing with the ledger, and emitting no task_failed event at all.
func TestCancelNoRetryKeepsGenuineFailure(t *testing.T) {
	c, repo := newNoRetryCancelFixture(t)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "fails", Name: "alwaysfail"},
		{ID: "sibling", Name: "hold"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	// The early fence must have durably CASed the failing task to failed while
	// the sibling is still executing (the run is mid-flight).
	waitForTaskStatusByID(t, c, h, "fails", string(ledger.TaskStatusFailed))

	cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Cancel(cancelCtx, h); err != nil {
		t.Fatal(err)
	}

	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// (a) The regression: the run error must not carry an invalid-state-
	// transition artifact from a forbidden failed -> canceled CAS.
	if runErrHasCancelArtifact(result.Err) {
		t.Fatalf("cancel after early-fenced failure polluted the run error: %v", result.Err)
	}

	// (b) The failed task's ledger status agrees with its result status: both
	// "failed" — the early CAS recorded the genuine failure durably.
	if got := statusForTaskID(result.Results, "fails"); got != "failed" {
		t.Fatalf("failed task result status = %q, want %q", got, "failed")
	}
	if got := statusForSnapshotTask(result.Snapshot, "fails"); got != string(ledger.TaskStatusFailed) {
		t.Fatalf("failed task ledger status = %q, want %q", got, string(ledger.TaskStatusFailed))
	}

	// (c) The still-running sibling is canceled in both ledger and result.
	if got := statusForTaskID(result.Results, "sibling"); got != "canceled" {
		t.Fatalf("sibling result status = %q, want %q", got, "canceled")
	}
	if got := statusForSnapshotTask(result.Snapshot, "sibling"); got != string(ledger.TaskStatusCanceled) {
		t.Fatalf("sibling ledger status = %q, want %q", got, string(ledger.TaskStatusCanceled))
	}

	// (d) Exactly one terminal event for the failed task: task_failed, emitted
	// by recordRunResults' short-circuit finalize (the early fence CAS is
	// deliberately silent).
	events, err := repo.ListEvents(context.Background(), h.runID)
	if err != nil {
		t.Fatal(err)
	}
	failedEvents := 0
	for _, evt := range events {
		if evt.TaskID != "fails" {
			continue
		}
		if evt.Kind == "task_failed" {
			failedEvents++
		}
		if evt.Kind == "task_canceled" {
			t.Fatalf("failed task got a task_canceled event despite a durable ledger failure: %+v", evt)
		}
	}
	if failedEvents != 1 {
		t.Fatalf("failed task terminal events = %d, want exactly 1 task_failed", failedEvents)
	}
}

// TestCancelNoRetryKeepsGenuineTimeout is the timed_out twin of
// TestCancelNoRetryKeepsGenuineFailure: a task that genuinely times out under
// NoRetry is early-fenced to timed_out; a cancel landing while a sibling still
// executes must keep that durable outcome (no invalid timed_out -> canceled
// CAS, exactly one task_timed_out event, ledger == result).
func TestCancelNoRetryKeepsGenuineTimeout(t *testing.T) {
	c, repo := newNoRetryCancelFixture(t)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "timesout", Name: "hold", Timeout: 50 * time.Millisecond},
		{ID: "sibling", Name: "hold"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	// The early fence must have durably CASed the timing-out task to timed_out
	// while the sibling is still executing (the run is mid-flight).
	waitForTaskStatusByID(t, c, h, "timesout", string(ledger.TaskStatusTimedOut))

	cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Cancel(cancelCtx, h); err != nil {
		t.Fatal(err)
	}

	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// (a) No invalid-state-transition artifact from a timed_out -> canceled CAS.
	if runErrHasCancelArtifact(result.Err) {
		t.Fatalf("cancel after early-fenced timeout polluted the run error: %v", result.Err)
	}

	// (b) Ledger status == result status, both "timed_out".
	if got := statusForTaskID(result.Results, "timesout"); got != "timed_out" {
		t.Fatalf("timed-out task result status = %q, want %q", got, "timed_out")
	}
	if got := statusForSnapshotTask(result.Snapshot, "timesout"); got != string(ledger.TaskStatusTimedOut) {
		t.Fatalf("timed-out task ledger status = %q, want %q", got, string(ledger.TaskStatusTimedOut))
	}

	// (c) The still-running sibling is canceled in both ledger and result.
	if got := statusForTaskID(result.Results, "sibling"); got != "canceled" {
		t.Fatalf("sibling result status = %q, want %q", got, "canceled")
	}
	if got := statusForSnapshotTask(result.Snapshot, "sibling"); got != string(ledger.TaskStatusCanceled) {
		t.Fatalf("sibling ledger status = %q, want %q", got, string(ledger.TaskStatusCanceled))
	}

	// (d) Exactly one terminal event for the timed-out task: task_timed_out.
	events, err := repo.ListEvents(context.Background(), h.runID)
	if err != nil {
		t.Fatal(err)
	}
	timedOutEvents := 0
	for _, evt := range events {
		if evt.TaskID != "timesout" {
			continue
		}
		if evt.Kind == "task_timed_out" {
			timedOutEvents++
		}
		if evt.Kind == "task_canceled" {
			t.Fatalf("timed-out task got a task_canceled event despite a durable ledger timeout: %+v", evt)
		}
	}
	if timedOutEvents != 1 {
		t.Fatalf("timed-out task terminal events = %d, want exactly 1 task_timed_out", timedOutEvents)
	}
}

// TestCancelNoRetryRunningTaskBecomesCanceled pins the pre-R9 cancel path under
// NoRetry: a task still RUNNING when Cancel lands must surface as canceled in
// both the ledger and the result set (running -> cancel_requested -> canceled)
// with a clean run error. This is the path markCanceledWithoutResults must not
// disturb: a running task is overwritten to canceled (or already carries a
// canceled result from the pool), never kept.
func TestCancelNoRetryRunningTaskBecomesCanceled(t *testing.T) {
	c, repo := newNoRetryCancelFixture(t)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "hold"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	// Wait until the task is mid-flight (running in the ledger).
	waitForTaskStatusByID(t, c, h, "t1", string(ledger.TaskStatusRunning))

	cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Cancel(cancelCtx, h); err != nil {
		t.Fatal(err)
	}

	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if runErrHasCancelArtifact(result.Err) {
		t.Fatalf("cancel of a running task polluted the run error: %v", result.Err)
	}
	if got := statusForTaskID(result.Results, "t1"); got != "canceled" {
		t.Fatalf("result status = %q, want %q", got, "canceled")
	}
	if got := statusForSnapshotTask(result.Snapshot, "t1"); got != string(ledger.TaskStatusCanceled) {
		t.Fatalf("ledger status = %q, want %q", got, string(ledger.TaskStatusCanceled))
	}
	if result.Snapshot.Status != ledger.RunStatusCanceled {
		t.Fatalf("run status = %q, want %q", result.Snapshot.Status, ledger.RunStatusCanceled)
	}

	// Exactly one terminal event for the canceled task.
	events, err := repo.ListEvents(context.Background(), h.runID)
	if err != nil {
		t.Fatal(err)
	}
	canceledEvents := 0
	for _, evt := range events {
		if evt.TaskID == "t1" && evt.Kind == "task_canceled" {
			canceledEvents++
		}
	}
	if canceledEvents != 1 {
		t.Fatalf("canceled task terminal events = %d, want exactly 1 task_canceled", canceledEvents)
	}
}
