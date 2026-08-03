package coordinator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// MessageSummary is the model-visible synopsis entry for a task_message event.
type MessageSummary struct {
	MessageID  string        `json:"message_id"`
	Kind       agentmsg.Kind `json:"kind"`
	Synopsis   string        `json:"synopsis"`
	ContentRef string        `json:"content_ref,omitempty"`
	TaskID     string        `json:"task_id,omitempty"`
	Sequence   uint64        `json:"sequence,omitempty"`
}

// MessageKindAskDeclined is the run_messages kind surfaced for a
// task_ask_declined lifecycle event. The ask itself never persisted as a
// message on this path — it is a decline announcement attributed to the asker.
const MessageKindAskDeclined agentmsg.Kind = "ask_declined"

// ListRunMessages returns task_message announcements for a run, optionally
// filtered by taskID (empty = all tasks). Bodies are never included - use
// content_ref + ledger LoadContent for full payloads. Task-ask-decline
// announcements (LifecycleKindTaskAskDeclined) are surfaced as
// MessageKindAskDeclined entries with TaskID = asker and a synopsis like
// "ask declined: target terminal".
func (c *coordinator) ListRunMessages(ctx context.Context, runID, taskID string) ([]MessageSummary, error) {
	if runID == "" {
		return nil, fmt.Errorf("list run messages: run_id is required")
	}
	events, err := c.repo.ListEvents(ctx, runID)
	if err != nil {
		return nil, err
	}
	out := make([]MessageSummary, 0)
	for _, e := range events {
		switch e.Kind {
		case LifecycleKindTaskMessage:
			if taskID != "" && e.TaskID != taskID {
				continue
			}
			var p agentmsg.LifecyclePayload
			if len(e.Payload) > 0 {
				if err := json.Unmarshal(e.Payload, &p); err != nil {
					continue
				}
			}
			out = append(out, MessageSummary{
				MessageID:  p.MessageID,
				Kind:       p.Kind,
				Synopsis:   p.Synopsis,
				ContentRef: p.ContentRef,
				TaskID:     e.TaskID,
				Sequence:   e.Sequence,
			})
		case LifecycleKindTaskAskDeclined:
			if taskID != "" && e.TaskID != taskID {
				continue
			}
			var p struct {
				AskID  string `json:"ask_id"`
				Reason string `json:"reason"`
			}
			if len(e.Payload) > 0 {
				_ = json.Unmarshal(e.Payload, &p)
			}
			reason := p.Reason
			if reason == "" {
				reason = agentmsg.DeclineReasonResponderTerminal
			}
			out = append(out, MessageSummary{
				MessageID: p.AskID,
				Kind:      MessageKindAskDeclined,
				Synopsis:  "ask declined: " + reason,
				TaskID:    e.TaskID,
				Sequence:  e.Sequence,
			})
		}
	}
	return out, nil
}

// LoadMessageBody resolves a content_ref to the full stored message envelope.
func (c *coordinator) LoadMessageBody(ctx context.Context, contentRef string) (agentmsg.Message, error) {
	if contentRef == "" {
		return agentmsg.Message{}, ledger.ErrContentNotFound
	}
	data, err := c.repo.LoadContent(ctx, contentRef)
	if err != nil {
		return agentmsg.Message{}, err
	}
	var msg agentmsg.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return agentmsg.Message{}, fmt.Errorf("decode message: %w", err)
	}
	return msg, nil
}
