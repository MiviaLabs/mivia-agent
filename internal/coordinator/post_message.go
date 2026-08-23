package coordinator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
)

// Lifecycle kind for agent-to-agent message announcements (plan 53.01).
// Payload is ID + synopsis only (never bodies) - see agentmsg.LifecyclePayload.
const LifecycleKindTaskMessage = "task_message"

// LifecycleKindTaskAskDeclined is appended when an ask is declined because its
// target task reached terminal status without answering. Attributed to the
// ASKER task/attempt. Payload carries {ask_id, reason}; run_messages surfaces
// it as an "ask_declined" entry (plan 53.04 observability).
const LifecycleKindTaskAskDeclined = "task_ask_declined"

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

	// Stamp run ID always. From.TaskID is filled only for non-parent parties
	// that omit it (child tools). Parent steers/answers use Role=parent (or
	// zero Party, which IsParent treats as parent) and must not gain a child
	// TaskID - that would make Party.IsParent() false after persist.
	msg.RunID = runID
	if msg.From.TaskID == "" && msg.From.Role != agentmsg.ParentSentinel && !msg.From.IsParent() {
		msg.From.TaskID = taskID
	}
	maxBody := c.maxBodyBytes
	if maxBody <= 0 {
		maxBody = agentmsg.DefaultMaxBodyBytes
	}
	if err := agentmsg.Validate(msg, maxBody); err != nil {
		return fmt.Errorf("post task message: %w", err)
	}

	payloadBytes, contentRef := encodeMessageForLedger(msg)
	if err := c.repo.StoreContent(ctx, contentRef, payloadBytes); err != nil {
		return fmt.Errorf("post task message: store content: %w", err)
	}

	announce := agentmsg.LifecyclePayload{
		MessageID:  msg.ID,
		Kind:       msg.Kind,
		Synopsis:   agentmsg.Synopsis(redact.Text(msg.Body), agentmsg.DefaultSynopsisBytes),
		ContentRef: contentRef,
	}
	// LifecyclePayload is a fixed struct of strings (no Body field); Marshal
	// cannot fail. Payload contract (ID+synopsis only) is enforced by the type
	// and by TestAssertPayloadIsAnnouncement / integration assertions.
	raw, _ := json.Marshal(announce)

	// SessionID carries the calling principal's session when the task is in
	// hand: the child tool context stamps the caller (the task's session), so a
	// child-posted message correlates to its task's surface. It stays empty
	// when no caller is in the context.
	sessionID := ""
	if caller, ok := runtime.CallerFrom(ctx); ok {
		sessionID = caller.SessionID
	}
	evt := ledger.LifecycleEvent{
		ID:        newEventID(),
		RunID:     runID,
		Kind:      LifecycleKindTaskMessage,
		TaskID:    taskID,
		AttemptID: "", // attempt stamped by callers in later phases when known
		SessionID: sessionID,
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
// (strings/time/structs); sdkadapter.KindMessage is registered, so the ref is
// non-empty for any non-empty marshaled envelope.
func encodeMessageForLedger(msg agentmsg.Message) (data []byte, ref string) {
	data, _ = json.Marshal(msg)
	ref = sdkadapter.Mint(sdkadapter.KindMessage, data)
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
