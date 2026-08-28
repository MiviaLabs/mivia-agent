package clichat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/agents"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

const (
	toolPostMessage = "post_message"
	toolRunMessages = "run_messages"

	// defaultQuestionWaitSec is the default wait_seconds for a question. It sits
	// above the agent-loop tool-timeout floor (DefaultToolTimeout = 60s) so a
	// default question is not clamped away before a realistic parent answer can
	// arrive; the effective wait is still clamped to the enclosing tool/step
	// deadline by parkedWaitDuration.
	defaultQuestionWaitSec = 180

	// maxQuestionWaitSec is the advertised schema ceiling for wait_seconds. It
	// documents the bound for the model; the runtime wait is always clamped to
	// the enclosing tool/step budget regardless of the requested value.
	maxQuestionWaitSec = 3600
)

// postMessageTool is the child-side upstream messaging tool
// (finding/question/ask/answer). It is NOT PrivilegedTool so ScopeSpawned may
// see it after baseline injection. Messaging is always enabled.
type postMessageTool struct {
	dispatcher *runtime.Dispatcher
	cfg        config.SubagentConfig
	repo       ledger.LedgerRepository
	// referralSpawn starts a same-run referral task for RouteSpawn (tests may
	// inject a stub). Nil falls back to coordinator.SpawnReferralFromAsk.
	referralSpawn func(ctx context.Context, runID, toRole string, ask agentmsg.Message) (string, error)
}

func (t *postMessageTool) Name() string { return toolPostMessage }

func (t *postMessageTool) Description() string {
	return "Post a structured message from a running task. " +
		"kind \"finding\" records a durable discovery on the run blackboard without blocking. " +
		"kind \"question\" parks this task until the parent answers or wait_seconds elapses. " +
		"kind \"ask\" is a parent-routed one-shot referral to a same-run role (to_role required); " +
		"optional wait_seconds parks for the peer answer. " +
		"kind \"answer\" replies once to an open ask (in_reply_to required). " +
		"Not free-form chat: typed, budgeted, and attributable only."
}

func (t *postMessageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind": map[string]any{
				"type":        "string",
				"description": "Message kind: finding, question, ask, or answer",
				"enum":        []string{"finding", "question", "ask", "answer"},
			},
			"body": map[string]any{
				"type": "string",
				"description": "Claim plus its evidence pointer (file:line, command, run id) - " +
					"~4 sentences. Not restated task context or step narration. " +
					"Bounded by messaging.max_body_bytes; over the limit is rejected, " +
					"not truncated - shorten and retry.",
			},
			"refs": map[string]any{
				"type": "array",
				"description": "Optional content references backing the claim: opaque handles of the form " +
					"ref:<kind>:<digest>, copied VERBATIM from a prior tool result's output_ref/error_ref " +
					"or a run_messages entry's content_ref. Never invent one; never pass file paths, " +
					"package names, or message ids - put those in body. When you have no such handle, omit refs.",
				"items": map[string]any{"type": "string"},
			},
			"wait_seconds": map[string]any{
				"type":    "integer",
				"minimum": 0,
				"maximum": maxQuestionWaitSec,
				"description": fmt.Sprintf("For question (required wait) or ask (optional park): max seconds to wait "+
					"for an answer (0 = tool default %ds; capped at %ds). Waits beyond the "+
					"enclosing tool/step budget are clamped and end as no_answer",
					defaultQuestionWaitSec, maxQuestionWaitSec),
			},
			"to_role": map[string]any{
				"type":        "string",
				"description": "For ask: target agent role name in the same run",
			},
			"in_reply_to": map[string]any{
				"type":        "string",
				"description": "For answer: ask message id; for ask: optional prior ask id for chain depth",
			},
		},
		"required": []string{"kind", "body"},
	}
}

