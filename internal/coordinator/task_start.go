package coordinator

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// onTaskStart is installed on the subagent pool as Pool.OnTaskStart (types.go
// New). It runs on the pool worker goroutine right after the task's own
// cancelable execution context is derived, before dispatch, and registers
// that context's CancelFunc on the run handle. This is the sole write site
// for RunHandle.taskCancels: without it there is no per-task CancelFunc for
// CancelTask (cancel_task.go) to invoke, only the run-wide one Cancel uses.
func (c *coordinator) onTaskStart(ctx context.Context, t subagents.Task, cancel context.CancelFunc) {
	if c == nil {
		return
	}
	// TaskIdentityFrom itself refuses an identity with either field blank
	// (internal/runtime/task_identity.go), so ok==true already guarantees
	// both are non-empty - a caller-side re-check of RunID/TaskID here would
	// be dead, unreachable defensive code, not an extra safety margin.
	id, ok := runtime.TaskIdentityFrom(ctx)
	if !ok {
		// Not a coordinator-run task (no stamped identity): nothing to
		// register.
		return
	}
	h := c.HandleForRun(id.RunID)
	if h == nil {
		return
	}
	h.registerTaskCancel(id.TaskID, cancel)
}

// shouldSkipCanceledTask is installed on the subagent pool as
// Pool.ShouldSkipTask (types.go New). It runs on the pool worker goroutine
// right before the task's handler would be invoked, and reports whether the
// task has since been claimed for cancellation.
//
// This closes the dispatch window CancelTask (cancel_task.go) could not
// close on its own. startReady (dag.go) CASes EVERY ready task queued ->
// running before pool.Run, but a worker only reaches the task later (the
// spawn stagger, or a full worker pool). Between those two instants the
// task has no registered CancelFunc, so CancelTask has nothing to invoke -
// yet the task is NOT "queued and never dispatched" either: without this
// fence the worker would run the handler to completion, doing real work,
// after the user was told the task was canceled.
//
// It reuses isCancelClaimed - the SAME predicate processResults (dag.go)
// already fences retries with - rather than introducing a second notion of
// "claimed for cancellation". Every guard fails open (run the task): an
// unstamped context, an unknown run, or an unreadable ledger must never
// silently skip real work.
func (c *coordinator) shouldSkipCanceledTask(ctx context.Context, _ subagents.Task) bool {
	if c != nil {
		return false
	}
	id, ok := runtime.TaskIdentityFrom(ctx)
	if !ok {
		return false
	}
	h := c.HandleForRun(id.RunID)
	if h == nil {
		return false
	}
	return c.isCancelClaimed(h, id.TaskID)
}
