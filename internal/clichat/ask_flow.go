package clichat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// routingPolicyFromConfig maps TOML routing into pure agentmsg policy.
func routingPolicyFromConfig(cfg config.MessagingRoutingConfig) agentmsg.RoutingPolicy {
	return agentmsg.RoutingPolicy{
		Mode:                    cfg.Mode,
		MaxAsksPerTask:          cfg.MaxAsksPerTask,
		MaxReferralDepth:        cfg.MaxReferralDepth,
		Allow:                   append([]string(nil), cfg.Allow...),
		MaxReferralSpawnsPerRun: cfg.MaxReferralSpawnsPerRun,
	}
}

// handleAsk runs parent-routed Ask (plan 53.04). Returns tool JSON result.
func (t *postMessageTool) handleAsk(
	ctx context.Context,
	c coordinator.Coordinator,
	id runtime.TaskIdentity,
	body string,
	refs []string,
	toRole string,
	waitSec int,
	inReplyTo string,
) (string, error) {
	toRole = strings.TrimSpace(toRole)
	fromRole := strings.TrimSpace(id.Agent)
	if fromRole == "" {
		fromRole = "agent"
	}
	if toRole == "" {
		return "", fmt.Errorf("ask requires to_role")
	}
	blocking := waitSec > 0
	dec, liveID, ancestors, err := t.decideAskRoute(ctx, c, id, fromRole, toRole, blocking, inReplyTo)
	if err != nil {
		return "", err
	}
	msg, err := t.mintAskMessage(id, fromRole, toRole, body, refs, inReplyTo)
	if err != nil {
		return "", err
	}
	answerCh, unpark, parked, err := parkBlockingAsk(c, id, msg.ID, blocking, waitSec, dec)
	if err != nil {
		return "", err
	}
	if parked {
		defer func() {
			if parked {
				unpark()
			}
		}()
	}
	if err := c.ConsumeMessageQuota(id.RunID, id.TaskID, t.cfg.Messaging.MaxMessagesPerTask); err != nil {
		return "", err
	}
	if err := c.PostTaskMessage(ctx, id.RunID, id.TaskID, msg); err != nil {
		// Refund the burned slot: a failed persist must never permanently
		// consume a message-budget slot (messageQuota is otherwise increment-only).
		c.RefundMessageQuota(id.RunID, id.TaskID)
		return "", err
	}
	if dec.Action == agentmsg.RouteDecline {
		return declineAfterPersist(msg.ID, dec.Reason), nil
	}
	// RouteAsk already enforced max_asks; TryRegisterAsk is atomic vs concurrent posts.
	if !c.TryRegisterAsk(id.RunID, id.TaskID, fromRole, msg.ID, ancestors, t.cfg.Messaging.Routing.MaxAsksPerTask) {
		// Concurrent loser: another ask won the last slot after RouteAsk checked.
		return declineAfterPersist(msg.ID, agentmsg.DeclineQuotaExceeded), nil
	}
	if routeErr := t.applyAskRoute(ctx, c, id, toRole, liveID, msg, dec); routeErr != nil {
		return routeErr.result, routeErr.err
	}
	if !blocking {
		out, _ := json.Marshal(map[string]any{
			"status": "posted", "message_id": msg.ID, "route": string(dec.Action),
		})
		return string(out), nil
	}
	if err := c.TransitionToAwaitingInput(ctx, id.RunID, id.TaskID); err != nil {
		// Deliver already happened; close ask so peers cannot answer into a void.
		c.CloseAsk(msg.ID)
		return "", fmt.Errorf("park ask: %w", err)
	}
	return t.waitOnParkedAnswer(ctx, c, id, msg, waitSec, answerCh, &parked, unpark)
}

func (t *postMessageTool) mintAskMessage(
	id runtime.TaskIdentity, fromRole, toRole, body string, refs []string, inReplyTo string,
) (agentmsg.Message, error) {
	msg, err := agentmsg.NewMessage(
		id.RunID, agentmsg.KindAsk,
		agentmsg.Party{TaskID: id.TaskID, Agent: id.Agent, Role: fromRole},
		agentmsg.Party{Role: toRole},
		body, refs,
		agentmsg.Options{MaxBodyBytes: t.cfg.Messaging.MaxBodyBytes, InReplyTo: inReplyTo},
	)
	if err != nil {
		return agentmsg.Message{}, err
	}
	msg.From = agentmsg.Party{TaskID: id.TaskID, Agent: id.Agent, Role: fromRole}
	msg.To = agentmsg.Party{Role: toRole}
	return msg, nil
}

