package coordinator

import (
	"context"
	"encoding/json"
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
// further sends fail cleanly without close-on-terminal panics. Any asks that
// were delivered to this task's mailbox are declined to their parked askers
// (they will never be answered now that the task is terminal), gated on the
// ledger task status so a retry that is pending or queued is never declined.
func (h *RunHandle) MarkTaskMailboxTerminal(taskID string) {
	if h == nil || h.mailboxes == nil {
		return
	}
	// Drain undelivered messages first so the mailbox is not holding capacity
	// for a task that is now terminal.
	h.mailboxes.Drain(taskID)
	h.mailboxes.MarkTerminal(taskID)
	if h.owner != nil {
		h.owner.declineAsksForTerminalTask(h.runID, taskID)
	}
}

// declineAsksForTerminalTask retires asks that were delivered to a task which
// reached terminal status without answering, unblocking each parked asker with
// the wire-format decline sentinel instead of making it wait out the full
// wait_seconds. Gated on the ledger task status (IsTaskTerminal): a retry that
// is pending or queued must NOT be declined — the task will run again and may
// answer. Repo read errors fail safe (no decline; the asker timer handles it).
func (c *coordinator) declineAsksForTerminalTask(runID, taskID string) {
	if c == nil || c.repo == nil || runID == "" || taskID == "" {
		return
	}
	snap, err := c.repo.GetTask(context.Background(), runID, taskID)
	if err != nil {
		// Fail safe: cannot confirm terminal; let the asker timer handle it.
		return
	}
	if !IsTaskTerminal(snap.Status) {
		// retry_pending / queued / running etc: the task may run again.
		return
	}
	for _, askID := range c.asksTargeting(runID, taskID) {
		askerTaskID, ok := c.AskLookup(askID)
		if !ok {
			// Claimed/closed concurrently; a real answer wins over the decline.
			continue
		}
		// SealOpenAskAnswer (not SealAskAnswer) so a lost seal race never delivers
		// a decline on top of a real answer already in flight, and a concurrent
		// ClaimAskAnswer between AskLookup and the seal never loses the durable
		// real answer (a claimed ask means a real answer is mid-persist).
		if !c.SealOpenAskAnswer(askID) {
			continue
		}
		c.DeliverAnswer(runID, askerTaskID, askID, agentmsg.AskDeclinePrefix+agentmsg.DeclineReasonResponderTerminal)
		c.appendAskDeclinedEvent(runID, askerTaskID, askID)
	}
}

// declineAskDeliveredToTerminal declines a single ask whose mailbox delivery
// landed on an already-terminal task — the finalize fence ran before the
// byTarget record existed (the missed-decline window MailboxSend closes). Same
// gate and semantics as declineAsksForTerminalTask: gated on the ledger task
// status (IsTaskTerminal) so a retry that is pending/queued is never declined;
// idempotent so a sealed/claimed ask no-ops and a real answer in flight always
// wins.
func (c *coordinator) declineAskDeliveredToTerminal(runID, taskID, askID string) {
	if c == nil || c.repo == nil || runID == "" || taskID == "" || askID == "" {
		return
	}
	snap, err := c.repo.GetTask(context.Background(), runID, taskID)
	if err != nil {
		// Fail safe: cannot confirm terminal; let the asker timer handle it.
		return
	}
	if !IsTaskTerminal(snap.Status) {
		return
	}
	askerTaskID, ok := c.AskLookup(askID)
	if !ok {
		// Claimed/closed concurrently; a real answer wins over the decline.
		return
	}
	if !c.SealOpenAskAnswer(askID) {
		return
	}
	c.DeliverAnswer(runID, askerTaskID, askID, agentmsg.AskDeclinePrefix+agentmsg.DeclineReasonResponderTerminal)
	c.appendAskDeclinedEvent(runID, askerTaskID, askID)
}

// appendAskDeclinedEvent records a terminal ask decline for observability: a
// task_ask_declined lifecycle event attributed to the ASKER task/attempt so
// run_messages can surface it after the fact. Best-effort (the decline itself
// already happened; a failed append must not fail the finalize fence). Mirrors
// how other events append: c.repo.AppendEvent then c.emitLifecycleEvent.
func (c *coordinator) appendAskDeclinedEvent(runID, askerTaskID, askID string) {
	if c == nil || c.repo == nil || runID == "" || askerTaskID == "" || askID == "" {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"ask_id": askID,
		"reason": agentmsg.DeclineReasonResponderTerminal,
	})
	attemptID := ""
	if h := c.HandleForRun(runID); h != nil {
		attemptID = h.getAttempt(askerTaskID)
	}
	evt := ledger.LifecycleEvent{
		ID:        newEventID(),
		RunID:     runID,
		Kind:      LifecycleKindTaskAskDeclined,
		TaskID:    askerTaskID,
		AttemptID: attemptID,
		Payload:   payload,
		CreatedAt: c.nowLocked(),
	}
	if err := c.repo.AppendEvent(context.Background(), evt); err != nil {
		return
	}
	c.emitLifecycleEvent(evt)
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
