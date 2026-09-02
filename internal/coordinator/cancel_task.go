package coordinator

import (
	"context"
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
			// The task's executeOne call has not unwound within budget
			// (e.g. blocked on something that ignores ctx). Fall through to
			// finalize the ledger anyway: cancel_requested -> canceled is a
			// valid transition regardless, and onTaskDone's own cancel guard
			// (task_done.go) means it will never race this finalize once
			// the worker does eventually return.
		}
	}
	// Still queued (never dispatched, no CancelFunc registered): the DAG's
	// own startReady (dag.go) observes cancel_requested via isCancelClaimed
	// and never dispatches it, settling it in its in-memory results map  -
	// but that never touches the ledger row. Finalize below either way.

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
			continue // status moved concurrently; re-read and retry
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
