package coordinator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// taskCancelPollInterval mirrors the 25ms poll cadence recovery_reclaim.go
// already uses for run-claim polling (recovery_reclaim.go:124).
// taskCancelWaitBudget bounds how long CancelTask waits for a dispatched
// task's own executeOne call to unwind (after its context is canceled)
// before finalizing the ledger itself, and how long finalizeSingleTaskCancel
// spends reconciling a concurrent CAS conflict.
const (
	taskCancelPollInterval = 25 * time.Millisecond
	taskCancelWaitBudget   = 5 * time.Second
)

// ErrTaskCancelNotStopped reports that a per-task cancel was requested and
// durably recorded (the task sits at cancel_requested), the task's execution
// context was canceled, and the task STILL had not unwound within
// taskCancelWaitBudget. The task is very probably still running.
//
// It is a distinct outcome from success on purpose: CancelTask must never
// report success, nor write a terminal canceled row, for a task it cannot
// show has stopped. A caller should tell the user the task is still running,
// not that it was canceled.
var ErrTaskCancelNotStopped = errors.New("task did not stop within the cancel wait budget; it is still running")

// CancelTask cancels exactly ONE task within a run, leaving the run's other
// in-flight tasks and the run itself untouched. This is deliberately NOT
// Cancel scoped to one task: Cancel cancels h.poolCtx (the run-wide
// CancelFunc) and waits on h.done (the run's completion signal) - both would
// take down every sibling task. CancelTask instead invokes only the named
// task's own execution context CancelFunc (registered by onTaskStart from
// the per-task context subagents.Pool.executeOne already derives for timeout
// enforcement) and waits only for that task's own completion signal
// (signalTaskDone, via onTaskDone), never h.done.
//
// A recovered handle refuses: cancelRecovered's fail-closed reasoning
// (cancel.go) is that a recovered run may have NO live in-process owner  -
// its "running" task may belong to a different executor, or none - so this
// process holds no CancelFunc for it at all. Silently no-oping there would
// claim a cancellation that never happened; CancelTask reports that clearly
// instead of weakening the whole-run recovered-cancel safety property.
//
// Contract: a nil error means the task is settled - it is terminal, or it
// demonstrably stopped and this call wrote its terminal canceled row.
// ErrTaskCancelNotStopped means the request is durably recorded but the task
// did not stop within taskCancelWaitBudget, so nothing terminal was written.
// Any other error is a failure of the cancel itself.
func (c *coordinator) CancelTask(ctx context.Context, h *RunHandle, taskID string) error {
	if err := c.validateHandle(h); err != nil {
		return err
	}
	if h.recovered {
		return fmt.Errorf("cannot cancel single task %q on recovered run %q: no live execution owner in this process", taskID, h.runID)
	}

	settled, err := c.requestSingleTaskCancel(ctx, h, taskID)
	if err != nil {
		return err
	}
	if settled {
		// Already terminal: safe no-op (requirement 2 - an already-terminal
		// task must not error or hang).
		return nil
	}

	if cancelFn, done, ok := h.taskCancelFunc(taskID); ok && cancelFn != nil {
		// Dispatched: stop its own execution context. Safe to call
		// concurrently with defer cancel() in executeOne - CancelFunc is
		// idempotent and safe for repeated/concurrent invocation.
		cancelFn()
		select {
		case <-done:
		case <-time.After(taskCancelWaitBudget):
			// The task's executeOne call has NOT unwound within budget
			// (e.g. a tool that ignores ctx). Report that instead of
			// finalizing: writing a terminal canceled row here told the
			// user - and the ledger, and the run's own result set - that a
			// task had stopped while it demonstrably kept running, and its
			// output was then silently discarded at run end.
			//
			// The task stays at cancel_requested, which is exactly right: it
			// is a live cancellation request nothing has satisfied yet. The
			// existing run-end paths (recordRunResults' cancel override, or
			// reconcileCancellation) still finalize it as canceled once the
			// task really does stop.
			return fmt.Errorf("cancel task %q on run %q: %w", taskID, h.runID, ErrTaskCancelNotStopped)
		}
	}
	// Still queued (never dispatched, no CancelFunc registered): the DAG's
	// own startReady (dag.go) observes cancel_requested via isCancelClaimed
	// and never dispatches it, settling it in its in-memory results map  -
	// but that never touches the ledger row. Finalize below either way.
	//
	// A task already inside a dispatched batch but not yet picked up by a
	// worker also has no CancelFunc. That one is NOT settled by this call's
	// ledger write alone: the pool's own pre-dispatch fence
	// (Pool.ShouldSkipTask -> shouldSkipCanceledTask, task_start.go) sees the
	// cancel_requested row and returns a canceled result without ever running
	// the handler.

	return c.finalizeSingleTaskCancel(h, taskID)
}

