package coordinator

import (
	"context"
)

// FindLiveTaskByRole returns the first non-terminal task in the run whose
// AgentName matches role (case-sensitive). Non-terminal is !IsTaskTerminal
// (queued, running, awaiting_input, retry_pending, cancel_requested, …).
// Queued same-role targets must be found so peer asks deliver via mailbox
// instead of referral-as-spawn (plan 53.04: parent panel with detached siblings).
func (c *coordinator) FindLiveTaskByRole(ctx context.Context, runID, role string) (taskID string, ok bool, err error) {
	if role == "" {
		return "", false, nil
	}
	tasks, err := c.repo.ListTasks(ctx, runID)
	if err != nil {
		return "", false, err
	}
	for _, t := range tasks {
		if t.AgentName != role {
			continue
		}
		if !IsTaskTerminal(t.Status) {
			return t.TaskID, true, nil
		}
	}
	return "", false, nil
}

// HandleForRun returns the active RunHandle for runID, if still registered.
func (c *coordinator) HandleForRun(runID string) *RunHandle {
	if runID == "" {
		return nil
	}
	c.handlesMu.Lock()
	defer c.handlesMu.Unlock()
	return c.handlesByRun[runID]
}

// RunID returns the handle's run id for tests and tools.
func (h *RunHandle) RunID() string {
	if h == nil {
		return ""
	}
	return h.runID
}
