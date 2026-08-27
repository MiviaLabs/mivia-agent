package clichat

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// sendToTaskTool is the parent-side tool for steer/answer delivery (plan 53.03).
type sendToTaskTool struct {
	dispatcher *runtime.Dispatcher
	cfg        config.SubagentConfig
	repo       ledger.LedgerRepository
}

func (t *sendToTaskTool) Name() string { return "send_to_task" }
func (t *sendToTaskTool) Privileged()  {}
func (t *sendToTaskTool) Description() string {
	return "Send a structured message to tasks in an orchestration run. " +
		"Target a single task with task_id, or broadcast to several with task_ids (1+). " +
		"kind \"steer\" is unsolicited mid-task guidance delivered at the child's next step boundary. " +
		"Set interrupt on a steer to break into a long in-flight LLM call instead of waiting for the next step boundary. " +
		"kind \"answer\" replies to a parked question and unblocks the child. " +
		"Broadcast returns a per-task delivered/error map; a failure on one child does not fail the call. " +
		"Session-scoped only; gated by run principal (INV-AG-9)."
}

func (t *sendToTaskTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"run_id":  map[string]any{"type": "string", "description": "Orchestration run id"},
			"task_id": map[string]any{"type": "string", "description": "Target task id (single target; mutually exclusive with task_ids)"},
			"task_ids": map[string]any{
				"type":        "array",
				"description": "Target task ids for broadcast (1+; mutually exclusive with task_id)",
				"items":       map[string]any{"type": "string"},
				"minItems":    1,
			},
			"kind": map[string]any{"type": "string", "enum": []string{"steer", "answer"}, "description": "Message kind"},
			"body": map[string]any{
				"type": "string",
				"description": "Claim plus its evidence pointer (file:line, command, run id) - " +
					"~4 sentences, not restated task context. Over messaging.max_body_bytes " +
					"is rejected, not truncated - shorten and retry.",
			},
			"interrupt": map[string]any{
				"type":        "boolean",
				"description": "(steer only) break into a long in-flight LLM call instead of waiting for the next step boundary",
			},
			"in_reply_to": map[string]any{
				"type": "string", "description": "Required for answer: question message id",
			},
		},
		"required":             []string{"run_id", "kind", "body"},
		"additionalProperties": false,
	}
}

// sendToTaskParams is the decoded input for send_to_task. Task targeting is
// exactly one of task_id (single) or task_ids (broadcast, 1+).
type sendToTaskParams struct {
	RunID     string   `json:"run_id"`
	TaskID    string   `json:"task_id"`
	TaskIDs   []string `json:"task_ids"`
	Kind      string   `json:"kind"`
	Body      string   `json:"body"`
	InReplyTo string   `json:"in_reply_to"`
	Interrupt bool     `json:"interrupt"`
}

func (t *sendToTaskTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in sendToTaskParams
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	// Exactly one target selector. task_ids must be non-empty when present so
	// an explicitly empty broadcast is rejected, not silently ignored.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	_, taskIDsProvided := raw["task_ids"]
	switch {
	case in.TaskID != "" && len(in.TaskIDs) > 0:
		return "", fmt.Errorf("task_id and task_ids are mutually exclusive; provide exactly one")
	case taskIDsProvided && len(in.TaskIDs) == 0:
		return "", fmt.Errorf("task_ids must contain at least one task_id")
	case in.TaskID == "" && len(in.TaskIDs) == 0:
		return "", fmt.Errorf("exactly one of task_id or task_ids is required")
	}
	record, errJSON := cliorchestrate.AccessibleOrchestrationHandle(ctx, in.RunID, t.dispatcher, t.repo)
	if errJSON != "" {
		return errJSON, nil
	}
	kind := agentmsg.Kind(in.Kind)
	if kind != agentmsg.KindSteer && kind != agentmsg.KindAnswer {
		return "", fmt.Errorf("kind must be steer or answer")
	}
	// The interrupt flag is a mid-step steer-only signal: it is meaningless on
	// an answer and must be rejected (matches agentmsg.Validate). Structured
	// error envelope so the caller can machine-read the rejection.
	if in.Interrupt && kind != agentmsg.KindSteer {
		return `{"error":"interrupt requires kind steer"}`, nil
	}
	if len(in.TaskIDs) > 0 {
		return t.broadcastToTasks(ctx, record, in, kind)
	}
	msg, err := agentmsg.NewMessage(
		in.RunID, kind,
		agentmsg.Party{Role: agentmsg.ParentSentinel},
		agentmsg.Party{TaskID: in.TaskID},
		in.Body, nil,
		agentmsg.Options{
			MaxBodyBytes: t.cfg.Messaging.MaxBodyBytes,
			InReplyTo:    in.InReplyTo,
			Interrupt:    in.Interrupt,
		},
	)
	if err != nil {
		return "", err
	}
	delivered, err := record.GetCoordinator().SendToTask(ctx, record.GetHandle(), in.TaskID, msg)
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

// broadcastToTasks sends one message per task in task_ids and returns a
// per-task result map {task_id: {delivered, error}}. Each target gets its own
// minted message; a failure on one child (unknown, terminal, or mailbox-full)
// is recorded on that entry and does not fail the whole call.
func (t *sendToTaskTool) broadcastToTasks(ctx context.Context, record cliorchestrate.RunAccess, in sendToTaskParams, kind agentmsg.Kind) (string, error) {
	type perTaskResult struct {
		Delivered bool   `json:"delivered"`
		Error     string `json:"error,omitempty"`
	}
	results := make(map[string]perTaskResult, len(in.TaskIDs))
	for _, taskID := range in.TaskIDs {
		if taskID == "" {
			results[taskID] = perTaskResult{Error: "task_id is required"}
			continue
		}
		msg, err := agentmsg.NewMessage(
			in.RunID, kind,
			agentmsg.Party{Role: agentmsg.ParentSentinel},
			agentmsg.Party{TaskID: taskID},
			in.Body, nil,
			agentmsg.Options{
				MaxBodyBytes: t.cfg.Messaging.MaxBodyBytes,
				InReplyTo:    in.InReplyTo,
				Interrupt:    in.Interrupt,
			},
		)
		if err != nil {
			results[taskID] = perTaskResult{Error: err.Error()}
			continue
		}
		delivered, err := record.GetCoordinator().SendToTask(ctx, record.GetHandle(), taskID, msg)
		if err != nil {
			results[taskID] = perTaskResult{Error: err.Error()}
			continue
		}
		results[taskID] = perTaskResult{Delivered: delivered}
	}
	out, _ := json.Marshal(map[string]any{
		"status":  "sent",
		"results": results,
	})
	return string(out), nil
}
