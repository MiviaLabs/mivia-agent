package cli

import (
	"context"
	"encoding/json"
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
	answerCh, unpark, parked, err := parkBlockingAsk(c, id, msg.ID, blocking, dec)
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
	blocking bool, dec agentmsg.RouteDecision,
) (<-chan string, func(), bool, error) {
	if !blocking || dec.Action == agentmsg.RouteDecline {
		return nil, nil, false, nil
	}
	answerCh, unpark, err := c.ParkQuestion(id.RunID, id.TaskID, msgID)
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
			return "", fmt.Errorf("ask already answered")
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
	if err := c.PostTaskMessage(ctx, id.RunID, id.TaskID, msg); err != nil {
		c.UnclaimAskAnswer(inReplyTo, askerTask)
		return "", err
	}
	// Durable success: permanently retire (claim alone is not terminal).
	c.CloseAsk(inReplyTo)
	parked := c.DeliverAnswer(id.RunID, askerTask, inReplyTo, body)
	mailboxOK := false
	if h := c.HandleForRun(id.RunID); h != nil {
		mailboxOK, _ = c.MailboxSend(h, askerTask, msg)
	}
	// Durable answer exists; surface whether live delivery reached the asker.
	out, _ := json.Marshal(map[string]any{
		"status":      "answered",
		"message_id":  msg.ID,
		"in_reply_to": inReplyTo,
		"delivered":   parked || mailboxOK,
	})
	return string(out), nil
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
	if dl, ok := ctx.Deadline(); ok {
		remain := int(time.Until(dl).Seconds())
		if remain > 0 && remain < waitSec {
			waitSec = remain
		}
	}
	timer := time.NewTimer(time.Duration(waitSec) * time.Second)
	defer timer.Stop()
	select {
	case answer := <-answerCh:
		*parked = false
		unpark()
		// Retire ask for one-shot: peer path claims/closes; parent SendToTask
		// also CloseAsk after durable persist (idempotent if already closed).
		c.CloseAsk(msg.ID)
		_ = c.TransitionFromAwaitingInput(ctx, id.RunID, id.TaskID, string(ledger.TaskStatusRunning))
		out, _ := json.Marshal(map[string]any{
			"status": "answered", "message_id": msg.ID, "answer": answer,
		})
		return string(out), nil
	case <-timer.C:
		*parked = false
		unpark()
		c.CloseAsk(msg.ID)
		_ = c.TransitionFromAwaitingInput(ctx, id.RunID, id.TaskID, string(ledger.TaskStatusRunning))
		out, _ := json.Marshal(map[string]any{
			"status": "no_answer", "reason": "timed_out", "message_id": msg.ID,
		})
		return string(out), nil
	case <-ctx.Done():
		*parked = false
		unpark()
		c.CloseAsk(msg.ID)
		_ = c.TransitionFromAwaitingInput(context.Background(), id.RunID, id.TaskID, string(ledger.TaskStatusRunning))
		return "", ctx.Err()
	}
}
