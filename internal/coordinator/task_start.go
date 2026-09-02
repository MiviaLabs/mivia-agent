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
