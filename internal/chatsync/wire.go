package chatsync

import (
	"encoding/json"
	"time"
)

// ---------------------------------------------------------------------------
// Promoted probe wire types.
//
// Field names and nullability mirror the API's response DTOs.
// Timestamps stay strings: the format is part of the contract, so the probe
// reports it rather than failing inside a decoder.
// ---------------------------------------------------------------------------

// Session models a chat session record on the API.
type Session struct {
	ID              string  `json:"id"`
	OrganizationID  string  `json:"organizationId"`
	UserID          string  `json:"userId"`
	Title           string  `json:"title"`
	CwdLabel        *string `json:"cwdLabel"`
	HostLabel       *string `json:"hostLabel"`
	Status          string  `json:"status"`
	LastSeq         int64   `json:"lastSeq"`
	LastEventAt     string  `json:"lastEventAt"`
	LastHeartbeatAt string  `json:"lastHeartbeatAt"`
	EndedAt         *string `json:"endedAt"`
	CreatedAt       string  `json:"createdAt"`
}

// EventItem is one event in an append batch.
type EventItem struct {
	Seq     int64           `json:"seq"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// StoredEvent is an event read back from the cursor endpoint.
type StoredEvent struct {
	SessionID string          `json:"sessionId"`
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"createdAt"`
}

// AppendResult is the response from appending events.
type AppendResult struct {
	LastSeq       int64 `json:"lastSeq"`
	InsertedCount int   `json:"insertedCount"`
}

// SessionInput is a remote input queued for this session.
type SessionInput struct {
	ID           string  `json:"id"`
	SessionID    string  `json:"sessionId"`
	AuthorUserID string  `json:"authorUserId"`
	Kind         string  `json:"kind"`
	Body         string  `json:"body"`
	CreatedAt    string  `json:"createdAt"`
	ConsumedAt   *string `json:"consumedAt"`
}

// NextInput is the envelope returned by long-polling /inputs/next.
type NextInput struct {
	Input *SessionInput `json:"input"`
}

// ErrorEnvelope is the shape the API's global exception filter emits. Message
// is a string for thrown exceptions and an array for validation failures, so
// it stays raw.
type ErrorEnvelope struct {
	StatusCode int             `json:"statusCode"`
	Error      string          `json:"error"`
	Message    json.RawMessage `json:"message"`
	Path       string          `json:"path"`
}

// ---------------------------------------------------------------------------
// Wire type constants (prefix mivia.chat.v1.).
// ---------------------------------------------------------------------------

const (
	TypeTurnStarted         = "mivia.chat.v1.turn.started"
	TypeAssistantDelta      = "mivia.chat.v1.assistant.delta"
	TypeAssistantMessage    = "mivia.chat.v1.assistant.message"
	TypeThinkingDelta       = "mivia.chat.v1.thinking.delta"
	TypeToolStarted         = "mivia.chat.v1.tool.started"
	TypeToolEnded           = "mivia.chat.v1.tool.ended"
	TypeSubagentToolStarted = "mivia.chat.v1.subagent.tool.started"
	TypeSubagentToolEnded   = "mivia.chat.v1.subagent.tool.ended"
	TypeSubagentProgress    = "mivia.chat.v1.subagent.progress"
	TypeSubagentEnded       = "mivia.chat.v1.subagent.ended"
	// A subagent's own prose. These are DELIBERATELY distinct types rather
	// than the root assistant/thinking types with Envelope.Agent set: a
	// viewer that predates them must be able to keep a subagent's output out
	// of the main transcript, and the only thing such a viewer can key on is
	// the type string. Every subagent type shares the
	// "mivia.chat.v1.subagent." prefix for exactly that reason, so one
	// prefix rule covers types minted after the client shipped.
	TypeSubagentAssistantDelta   = "mivia.chat.v1.subagent.assistant.delta"
	TypeSubagentAssistantMessage = "mivia.chat.v1.subagent.assistant.message"
	TypeSubagentThinkingDelta    = "mivia.chat.v1.subagent.thinking.delta"
	TypeTurnEnded                = "mivia.chat.v1.turn.ended"
	TypeContextCompacted         = "mivia.chat.v1.context.compacted"
	TypeTurnFailed               = "mivia.chat.v1.turn.failed"
	TypeSyncDropped              = "mivia.chat.v1.sync.dropped"
	TypeSyncForked               = "mivia.chat.v1.sync.forked"
)

// WireEventSpec binds one wire type string to the Go struct that models its
// payload.
type WireEventSpec struct {
	// Type is the mivia.chat.v1.* type string. The API names each SSE frame
	// after this exact string, so a browser client must register a listener
	// per entry: EventSource.onmessage never fires for these events.
	Type string
	// Payload is a zero value of the struct that models this type's payload.
	Payload any
}

