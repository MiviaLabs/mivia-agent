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
	TypeTurnEnded           = "mivia.chat.v1.turn.ended"
	TypeContextCompacted    = "mivia.chat.v1.context.compacted"
	TypeTurnFailed          = "mivia.chat.v1.turn.failed"
	TypeSyncDropped         = "mivia.chat.v1.sync.dropped"
	TypeSyncForked          = "mivia.chat.v1.sync.forked"
)

// KnownWireTypes lists all recognized mivia.chat.v1.* type constants.
var KnownWireTypes = []string{
	TypeTurnStarted,
	TypeAssistantDelta,
	TypeAssistantMessage,
	TypeThinkingDelta,
	TypeToolStarted,
	TypeToolEnded,
	TypeSubagentToolStarted,
	TypeSubagentToolEnded,
	TypeSubagentProgress,
	TypeSubagentEnded,
	TypeTurnEnded,
	TypeContextCompacted,
	TypeTurnFailed,
	TypeSyncDropped,
	TypeSyncForked,
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
type SubagentProgressPayload struct {
	Envelope
	ElapsedSeconds float64 `json:"elapsed_seconds,omitempty"`
	Steps          int     `json:"steps,omitempty"`
	ToolCalls      int     `json:"tool_calls,omitempty"`
	Detail         string  `json:"detail,omitempty"`
}

// SubagentEndedPayload is the payload of mivia.chat.v1.subagent.ended.
type SubagentEndedPayload struct {
	Envelope
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
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