func (t *postMessageTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Kind        string   `json:"kind"`
		Body        string   `json:"body"`
		Refs        []string `json:"refs"`
		WaitSeconds int      `json:"wait_seconds"`
		ToRole      string   `json:"to_role"`
		InReplyTo   string   `json:"in_reply_to"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	kind := agentmsg.Kind(in.Kind)
	switch kind {
	case agentmsg.KindFinding, agentmsg.KindQuestion, agentmsg.KindAsk, agentmsg.KindAnswer:
	default:
		return "", fmt.Errorf("kind must be finding, question, ask, or answer")
	}
	id, ok := runtime.TaskIdentityFrom(ctx)
	if !ok {
		return "", fmt.Errorf("post_message requires a running task identity")
	}
	c := cliorchestrate.InitCoordinator(t.dispatcher, t.cfg, t.repo)

	if kind == agentmsg.KindAsk {
		return t.handleAsk(ctx, c, id, in.Body, in.Refs, in.ToRole, in.WaitSeconds, in.InReplyTo)
	}
	if kind == agentmsg.KindAnswer {
		return t.handlePeerAnswer(ctx, c, id, in.Body, in.InReplyTo)
	}

	// Build and validate before side effects (quota, park, ledger).
	msg, err := agentmsg.NewMessage(
		id.RunID, kind,
		agentmsg.Party{TaskID: id.TaskID, Agent: id.Agent},
		agentmsg.Party{Role: agentmsg.ParentSentinel},
		in.Body, in.Refs,
		agentmsg.Options{MaxBodyBytes: t.cfg.Messaging.MaxBodyBytes},
	)
	if err != nil {
		return "", err
	}
	// Server-side provenance: From is stamped from identity, not client input.
	msg.From = agentmsg.Party{TaskID: id.TaskID, Agent: id.Agent}

	if kind == agentmsg.KindFinding {
		if err := c.ConsumeMessageQuota(id.RunID, id.TaskID, t.cfg.Messaging.MaxMessagesPerTask); err != nil {
			return "", err
		}
		if err := c.PostTaskMessage(ctx, id.RunID, id.TaskID, msg); err != nil {
			// Refund the burned slot: a failed persist must never permanently
			// consume a message-budget slot (messageQuota is otherwise increment-only).
			c.RefundMessageQuota(id.RunID, id.TaskID)
			return "", err
		}
		out, _ := json.Marshal(map[string]any{
			"status":     "posted",
			"message_id": msg.ID,
			"kind":       string(kind),
		})
		return string(out), nil
	}
	return t.waitForAnswer(ctx, c, id, msg, in.WaitSeconds)
}

func (t *postMessageTool) waitForAnswer(ctx context.Context, c coordinator.Coordinator, id runtime.TaskIdentity, msg agentmsg.Message, waitSec int) (string, error) {
	if waitSec <= 0 {
		waitSec = defaultQuestionWaitSec
	}
	// Clamp the effective wait to the remaining context deadline
	// (min(waitSec, remaining)): a child-requested wait above the enclosing
	// tool/step budget must park only until that budget expires, then exit via
	// the clean no_answer JSON — never a raw ctx.Done() error. The park maxWait
	// uses the same clamped value so the registry TTL tracks the effective wait
	// instead of a stale full-length request.
	wait := parkedWaitDuration(ctx, waitSec)

	// Reserve park BEFORE ledger announce so a racing parent answer cannot
	// miss DeliverAnswer (persist-then-announce still holds for the message).
	// Tie the park expiry to the effective (clamped) wait so a long wait
	// (operator-raised tool deadline) is never evicted early by a parent's
	// DeliverAnswer.
	answerCh, unpark, err := c.ParkQuestion(id.RunID, id.TaskID, msg.ID, wait)
	if err != nil {
		// Another park is live — do NOT force awaiting_input → running.
		return "", err
	}
	// unpark only after we leave the wait (answer / timeout / cancel).
	parked := true
	defer func() {
		if parked {
			unpark()
		}
	}()

	if err := c.ConsumeMessageQuota(id.RunID, id.TaskID, t.cfg.Messaging.MaxMessagesPerTask); err != nil {
		return "", err
	}
	if err := c.PostTaskMessage(ctx, id.RunID, id.TaskID, msg); err != nil {
		// Refund the burned slot: a failed persist must never permanently
		// consume a message-budget slot (messageQuota is otherwise increment-only).
		c.RefundMessageQuota(id.RunID, id.TaskID)
		return "", err
	}
	// Best-effort: cancel may race; park registry is already held.
	_ = c.TransitionToAwaitingInput(ctx, id.RunID, id.TaskID)

	timer := time.NewTimer(wait)
	defer timer.Stop()
	// finishReceive retires the park for any value read off the channel: a
	// system decline sentinel reports no_answer; any other value is a real answer.
	finishReceive := func(answer string) (string, error) {
		if reason, ok := declineReason(answer); ok {
			return retireParkedWaitNoAnswer(c, id, &parked, unpark, msg.ID, reason)
		}
		return retireParkedWait(c, id, &parked, unpark, map[string]any{
			"status": "answered", "message_id": msg.ID, "answer": answer,
		})
	}
	select {
	case answer := <-answerCh:
		return finishReceive(answer)
	case <-timer.C:
		// Prefer parked answer/decline when both timer and channel are ready.
		if answer, ok := tryRecvAnswer(answerCh); ok {
			return finishReceive(answer)
		}
		return retireParkedWaitNoAnswer(c, id, &parked, unpark, msg.ID, "timed_out")
	case <-ctx.Done():
		// Prefer a ready answer/decline over the raw cancel error.
		if answer, ok := tryRecvAnswer(answerCh); ok {
			return finishReceive(answer)
		}
		// The enclosing tool/step deadline fired while parked (the wait was
		// clamped to that budget, or the requested wait elapsed in a timer
		// race): surface the documented no_answer result, not the raw
		// ctx.Err(). A genuine cancel must still propagate as an error.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return retireParkedWaitNoAnswer(c, id, &parked, unpark, msg.ID, "timed_out")
		}
		// Cancel-while-parked: leave terminal transition to the cancel path.
		return retireParkedWaitCancel(c, id, &parked, unpark, ctx.Err())
	}
}

// retireParkedWait retires a parked-question wait: clears the parked flag,
// unparks, best-effort transitions the task back to running, and renders the
// JSON result. Every terminal path of waitForAnswer routes through it so the
// park retirement and status transition stay in one place. The transition runs
// on context.Background() because the tool ctx may already be expired when the
// wait ends (a deadline-clamped timer race); the best-effort transition must
// not be blocked by the very deadline that ended the wait.
func retireParkedWait(c coordinator.Coordinator, id runtime.TaskIdentity, parked *bool, unpark func(), result map[string]any) (string, error) {
	*parked = false
	unpark()
	// Best-effort unpark; cancel may have won — still return the result.
	_ = c.TransitionFromAwaitingInput(context.Background(), id.RunID, id.TaskID, string(ledger.TaskStatusRunning))
	out, _ := json.Marshal(result)
	return string(out), nil
}

// retireParkedWaitNoAnswer retires a parked-question wait with a no_answer
// result: the wait ended without a real peer answer (parked timer expiry,
// deadline-clamped ctx expiry, or a system ask-decline sentinel). It renders
// the same no_answer JSON shape retireParkedWait produced for these paths.
func retireParkedWaitNoAnswer(c coordinator.Coordinator, id runtime.TaskIdentity, parked *bool, unpark func(), msgID, reason string) (string, error) {
	return retireParkedWait(c, id, parked, unpark, map[string]any{
		"status": "no_answer", "reason": reason, "message_id": msgID,
	})
}

// retireParkedWaitCancel retires a parked-question wait for a genuine cancel:
// clears the parked flag, unparks, best-effort returns the task to running
// (the terminal transition is left to the cancel path), and propagates the
// context error unchanged so a canceled task is never a no_answer park.
func retireParkedWaitCancel(c coordinator.Coordinator, id runtime.TaskIdentity, parked *bool, unpark func(), cause error) (string, error) {
	*parked = false
	unpark()
	_ = c.TransitionFromAwaitingInput(context.Background(), id.RunID, id.TaskID, string(ledger.TaskStatusRunning))
	return "", cause
}

// runMessagesTool is the parent-side pull tool for run message history.
// Privileged: session-scope only (INV-AG-9).
type runMessagesTool struct {
	dispatcher *runtime.Dispatcher
	cfg        config.SubagentConfig
	repo       ledger.LedgerRepository
}

func (t *runMessagesTool) Name() string { return toolRunMessages }

// Privileged marks this as a session-control tool (never ScopeSpawned).
func (t *runMessagesTool) Privileged() {}

func (t *runMessagesTool) Description() string {
	return "List structured messages posted during an orchestration run " +
		"(findings, questions, answers, steers, and ask declines). Returns " +
		"synopsis entries; full bodies are available via content_ref with " +
		"ledger_read when needed."
}

func (t *runMessagesTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"run_id": map[string]any{
				"type":        "string",
				"description": "Orchestration run id",
			},
			"task_id": map[string]any{
				"type":        "string",
				"description": "Optional: filter to one task",
			},
			"include_body": map[string]any{
				"type":        "boolean",
				"description": "When true, resolve content_ref bodies (bounded)",
			},
		},
		"required": []string{"run_id"},
	}
}

func (t *runMessagesTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		RunID       string `json:"run_id"`
		TaskID      string `json:"task_id"`
		IncludeBody bool   `json:"include_body"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	// INV-AG-9: same principal gate as join_run / cancel_run.
	record, errJSON := cliorchestrate.AccessibleOrchestrationHandle(ctx, in.RunID, t.dispatcher, t.repo)
	if errJSON != "" {
		return errJSON, nil
	}
	c := record.GetCoordinator()
	if c == nil {
		c = cliorchestrate.InitCoordinator(t.dispatcher, t.cfg, t.repo)
	}
	// One GetRun for both the incoming filter (a raw model id -> the real
	// task the run holds) and each returned message's TaskID (the real,
	// possibly namespaced id agentmsg stamped -> the model's own raw id) -
	// cliorchestrate.ResolveTaskID and ModelVisibleTaskID are exact
	// inverses of each other over the same task list.
	var tasks []ledger.TaskSnapshot
	if snap, err := t.repo.GetRun(ctx, in.RunID); err == nil {
		tasks = snap.Tasks
	}
	targetID := in.TaskID
	if targetID != "" {
		targetID = cliorchestrate.ResolveTaskID(tasks, targetID)
	}
	msgs, err := c.ListRunMessages(ctx, in.RunID, targetID)
	if err != nil {
		return "", err
	}
	type entry struct {
		MessageID  string `json:"message_id"`
		Kind       string `json:"kind"`
		Synopsis   string `json:"synopsis"`
		ContentRef string `json:"content_ref,omitempty"`
		TaskID     string `json:"task_id,omitempty"`
		Body       string `json:"body,omitempty"`
	}
	out := make([]entry, 0, len(msgs))
	for _, m := range msgs {
		e := entry{
			MessageID:  m.MessageID,
			Kind:       string(m.Kind),
			Synopsis:   m.Synopsis,
			ContentRef: m.ContentRef,
			TaskID:     cliorchestrate.ModelVisibleTaskID(tasks, m.TaskID),
		}
		if in.IncludeBody && m.ContentRef != "" {
			if full, err := c.LoadMessageBody(ctx, m.ContentRef); err == nil {
				e.Body = full.Body
			}
		}
		out = append(out, e)
	}
	raw, _ := json.Marshal(map[string]any{"messages": out})
	return string(raw), nil
}

