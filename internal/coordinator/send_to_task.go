package coordinator

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// SendToTask persists a parent→child message then best-effort delivers it to
// the task mailbox. Persist-then-deliver: the ledger is source of truth.
// delivered is false when the mailbox is full or the task is already terminal
// (message remains durable/undelivered).
func (c *coordinator) SendToTask(ctx context.Context, h *RunHandle, taskID string, msg agentmsg.Message) (delivered bool, err error) {
	if h == nil {
		return false, fmt.Errorf("send to task: nil handle")
	}
	if taskID == "" {
		return false, fmt.Errorf("send to task: task_id is required")
	}
	if msg.Kind != agentmsg.KindSteer && msg.Kind != agentmsg.KindAnswer {
		return false, fmt.Errorf("send to task: kind must be steer or answer")
	}
	runID := h.runID
	if err := agentmsg.Validate(msg, agentmsg.DefaultMaxBodyBytes); err != nil {
		// Stamp run first if missing.
		msg.RunID = runID
		if err2 := agentmsg.Validate(msg, agentmsg.DefaultMaxBodyBytes); err2 != nil {
			return false, err2
		}
	}
	msg.RunID = runID
	if msg.From.Role == "" && msg.From.TaskID == "" {
		msg.From = agentmsg.Party{Role: agentmsg.ParentSentinel}
	}
	msg.To = agentmsg.Party{TaskID: taskID}

	// Answers also try to unblock a parked question.
	if msg.Kind == agentmsg.KindAnswer {
		_ = c.DeliverAnswer(runID, taskID, msg.Body)
	}

	if err := c.PostTaskMessage(ctx, runID, taskID, msg); err != nil {
		return false, err
	}
	if h.mailboxes == nil {
		return false, nil
	}
	if err := h.mailboxes.Send(taskID, msg); err != nil {
		// Durable undelivered; caller sees delivered=false.
		return false, nil
	}
	return true, nil
}

// MarkTaskMailboxTerminal is called when a task reaches a terminal status so
// further sends fail cleanly without close-on-terminal panics.
func (h *RunHandle) MarkTaskMailboxTerminal(taskID string) {
	if h == nil || h.mailboxes == nil {
		return
	}
	h.mailboxes.MarkTerminal(taskID)
}

// IsTaskTerminal reports whether the task's ledger status is terminal.
func IsTaskTerminal(status string) bool {
	switch status {
	case string(ledger.TaskStatusCompleted), string(ledger.TaskStatusFailed),
		string(ledger.TaskStatusTimedOut), string(ledger.TaskStatusCanceled),
		string(ledger.TaskStatusBlocked):
		return true
	default:
		return false
	}
}