func parkBlockingAsk(
	c coordinator.Coordinator, id runtime.TaskIdentity, msgID string,
	blocking bool, waitSec int, dec agentmsg.RouteDecision,
) (<-chan string, func(), bool, error) {
	if !blocking || dec.Action == agentmsg.RouteDecline {
		return nil, nil, false, nil
	}
	// Tie the park expiry to the asker's effective max wait so a legitimately
	// parked asker (waitSec > parkTTL, e.g. an operator-raised tool deadline) is
	// never evicted early by a peer's DeliverAnswer.
	answerCh, unpark, err := c.ParkQuestion(id.RunID, id.TaskID, msgID, time.Duration(waitSec)*askWaitUnit)
	if err != nil {
		return nil, nil, false, err
	}
	return answerCh, unpark, true, nil
}

type askRouteErr struct {
	result string
	err    error
}

func (e *askRouteErr) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return e.result
}

func (t *postMessageTool) decideAskRoute(
	ctx context.Context,
	c coordinator.Coordinator,
	id runtime.TaskIdentity,
	fromRole, toRole string,
	blocking bool,
	inReplyTo string,
) (dec agentmsg.RouteDecision, liveID string, ancestors []string, err error) {
	depth, cycle, ancestors := c.AskChainInfo(inReplyTo, toRole)
	liveID, live, err := c.FindLiveTaskByRole(ctx, id.RunID, toRole)
	if err != nil {
		return dec, "", nil, err
	}
	dec = agentmsg.RouteAsk(routingPolicyFromConfig(t.cfg.Messaging.Routing), agentmsg.RouteInput{
		FromRole:           fromRole,
		ToRole:             toRole,
		Blocking:           blocking,
		TargetRunning:      live,
		AsksUsedByTask:     c.AsksUsedByTask(id.RunID, id.TaskID),
		ReferralSpawnsUsed: c.ReferralSpawnsUsed(id.RunID),
		ChainDepth:         depth,
		Cycle:              cycle,
	})
	return dec, liveID, ancestors, nil
}

func (t *postMessageTool) applyAskRoute(
	ctx context.Context,
	c coordinator.Coordinator,
	id runtime.TaskIdentity,
	toRole, liveID string,
	msg agentmsg.Message,
	dec agentmsg.RouteDecision,
) *askRouteErr {
	switch dec.Action {
	case agentmsg.RouteDeliver:
		h := c.HandleForRun(id.RunID)
		if h == nil {
			c.CloseAsk(msg.ID)
			return &askRouteErr{result: declineAfterPersist(msg.ID, agentmsg.DeclineTargetNotRunning)}
		}
		deliver := msg
		deliver.To = agentmsg.Party{TaskID: liveID}
		delivered, err := c.MailboxSend(h, liveID, deliver)
		if err != nil || !delivered {
			c.CloseAsk(msg.ID)
			return &askRouteErr{result: declineAfterPersist(msg.ID, agentmsg.DeclineTargetNotRunning)}
		}
	case agentmsg.RouteSpawn:
		maxSpawn := t.cfg.Messaging.Routing.MaxReferralSpawnsPerRun
		if !c.TryIncReferralSpawn(id.RunID, maxSpawn) {
			c.CloseAsk(msg.ID)
			return &askRouteErr{result: declineAfterPersist(msg.ID, agentmsg.DeclineSpawnQuotaExceeded)}
		}
		spawnFn := t.referralSpawn
		if spawnFn == nil {
			spawnFn = func(ctx context.Context, runID, role string, ask agentmsg.Message) (string, error) {
				return c.SpawnReferralFromAsk(ctx, runID, role, ask)
			}
		}
		if _, spawnErr := spawnFn(ctx, id.RunID, toRole, msg); spawnErr != nil {
			c.DecReferralSpawn(id.RunID)
			c.CloseAsk(msg.ID)
			return &askRouteErr{result: declineAfterPersist(msg.ID, agentmsg.DeclineInvalid)}
		}
	}
	return nil
}

func declineAfterPersist(messageID, reason string) string {
	out, _ := json.Marshal(map[string]any{
		"status": "declined", "reason": reason, "message_id": messageID,
	})
	return string(out)
}

