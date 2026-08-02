package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

const (
	toolPostMessage = "post_message"
	toolRunMessages = "run_messages"

	// defaultQuestionWaitSec is the default wait_seconds for a question.
	defaultQuestionWaitSec = 60
)

// postMessageTool is the child-side upstream messaging tool (finding/question).
// It is NOT PrivilegedTool so ScopeSpawned may see it after baseline injection.
type postMessageTool struct {
	dispatcher *runtime.Dispatcher
	cfg        config.SubagentConfig
	repo       ledger.LedgerRepository
}

func (t *postMessageTool) Name() string { return toolPostMessage }

func (t *postMessageTool) Description() string {
	return "Post a structured message upstream from a running task. " +
		"kind \"finding\" records a durable discovery on the run blackboard without blocking. " +
		"kind \"question\" parks this task until answered or wait_seconds elapses " +
		"(structured no_answer on timeout). Not free-form chat: typed and budgeted only."
}

func (t *postMessageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind": map[string]any{
				"type":        "string",
				"description": "Message kind: finding or question",
				"enum":        []string{"finding", "question"},
			},
			"body": map[string]any{
				"type":        "string",
				"description": "Message body (bounded by messaging.max_body_bytes)",
			},
			"refs": map[string]any{
				"type":        "array",
				"description": "Optional ledger content refs (recorded, never re-minted)",
				"items":       map[string]any{"type": "string"},
			},
			"wait_seconds": map[string]any{
				"type":        "integer",
				"description": "For question only: max seconds to wait for an answer (default 60)",
			},
		},
		"required": []string{"kind", "body"},
	}
}

func (t *postMessageTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if !t.cfg.Messaging.IsEnabled() {
		return "", fmt.Errorf("messaging is disabled")
	}
	var in struct {
		Kind        string   `json:"kind"`
		Body        string   `json:"body"`
		Refs        []string `json:"refs"`
		WaitSeconds int      `json:"wait_seconds"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	kind := agentmsg.Kind(in.Kind)
	if kind != agentmsg.KindFinding && kind != agentmsg.KindQuestion {
		return "", fmt.Errorf("kind must be finding or question")
	}
	id, ok := runtime.TaskIdentityFrom(ctx)
	if !ok {
		return "", fmt.Errorf("post_message requires a running task identity")
	}
	c := initCoordinator(t.dispatcher, t.cfg, t.repo)

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
	// Cap wait to remaining context deadline when known.
	if dl, ok := ctx.Deadline(); ok {
		remain := int(time.Until(dl).Seconds())
		if remain > 0 && remain < waitSec {
			waitSec = remain
		}
	}

	// Reserve park BEFORE ledger announce so a racing parent answer cannot
	// miss DeliverAnswer (persist-then-announce still holds for the message).
	answerCh, unpark, err := c.ParkQuestion(id.RunID, id.TaskID, msg.ID)
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
		return "", err
	}
	if err := c.TransitionToAwaitingInput(ctx, id.RunID, id.TaskID); err != nil {
		return "", fmt.Errorf("park question: %w", err)
	}

	timer := time.NewTimer(time.Duration(waitSec) * time.Second)
	defer timer.Stop()
	select {
	case answer := <-answerCh:
		parked = false
		unpark()
		if err := c.TransitionFromAwaitingInput(ctx, id.RunID, id.TaskID, string(ledger.TaskStatusRunning)); err != nil {
			// Cancel may have won; still return the answer if we got one.
			if err != ledger.ErrConflict {
				return "", err
			}
		}
		out, _ := json.Marshal(map[string]any{
			"status":     "answered",
			"message_id": msg.ID,
			"answer":     answer,
		})
		return string(out), nil
	case <-timer.C:
		parked = false
		unpark()
		_ = c.TransitionFromAwaitingInput(ctx, id.RunID, id.TaskID, string(ledger.TaskStatusRunning))
		out, _ := json.Marshal(map[string]any{
			"status":     "no_answer",
			"reason":     "timed_out",
			"message_id": msg.ID,
		})
		return string(out), nil
	case <-ctx.Done():
		// Cancel-while-parked: leave terminal transition to cancel path;
		// best-effort unpark of status if still awaiting.
		parked = false
		unpark()
		_ = c.TransitionFromAwaitingInput(context.Background(), id.RunID, id.TaskID, string(ledger.TaskStatusRunning))
		return "", ctx.Err()
	}
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
		"(findings, questions, answers, steers). Returns synopsis entries; " +
		"full bodies are available via content_ref with ledger_read when needed."
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
	if !t.cfg.Messaging.IsEnabled() {
		return "", fmt.Errorf("messaging is disabled")
	}
	var in struct {
		RunID       string `json:"run_id"`
		TaskID      string `json:"task_id"`
		IncludeBody bool   `json:"include_body"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	// INV-AG-9: same principal gate as join_run / cancel_run.
	record, errJSON := accessibleOrchestrationHandle(ctx, in.RunID, t.dispatcher, t.repo)
	if errJSON != "" {
		return errJSON, nil
	}
	c := record.coord
	if c == nil {
		c = initCoordinator(t.dispatcher, t.cfg, t.repo)
	}
	msgs, err := c.ListRunMessages(ctx, in.RunID, in.TaskID)
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
			TaskID:     m.TaskID,
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

// sendToTaskTool is the parent-side tool for steer/answer delivery (plan 53.03).
type sendToTaskTool struct {
	dispatcher *runtime.Dispatcher
	cfg        config.SubagentConfig
	repo       ledger.LedgerRepository
}

func (t *sendToTaskTool) Name() string { return "send_to_task" }
func (t *sendToTaskTool) Privileged()  {}
func (t *sendToTaskTool) Description() string {
	return "Send a structured message to a running task in an orchestration run. " +
		"kind \"steer\" is unsolicited mid-task guidance delivered at the child's next step boundary. " +
		"kind \"answer\" replies to a parked question and unblocks the child. " +
		"Session-scoped only; gated by run principal (INV-AG-9)."
}

func (t *sendToTaskTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"run_id":  map[string]any{"type": "string", "description": "Orchestration run id"},
			"task_id": map[string]any{"type": "string", "description": "Target task id"},
			"kind":    map[string]any{"type": "string", "enum": []string{"steer", "answer"}, "description": "Message kind"},
			"body":    map[string]any{"type": "string", "description": "Message body"},
			"in_reply_to": map[string]any{
				"type": "string", "description": "Required for answer: question message id",
			},
		},
		"required": []string{"run_id", "task_id", "kind", "body"},
	}
}

