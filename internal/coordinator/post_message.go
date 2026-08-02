package coordinator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/contentref"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// Lifecycle kind for agent-to-agent message announcements (plan 53.01).
// Payload is ID + synopsis only (never bodies) - see agentmsg.LifecyclePayload.
const LifecycleKindTaskMessage = "task_message"

// PostTaskMessage persists a validated message to the run ledger, appends a
// task_message lifecycle event (ID + synopsis only), and announces it to
// lifecycle subscribers. This is the seam child tools and parent tools use so
// they never write ledger rows without a live announce.
//
// Phase 01: no delivery, mailbox, or model-visible tools. Callers construct
// and validate the message first (agentmsg.NewMessage / Validate).
//
// Ordering is persist-then-announce: content store → AppendEvent → emit.
// Content is pinned via StoreContent (never reclaimed), so a task_message
// event's content_ref always resolves (INV-AG-10 pinning decision).
func (c *coordinator) PostTaskMessage(ctx context.Context, runID, taskID string, msg agentmsg.Message) error {
	if runID == "" {
		return fmt.Errorf("post task message: run_id is required")
	}
	if taskID == "" {
		return fmt.Errorf("post task message: task_id is required")
	}
	if msg.RunID != "" && msg.RunID != runID {
		return fmt.Errorf("post task message: message run_id %q does not match %q", msg.RunID, runID)
	}

	if _, err := c.repo.GetRun(ctx, runID); err != nil {
		return fmt.Errorf("post task message: get run: %w", err)
	}
	if _, err := c.repo.GetTask(ctx, runID, taskID); err != nil {
		return fmt.Errorf("post task message: get task: %w", err)
	}

	// Stamp run/task provenance before validation so callers may omit them
	// (server-side identity is authoritative; children cannot spoof From).
	msg.RunID = runID
	if msg.From.TaskID == "" {
		msg.From.TaskID = taskID
	}
	if err := agentmsg.Validate(msg, agentmsg.DefaultMaxBodyBytes); err != nil {
		return fmt.Errorf("post task message: %w", err)
	}

	payloadBytes, contentRef := encodeMessageForLedger(msg)
	if err := c.repo.StoreContent(ctx, contentRef, payloadBytes); err != nil {
		return fmt.Errorf("post task message: store content: %w", err)
	}

	announce := agentmsg.LifecyclePayload{
		MessageID:  msg.ID,
		Kind:       msg.Kind,
		Synopsis:   agentmsg.Synopsis(msg.Body, agentmsg.DefaultSynopsisBytes),
		ContentRef: contentRef,
	}
	// LifecyclePayload is a fixed struct of strings (no Body field); Marshal
	// cannot fail. Payload contract (ID+synopsis only) is enforced by the type
	// and by TestAssertPayloadIsAnnouncement / integration assertions.
	raw, _ := json.Marshal(announce)

	evt := ledger.LifecycleEvent{
		ID:        newEventID(),
		RunID:     runID,
		Kind:      LifecycleKindTaskMessage,
		TaskID:    taskID,
		AttemptID: "", // attempt stamped by callers in later phases when known
		Payload:   raw,
		CreatedAt: c.nowLocked(),
	}
	if err := c.repo.AppendEvent(ctx, evt); err != nil {
		return fmt.Errorf("post task message: append event: %w", err)
	}
	c.emitLifecycleEvent(evt)
	return nil
}

// encodeMessageForLedger serializes the full message for content-addressed
// storage and returns the KindMessage ref. Message is always JSON-serializable
// (strings/time/structs); contentref.KindMessage is registered, so the ref is
// non-empty for any non-empty marshaled envelope.
func encodeMessageForLedger(msg agentmsg.Message) (data []byte, ref string) {
	data, _ = json.Marshal(msg)
	ref = contentref.Reference(contentref.KindMessage, data)
	return data, ref
}

// assertPayloadIsAnnouncement ensures lifecycle Payload is ID+synopsis shape
// and does not embed a raw body field. Short bodies may equal synopsis.
func assertPayloadIsAnnouncement(payload []byte) error {
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		return fmt.Errorf("post task message: payload not JSON: %w", err)
	}
	if _, ok := m["body"]; ok {
		return fmt.Errorf("post task message: lifecycle payload must not contain body")
	}
	// Payload keys are only the announcement fields.
	for k := range m {
		switch k {
		case "message_id", "kind", "synopsis", "content_ref":
		default:
			return fmt.Errorf("post task message: unexpected payload field %q", k)
		}
	}
	return nil
}