// handlePeerAnswer routes a child answer to an open ask (one-shot).
func (t *postMessageTool) handlePeerAnswer(
	ctx context.Context,
	c coordinator.Coordinator,
	id runtime.TaskIdentity,
	body string,
	inReplyTo string,
) (string, error) {
	if strings.TrimSpace(inReplyTo) == "" {
		return "", fmt.Errorf("answer requires in_reply_to")
	}
	// Peek open asker without claiming so validation can fail without retiring the ask.
	askerTask, ok := c.AskLookup(inReplyTo)
	if !ok {
		if c.IsAskAnswered(inReplyTo) {
			// Sealed before claim: the asker's park already timed out (or the
			// ask was otherwise closed), so this answer can never be delivered
			// and is NOT persisted. Surface a structured diagnostic instead of
			// an opaque error so the relay can tell "asker timed out; I'm done"
			// from a real bug (mirrors the undelivered notice style below).
			return buildAnsweredNotice(inReplyTo), nil
		}
		return "", fmt.Errorf("unknown or closed ask %q", inReplyTo)
	}
	// Validate envelope before claim so body budget failures do not burn the ask.
	msg, err := agentmsg.NewMessage(
		id.RunID, agentmsg.KindAnswer,
		agentmsg.Party{TaskID: id.TaskID, Agent: id.Agent},
		agentmsg.Party{TaskID: askerTask},
		body, nil,
		agentmsg.Options{MaxBodyBytes: t.cfg.Messaging.MaxBodyBytes, InReplyTo: inReplyTo},
	)
	if err != nil {
		return "", err
	}
	msg.From = agentmsg.Party{TaskID: id.TaskID, Agent: id.Agent}

	// Claim immediately before durable side effects (one-shot).
	askerTask, err = c.ClaimAskAnswer(inReplyTo)
	if err != nil {
		return "", err
	}
	if err := c.ConsumeMessageQuota(id.RunID, id.TaskID, t.cfg.Messaging.MaxMessagesPerTask); err != nil {
		c.UnclaimAskAnswer(inReplyTo, askerTask)
		return "", err
	}
	// Waiter timeout/cancel may CloseAsk while we hold claim — refuse before persist.
	if c.IsAskAnswered(inReplyTo) {
		// FIX P2 (pinned): this sealed check runs AFTER ConsumeMessageQuota and
		// the answer is never persisted, so the burned slot must be refunded or
		// a max_messages_per_task=1 task wedges permanently. Unclaim mirrors
		// the post-failure branch below; it is a no-op on a permanently closed
		// ask (timeout CloseAsk wins over reopen — see TestUnclaimDoesNotReopenAfterTimeoutClose).
		c.RefundMessageQuota(id.RunID, id.TaskID)
		c.UnclaimAskAnswer(inReplyTo, askerTask)
		return "", fmt.Errorf("ask already answered")
	}
	if err := c.PostTaskMessage(ctx, id.RunID, id.TaskID, msg); err != nil {
		// Refund the burned slot: a failed persist must never permanently
		// consume a message-budget slot (messageQuota is otherwise increment-only).
		c.RefundMessageQuota(id.RunID, id.TaskID)
		c.UnclaimAskAnswer(inReplyTo, askerTask)
		return "", err
	}
	// Atomic seal wins inject: if waiter already sealed, skip DeliverAnswer/mailbox.
	if !c.SealAskAnswer(inReplyTo) {
		return "", fmt.Errorf("ask already answered")
	}
	parked := c.DeliverAnswer(id.RunID, askerTask, inReplyTo, body)
	mailboxOK := false
	if h := c.HandleForRun(id.RunID); h != nil {
		mailboxOK, _ = c.MailboxSend(h, askerTask, msg)
	}
	// Durable answer exists; surface whether live delivery reached the asker.
	delivered := parked || mailboxOK
	return buildAnsweredResult(msg.ID, inReplyTo, delivered), nil
}

// buildAnsweredNotice renders the structured diagnostic returned when an answer
// arrives for an ask that was already sealed (park timeout/close) before the
// claim: the answer can never be delivered and is NOT persisted. The structured
// result lets the relay tell "asker timed out; I'm done" from a real bug
// (mirrors the undelivered notice style below).
func buildAnsweredNotice(inReplyTo string) string {
	out, _ := json.Marshal(map[string]any{
		"status":      "answered",
		"delivered":   false,
		"notice":      "asker already timed out; answer not recorded",
		"in_reply_to": inReplyTo,
	})
	return string(out)
}