// requestSingleTaskCancel drives a task from queued/running/awaiting_input to
// cancel_requested, retrying on a version conflict (a concurrent dispatch or
// terminal write) until the task settles or truly needs the request. Returns
// settled=true when the task is already terminal (nothing more to do) and
// settled=false with a nil error once cancel_requested is durably recorded
// (by this call, or a concurrent one that raced ahead).
func (c *coordinator) requestSingleTaskCancel(ctx context.Context, h *RunHandle, taskID string) (settled bool, err error) {
	for {
		snap, err := c.repo.GetTask(ctx, h.runID, taskID)
		if err != nil {
			return false, fmt.Errorf("task %q not found in run %q: %w", taskID, h.runID, err)
		}
		if IsTaskTerminal(snap.Status) {
			return true, nil
		}
		if snap.Status == string(ledger.TaskStatusCancelRequested) {
			return false, nil
		}
		if snap.Status != string(ledger.TaskStatusQueued) &&
			snap.Status != string(ledger.TaskStatusRunning) &&
			snap.Status != string(ledger.TaskStatusAwaitingInput) {
			return false, fmt.Errorf("task %q has status %q, which cannot be canceled", taskID, snap.Status)
		}
		casErr := c.repo.CompareAndSetTaskStatus(ctx, h.runID, taskID, snap.Version, string(ledger.TaskStatusCancelRequested))
		if casErr == nil {
			reqEvt := ledger.LifecycleEvent{
				ID: newEventID(), RunID: h.runID, Kind: "task_cancel_requested",
				TaskID: taskID, AttemptID: h.getAttempt(taskID),
			}
			_ = c.repo.AppendEvent(ctx, reqEvt) // best-effort; state IS persisted via CAS
			c.emitLifecycleEvent(reqEvt)
			return false, nil
		}
		if casErr == ledger.ErrConflict {
			// Status moved concurrently: re-read and retry. Deliberately
			// without finalizeSingleTaskCancel's deadline. Every conflict
			// means another writer advanced the row's version, and a task's
			// status walk is monotone towards terminal, so this loop makes
			// progress on each pass and exits at the first terminal or
			// cancel_requested read. Adding a deadline here would swap a
			// bounded retry for a new failure mode, not remove one.
			continue
		}
		return false, fmt.Errorf("request cancel for task %q: %w", taskID, casErr)
	}
}

// finalizeSingleTaskCancel drives one task from cancel_requested (or queued,
// if it never even reached that CAS because a concurrent path already moved
// it) to canceled, mirroring reconcileCancellation's run-end finalize sweep
// (cancel.go) but scoped to one task and reachable while sibling tasks keep
// executing - it never waits on h.done. onTaskDone's own cancel guard
// (task_done.go: it bails when the ledger already shows
// cancel_requested/canceled) guarantees this is the only path that ever
// finalizes a task this call put into cancel_requested, so there is no race
// with the pool worker's own early-CAS fence.
func (c *coordinator) finalizeSingleTaskCancel(h *RunHandle, taskID string) error {
	persistCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	deadline := time.Now().Add(taskCancelWaitBudget)
	for {
		cur, err := c.repo.GetTask(persistCtx, h.runID, taskID)
		if err != nil {
			return fmt.Errorf("read task %q after cancel: %w", taskID, err)
		}
		if IsTaskTerminal(cur.Status) {
			// Settled terminal already, by this call's own earlier attempt
			// racing a retry, or by the DAG's own path. Either way there is
			// nothing left to finalize.
			return nil
		}
		if cur.Status != string(ledger.TaskStatusCancelRequested) && cur.Status != string(ledger.TaskStatusQueued) {
			// Some other non-terminal status appeared (should not happen on
			// the paths CancelTask reaches here from); nothing more to do
			// without risking an invalid transition.
			return nil
		}
		casErr := c.repo.CompareAndSetTaskStatus(persistCtx, h.runID, taskID, cur.Version, string(ledger.TaskStatusCanceled))
		if casErr == nil {
			return c.recordCancellation(persistCtx, h, cur)
		}
		if casErr != ledger.ErrConflict {
			return fmt.Errorf("finalize cancel for task %q: %w", taskID, casErr)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("finalize cancel for task %q: timed out reconciling concurrent updates", taskID)
		}
		time.Sleep(taskCancelPollInterval)
	}
}