func (t *sendToTaskTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if !t.cfg.Messaging.IsEnabled() {
		return "", fmt.Errorf("messaging is disabled")
	}
	var in struct {
		RunID     string `json:"run_id"`
		TaskID    string `json:"task_id"`
		Kind      string `json:"kind"`
		Body      string `json:"body"`
		InReplyTo string `json:"in_reply_to"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	record, errJSON := accessibleOrchestrationHandle(ctx, in.RunID, t.dispatcher, t.repo)
	if errJSON != "" {
		return errJSON, nil
	}
	kind := agentmsg.Kind(in.Kind)
	if kind != agentmsg.KindSteer && kind != agentmsg.KindAnswer {
		return "", fmt.Errorf("kind must be steer or answer")
	}
	msg, err := agentmsg.NewMessage(
		in.RunID, kind,
		agentmsg.Party{Role: agentmsg.ParentSentinel},
		agentmsg.Party{TaskID: in.TaskID},
		in.Body, nil,
		agentmsg.Options{
			MaxBodyBytes: t.cfg.Messaging.MaxBodyBytes,
			InReplyTo:    in.InReplyTo,
		},
	)
	if err != nil {
		return "", err
	}
	delivered, err := record.coord.SendToTask(ctx, record.handle, in.TaskID, msg)
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{
		"status":     "sent",
		"message_id": msg.ID,
		"delivered":  delivered,
	})
	return string(out), nil
}

// registerMessagingTools wires post_message (spawned baseline), run_messages
// and send_to_task (session-privileged). Called from session dispatcher setup.
func registerMessagingTools(d *runtime.Dispatcher, reg *tools.Registry, cfg config.SubagentConfig, repo ledger.LedgerRepository) error {
	post := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
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
		if err := registerSessionTool(d, reg, t); err != nil {
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
func injectBaselineMessaging(full, scoped *tools.Registry, cfg config.SubagentConfig, disallowed map[string]struct{}) {
	if full == nil || scoped == nil || !cfg.Messaging.IsEnabled() {
		return
	}
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