// registerMessagingTools wires post_message (spawned baseline), run_messages
// and send_to_task (session-privileged). Called from session dispatcher setup.
// Messaging is always enabled. agentReg may be nil (tests); when set, referral
// spawns resolve AgentDigest for production agent handlers.
func registerMessagingTools(d *runtime.Dispatcher, reg *tools.Registry, cfg config.SubagentConfig, repo ledger.LedgerRepository, agentReg *agents.AgentRegistry) error {
	post := &postMessageTool{
		dispatcher: d, cfg: cfg, repo: repo,
		referralSpawn: func(ctx context.Context, runID, toRole string, ask agentmsg.Message) (string, error) {
			c := cliorchestrate.InitCoordinator(d, cfg, repo)
			var meta coordinator.ReferralSpawnMeta
			if agentReg != nil {
				if route, err := cliorchestrate.ResolveTaskRoute(agentReg, nil, toRole, ""); err == nil {
					meta.AgentDigest = route.Digest()
					// Provider/model left empty: pool uses session defaults when unset.
				}
			}
			return c.SpawnReferralFromAsk(ctx, runID, toRole, ask, meta)
		},
	}
	if _, exists := reg.Get(post.Name()); !exists {
		if err := d.RegisterTool(reg, post); err != nil {
			return fmt.Errorf("register post_message: %w", err)
		}
		reg.Register(post)
	}
	for _, t := range []tools.Tool{
		&runMessagesTool{dispatcher: d, cfg: cfg, repo: repo},
		&sendToTaskTool{dispatcher: d, cfg: cfg, repo: repo},
	} {
		if err := cliagents.RegisterSessionTool(d, reg, t); err != nil {
			if _, exists := reg.Get(t.Name()); !exists {
				return err
			}
		}
	}
	return nil
}

// injectBaselineMessaging ensures post_message is available on a spawned
// registry even when the agent allowlist would otherwise drop it. Agents opt
// out via disallowed_tools / tools_remove including "post_message".
// Messaging is always enabled.
func injectBaselineMessaging(full, scoped *tools.Registry, cfg config.SubagentConfig, disallowed map[string]struct{}) {
	if full == nil || scoped == nil {
		return
	}
	_ = cfg // budgets live on the tool instance already registered in full
	if _, deny := disallowed[toolPostMessage]; deny {
		return
	}
	if _, already := scoped.Get(toolPostMessage); already {
		return
	}
	if t, ok := full.Get(toolPostMessage); ok {
		scoped.Register(t)
	}
}