// wireEventSpecs is the SINGLE definition site of the mivia.chat.v1 event
// vocabulary. KnownWireTypes is derived from it, the recorded contract in
// api/contracts/chat-sessions.v1.json is checked against it, and a
// source-level gate proves no Type* constant is missing from it. Adding a
// type means adding one row here and re-recording the contract; there is no
// second list in Go to keep in step.
var wireEventSpecs = []WireEventSpec{
	{Type: TypeTurnStarted, Payload: TurnStartedPayload{}},
	{Type: TypeAssistantDelta, Payload: AssistantDeltaPayload{}},
	{Type: TypeAssistantMessage, Payload: AssistantMessagePayload{}},
	{Type: TypeThinkingDelta, Payload: ThinkingDeltaPayload{}},
	{Type: TypeToolStarted, Payload: ToolStartedPayload{}},
	{Type: TypeToolEnded, Payload: ToolEndedPayload{}},
	{Type: TypeSubagentToolStarted, Payload: SubagentToolStartedPayload{}},
	{Type: TypeSubagentToolEnded, Payload: SubagentToolEndedPayload{}},
	{Type: TypeSubagentProgress, Payload: SubagentProgressPayload{}},
	{Type: TypeSubagentEnded, Payload: SubagentEndedPayload{}},
	{Type: TypeSubagentAssistantDelta, Payload: SubagentAssistantDeltaPayload{}},
	{Type: TypeSubagentAssistantMessage, Payload: SubagentAssistantMessagePayload{}},
	{Type: TypeSubagentThinkingDelta, Payload: SubagentThinkingDeltaPayload{}},
	{Type: TypeTurnEnded, Payload: TurnEndedPayload{}},
	{Type: TypeContextCompacted, Payload: ContextCompactedPayload{}},
	{Type: TypeTurnFailed, Payload: TurnFailedPayload{}},
	{Type: TypeSyncDropped, Payload: SyncDroppedPayload{}},
	{Type: TypeSyncForked, Payload: SyncForkedPayload{}},
}

// WireEventSpecs returns the event vocabulary in wire order. The returned
// slice is a copy, so a caller cannot edit the definition site.
func WireEventSpecs() []WireEventSpec {
	return append([]WireEventSpec(nil), wireEventSpecs...)
}

// KnownWireTypes lists all recognized mivia.chat.v1.* type constants. It is
// derived from wireEventSpecs and is never hand-maintained.
var KnownWireTypes = derivedWireTypes()

func derivedWireTypes() []string {
	out := make([]string, 0, len(wireEventSpecs))
	for _, spec := range wireEventSpecs {
		out = append(out, spec.Type)
	}
	return out
}

// ---------------------------------------------------------------------------
// Common envelope and sub-structures.
// ---------------------------------------------------------------------------

// Envelope is the common envelope embedded in every wire event payload.
type Envelope struct {
	V            int          `json:"v"`
	At           time.Time    `json:"at"`
	Turn         string       `json:"turn"`
	Block        string       `json:"block,omitempty"`
	Agent        *AgentOrigin `json:"agent,omitempty"`
	Trunc        *Truncation  `json:"trunc,omitempty"`
	Redacted     []string     `json:"redacted,omitempty"`
	SourceTurnID string       `json:"source_turn_id,omitempty"`
	WriterID     string       `json:"writer_id,omitempty"`
}

// AgentOrigin identifies a subagent when an event was emitted by one.
type AgentOrigin struct {
	Task  string `json:"task,omitempty"`
	Name  string `json:"name,omitempty"`
	Depth int    `json:"depth,omitempty"`
}

// Truncation contains field-level truncation records.
type Truncation struct {
	Fields map[string]TruncField `json:"fields"`
}

// TruncField records the kept and total byte lengths of a truncated string field.
type TruncField struct {
	Kept  int `json:"kept"`
	Total int `json:"total"`
}

// ---------------------------------------------------------------------------
// Typed wire event payloads.
// ---------------------------------------------------------------------------

// TurnStartedPayload is the payload of mivia.chat.v1.turn.started.
type TurnStartedPayload struct {
	Envelope
	Text       string `json:"text,omitempty"`
	SessionRef string `json:"session_ref,omitempty"`
	Synthetic  bool   `json:"synthetic,omitempty"`
}

// AssistantDeltaPayload is the payload of mivia.chat.v1.assistant.delta.
type AssistantDeltaPayload struct {
	Envelope
	Text  string `json:"text"`
	Index int    `json:"index"`
}

// AssistantMessagePayload is the payload of mivia.chat.v1.assistant.message.
type AssistantMessagePayload struct {
	Envelope
	Fragments int    `json:"fragments"`
	Bytes     int    `json:"bytes"`
	Status    string `json:"status"`
	Text      string `json:"text,omitempty"`
}