// buildAnsweredResult renders the durable-answer result, appending an
// undelivered notice when live delivery (park or mailbox) did not reach the
// asker: the answer is recorded durably in the run ledger but may never reach
// the asker. No step-boundary delivery is promised - a false MailboxSend means
// the message never entered the mailbox, so there is no later drain to inject it.
func buildAnsweredResult(messageID, inReplyTo string, delivered bool) string {
	res := map[string]any{
		"status":      "answered",
		"message_id":  messageID,
		"in_reply_to": inReplyTo,
		"delivered":   delivered,
	}
	if !delivered {
		res["notice"] = "asker not live; answer recorded durably in the run ledger (visible via run_messages) and may not reach the asker"
	}
	out, _ := json.Marshal(res)
	return string(out)
}

// waitOnParkedAnswer waits for DeliverAnswer / timeout / cancel.
func (t *postMessageTool) waitOnParkedAnswer(
	ctx context.Context,
	c coordinator.Coordinator,
	id runtime.TaskIdentity,
	msg agentmsg.Message,
	waitSec int,
	answerCh <-chan string,
	parked *bool,
	unpark func(),
) (string, error) {
	if waitSec <= 0 {
		waitSec = defaultQuestionWaitSec
	}
	timer := time.NewTimer(parkedWaitDuration(ctx, waitSec, askWaitUnit))
	defer timer.Stop()
	// finishReceive retires the park for any value read off the channel. A
	// system decline sentinel (AskDeclinePrefix) is reported as no_answer with
	// the stripped reason, exactly like the timer branch's cleanup; any other
	// value is a real peer answer. Every receive path (primary case plus the
	// timer/ctx drains) routes through it so a racing decline wins over
	// timed_out / raw cancel error.
	finishReceive := func(answer string) (string, error) {
		if reason, ok := declineReason(answer); ok {
			*parked = false
			unpark()
			c.CloseAsk(msg.ID)
			_ = c.TransitionFromAwaitingInput(ctx, id.RunID, id.TaskID, string(ledger.TaskStatusRunning))
			out, _ := json.Marshal(map[string]any{
				"status": "no_answer", "reason": reason, "message_id": msg.ID,
			})
			return string(out), nil
		}
		*parked = false
		unpark()
		// Peer/parent may already SealAskAnswer; CloseAsk is idempotent.
		c.CloseAsk(msg.ID)
		_ = c.TransitionFromAwaitingInput(ctx, id.RunID, id.TaskID, string(ledger.TaskStatusRunning))
		out, _ := json.Marshal(map[string]any{
			"status": "answered", "message_id": msg.ID, "answer": answer,
		})
		return string(out), nil
	}
	select {
	case answer := <-answerCh:
		return finishReceive(answer)
	case <-timer.C:
		// Prefer parked answer/decline when both timer and channel are ready (or late).
		if answer, ok := tryRecvAnswer(answerCh); ok {
			return finishReceive(answer)
		}
		*parked = false
		unpark()
		c.CloseAsk(msg.ID)
		_ = c.TransitionFromAwaitingInput(ctx, id.RunID, id.TaskID, string(ledger.TaskStatusRunning))
		out, _ := json.Marshal(map[string]any{
			"status": "no_answer", "reason": "timed_out", "message_id": msg.ID,
		})
		return string(out), nil
	case <-ctx.Done():
		if answer, ok := tryRecvAnswer(answerCh); ok {
			return finishReceive(answer)
		}
		// The enclosing tool/step deadline fired while parked (the wait was
		// clamped to that budget, or the deadline passed during park setup):
		// surface the documented no_answer result, never the raw ctx.Err().
		// Mirrors the question path's deadline handling (waitForAnswer) and
		// the timer.C branch's cleanup. A genuine cancel must still propagate
		// as an error — a canceled task is never a no_answer park.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			c.CloseAsk(msg.ID)
			return retireParkedWaitNoAnswer(c, id, parked, unpark, msg.ID, "timed_out")
		}
		*parked = false
		unpark()
		c.CloseAsk(msg.ID)
		_ = c.TransitionFromAwaitingInput(context.Background(), id.RunID, id.TaskID, string(ledger.TaskStatusRunning))
		return "", ctx.Err()
	}
}

// askWaitUnit is the unit for wait_seconds (seconds in production). Tests may
// shrink it so dual-ready timer/channel races finish quickly.
var askWaitUnit = time.Second

// tryRecvAnswer non-blocking drain of a parked answer channel.
func tryRecvAnswer(ch <-chan string) (string, bool) {
	select {
	case a := <-ch:
		return a, true
	default:
		return "", false
	}
}
