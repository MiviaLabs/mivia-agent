package cliorchestrate

import (
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// taskProgressInfo is the mid-run liveness block inspect_agents attaches to
// one running task: tool-call counts and last-activity age from the run's
// coordinator-side buffer (cap-proof counters - see
// coordinator.runToolCallBuffer.progress). A zero block on a long-running
// task (dispatched, no tool call ever observed) and a stale LastActivity age
// are the two wedge signals the orchestrator prompt's timeout-based rule
// keys on.
type taskProgressInfo struct {
	ToolCalls    int    `json:"tool_calls"`
	LastTool     string `json:"last_tool,omitempty"`
	LastActivity string `json:"last_activity_age,omitempty"`
}

// taskProgressInfoFor renders one task's progress block. Attached for every
// non-terminal task - including a zero block, which is itself the
// "dispatched but never got to its first tool call" wedge signal - and
// omitted for terminal tasks, whose recorded outcome supersedes liveness.
func taskProgressInfoFor(task ledger.TaskSnapshot, progress map[string]coordinator.TaskProgress, now time.Time) *taskProgressInfo {
	switch ledger.TaskStatus(task.Status) {
	case ledger.TaskStatusCompleted, ledger.TaskStatusFailed,
		ledger.TaskStatusTimedOut, ledger.TaskStatusCanceled,
		ledger.TaskStatusBlocked:
		return nil
	}
	p := progress[task.TaskID]
	info := &taskProgressInfo{ToolCalls: p.ToolCalls, LastTool: p.LastTool}
	if !p.LastActivity.IsZero() {
		info.LastActivity = now.Sub(p.LastActivity).Round(time.Second).String()
	}
	return info
}
