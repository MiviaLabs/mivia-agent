package coordinator

import (
	"context"
	"log"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// onTaskStart is installed on the subagent pool as Pool.OnTaskStart (types.go
// New). It runs on the pool worker goroutine right after the task's own
// cancelable execution context is derived, before dispatch, and registers
// that context's CancelFunc on the run handle. This is the sole write site
// for RunHandle.taskCancels: without it there is no per-task CancelFunc for
// CancelTask (cancel_task.go) to invoke, only the run-wide one Cancel uses.
//
// It also lazily CASes the task queued -> running here, the moment a pool
// worker actually reaches it. capReadyToPoolCapacity (dag.go) caps ONLY
// startReady's eager pre-dispatch CAS pass to the pool's worker count, not
// the batch buildBatch submits to pool.Run: the full ready set is always
// submitted, so Pool.execute's own worker-goroutine/jobs-channel pair
// (subagents.go) continuously admits the next ready task the instant any
// worker frees, instead of the DAG loop waiting for a whole wave to drain
// before it can even consider a capacity-capped task. A task beyond
// startReady's eager cap therefore reaches the pool still "queued" in the
// ledger; this is the only place that transitions it once real work
// actually starts. A failed CAS (already running from startReady's own
// eager pass - the common case - or claimed by a race this best-effort
// hygiene write does not need to win) is logged and otherwise ignored: the
// task's result still flows through the normal pool.Run -> processResults
// path regardless of this write's outcome, and GetTask's own read-then-CAS
// pattern (transitionTask, coordinator.go) already no-ops silently when the
// status already matches.
func (c *Coordinator) onTaskStart(ctx context.Context, t subagents.Task, cancel context.CancelFunc) {
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
	if err := c.transitionTask(h, t, string(ledger.TaskStatusRunning)); err != nil {
		log.Printf("coordinator: task %q lazy queued->running transition at dispatch: %v", id.TaskID, err)
	}
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
func (c *Coordinator) shouldSkipCanceledTask(ctx context.Context, _ subagents.Task) bool {
	if c == nil {
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