// ThinkingDeltaPayload is the payload of mivia.chat.v1.thinking.delta.
type ThinkingDeltaPayload struct {
	Envelope
	Bytes int    `json:"bytes"`
	Index int    `json:"index"`
	Text  string `json:"text,omitempty"`
}

// ToolStartedPayload is the payload of mivia.chat.v1.tool.started.
type ToolStartedPayload struct {
	Envelope
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	InputBytes int    `json:"input_bytes"`
	Phase      string `json:"phase,omitempty"`
	Input      string `json:"input,omitempty"`
}

// ToolEndedPayload is the payload of mivia.chat.v1.tool.ended.
type ToolEndedPayload struct {
	Envelope
	ToolCallID  string `json:"tool_call_id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	OutputBytes int    `json:"output_bytes"`
	Detail      string `json:"detail,omitempty"`
	Output      string `json:"output,omitempty"`
}

// SubagentToolStartedPayload is the payload of mivia.chat.v1.subagent.tool.started.
type SubagentToolStartedPayload struct {
	Envelope
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	InputBytes int    `json:"input_bytes"`
	Phase      string `json:"phase,omitempty"`
	Input      string `json:"input,omitempty"`
}

// SubagentToolEndedPayload is the payload of mivia.chat.v1.subagent.tool.ended.
type SubagentToolEndedPayload struct {
	Envelope
	ToolCallID  string `json:"tool_call_id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	OutputBytes int    `json:"output_bytes"`
	Detail      string `json:"detail,omitempty"`
	Output      string `json:"output,omitempty"`
}

// SubagentProgressPayload is the payload of mivia.chat.v1.subagent.progress.
//
// Detail is the only field. This type once also declared elapsed_seconds,
// steps and tool_calls, which no projection ever set: the producer
// (MultiStepHandler.stepOnEvent) formats those three numbers into the
// heartbeat's Detail text, and agent.Event has no numeric fields to carry
// them structurally. Three permanently-absent optional fields are worse than
// none - a client cannot tell "this build never sends it" from "nothing to
// report" - so they are gone until something actually populates them.
type SubagentProgressPayload struct {
	Envelope
	Detail string `json:"detail,omitempty"`
}

// SubagentEndedPayload is the payload of mivia.chat.v1.subagent.ended.
type SubagentEndedPayload struct {
	Envelope
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
}

// SubagentAssistantDeltaPayload is the payload of
// mivia.chat.v1.subagent.assistant.delta. It mirrors AssistantDeltaPayload,
// but Index counts fragments within ONE subagent run: two subagents streaming
// at once each start at 0, and Envelope.Agent.Task is what tells them apart.
type SubagentAssistantDeltaPayload struct {
	Envelope
	Text  string `json:"text"`
	Index int    `json:"index"`
}

// SubagentAssistantMessagePayload is the payload of
// mivia.chat.v1.subagent.assistant.message. Fragments and Text obey the same
// rule as the root aggregate (INV-1: Text is empty exactly when Fragments is
// non-zero), counted per subagent run rather than per turn.
type SubagentAssistantMessagePayload struct {
	Envelope
	Fragments int    `json:"fragments"`
	Bytes     int    `json:"bytes"`
	Status    string `json:"status"`
	Text      string `json:"text,omitempty"`
}

// SubagentThinkingDeltaPayload is the payload of
// mivia.chat.v1.subagent.thinking.delta. Text is present only when the host
// enabled thinking; Bytes always reports the real size, so a viewer can show
// that a subagent is reasoning without the content.
type SubagentThinkingDeltaPayload struct {
	Envelope
	Bytes int    `json:"bytes"`
	Index int    `json:"index"`
	Text  string `json:"text,omitempty"`
}

// TurnEndedPayload is the payload of mivia.chat.v1.turn.ended (F4: blocks dropped).
type TurnEndedPayload struct {
	Envelope
	Reason string `json:"reason"`
}

// ContextCompactedPayload is the payload of mivia.chat.v1.context.compacted.
type ContextCompactedPayload struct {
	Envelope
	Compaction   any    `json:"compaction,omitempty"`
	Message      string `json:"message,omitempty"`
	BetweenTurns bool   `json:"between_turns,omitempty"`
}

// TurnFailedPayload is the payload of mivia.chat.v1.turn.failed.
type TurnFailedPayload struct {
	Envelope
	Message string `json:"message"`
}

// SyncDroppedPayload is the payload of mivia.chat.v1.sync.dropped.
type SyncDroppedPayload struct {
	Envelope
	Dropped      uint64 `json:"dropped"`
	TotalDropped uint64 `json:"total_dropped"`
}

// SyncForkedPayload is the payload of mivia.chat.v1.sync.forked.
type SyncForkedPayload struct {
	Envelope
	NewSessionID string `json:"new_session_id"`
}

// WireEvent is one projected event ready for the outbox or upload batch.
type WireEvent struct {
	Seq     int64  `json:"seq"`
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}
