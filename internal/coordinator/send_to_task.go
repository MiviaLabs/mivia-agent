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
//
// Ordering is load-bearing for answers: claim registry asks before persist,
// then PostTaskMessage, CloseAsk, DeliverAnswer, mailbox Send. Unblocking a
// parked child before persist would let the child resume with no durable
// answer if the ledger write failed. Registry asks participate in one-shot
// claim (INV one answer per ask); phase-03 question ids are not registry asks
// and keep post-then-deliver without claim.
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
	msg.RunID = runID
	if msg.From.Role == "" && msg.From.TaskID == "" {
		msg.From = agentmsg.Party{Role: agentmsg.ParentSentinel}
	}
	msg.To = agentmsg.Party{TaskID: taskID}

	var claimedAsker string
	var holdClaim bool
	if msg.Kind == agentmsg.KindAnswer && msg.InReplyTo != "" {
		asker, claimed, claimErr := c.BeginAskAnswer(msg.InReplyTo)
		if claimErr != nil {
			return false, claimErr
		}
		if claimed {
			holdClaim = true
			claimedAsker = asker
		}
	}

	// Persist (includes body-budget validation via c.maxBodyBytes).
	if err := c.PostTaskMessage(ctx, runID, taskID, msg); err != nil {
		if holdClaim {
			c.UnclaimAskAnswer(msg.InReplyTo, claimedAsker)
		}
		return false, err
	}

	if msg.Kind == agentmsg.KindAnswer {
		if holdClaim {
			// Atomic seal: only the sealer may live-inject (timeout race-safe).
			if !c.SealAskAnswer(msg.InReplyTo) {
				return false, fmt.Errorf("ask already answered")
			}
		}
		_ = c.DeliverAnswer(runID, taskID, msg.InReplyTo, msg.Body)
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
