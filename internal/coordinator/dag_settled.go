package coordinator

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// settledTaskResult reads the task's current ledger state and, when another
// executor already drove it to a terminal status, returns that durable
// outcome as the task's result. Non-terminal states (and read failures)
// return ok=false so startReady's later branches handle them.
func (c *coordinator) settledTaskResult(h *RunHandle, taskID string) (subagents.Result, bool) {
	snap, err := c.repo.GetTask(h.poolContext(), h.runID, taskID)
	if err != nil || !IsTaskTerminal(snap.Status) {
		return subagents.Result{}, false
	}
	result := subagents.Result{TaskID: taskID, Status: snap.Status}
	if snap.Status != string(ledger.TaskStatusCompleted) {
		result.Err = fmt.Errorf("task settled as %s by another executor", snap.Status)
	}
	return result, true
}
