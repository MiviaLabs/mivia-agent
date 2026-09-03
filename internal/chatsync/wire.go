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
	TypeTurnStarted      = "mivia.chat.v1.turn.started"
	TypeAssistantDelta   = "mivia.chat.v1.assistant.delta"
	TypeAssistantMessage = "mivia.chat.v1.assistant.message"
	// A turn that is being re-driven from the beginning. Everything already
	// sent for its assistant block belongs to an attempt that no longer
	// exists, so a viewer that accumulates deltas must drop them.
	TypeAssistantReset = "mivia.chat.v1.assistant.reset"
	TypeThinkingDelta  = "mivia.chat.v1.thinking.delta"
	// The settled form of one thinking block, emitted when the block closes.
	// It exists so reasoning survives a redaction policy: a policy suppresses
	// the per-fragment text (a secret split across two deltas matches
	// neither pattern), and without a whole-block form to redact as ONE
	// string the reasoning was lost outright. Same INV-1 shape as
	// assistant.message - Text is empty exactly when Fragments is non-zero.
	TypeThinkingMessage     = "mivia.chat.v1.thinking.message"
	TypeToolStarted         = "mivia.chat.v1.tool.started"
	TypeToolEnded           = "mivia.chat.v1.tool.ended"
	TypeHookRan             = "mivia.chat.v1.hook.ran"
	TypeSubagentToolStarted = "mivia.chat.v1.subagent.tool.started"
	TypeSubagentToolEnded   = "mivia.chat.v1.subagent.tool.ended"
	TypeSubagentStarted     = "mivia.chat.v1.subagent.started"
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
	TypeSubagentThinkingMessage  = "mivia.chat.v1.subagent.thinking.message"
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
	{Type: TypeAssistantReset, Payload: AssistantResetPayload{}},
	{Type: TypeThinkingDelta, Payload: ThinkingDeltaPayload{}},
	{Type: TypeThinkingMessage, Payload: ThinkingMessagePayload{}},
	{Type: TypeToolStarted, Payload: ToolStartedPayload{}},
	{Type: TypeToolEnded, Payload: ToolEndedPayload{}},
	{Type: TypeHookRan, Payload: HookRanPayload{}},
	{Type: TypeSubagentToolStarted, Payload: SubagentToolStartedPayload{}},
	{Type: TypeSubagentToolEnded, Payload: SubagentToolEndedPayload{}},
	{Type: TypeSubagentStarted, Payload: SubagentStartedPayload{}},
	{Type: TypeSubagentProgress, Payload: SubagentProgressPayload{}},
	{Type: TypeSubagentEnded, Payload: SubagentEndedPayload{}},
	{Type: TypeSubagentAssistantDelta, Payload: SubagentAssistantDeltaPayload{}},
	{Type: TypeSubagentAssistantMessage, Payload: SubagentAssistantMessagePayload{}},
	{Type: TypeSubagentThinkingDelta, Payload: SubagentThinkingDeltaPayload{}},
	{Type: TypeSubagentThinkingMessage, Payload: SubagentThinkingMessagePayload{}},
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
	Task string `json:"task,omitempty"`
	Name string `json:"name,omitempty"`
	// Depth carries NO omitempty, deliberately. omitempty omits the zero
	// value, so a depth of 0 - the root agent - vanished from the wire and an
	// attributed root event became indistinguishable from a subagent event
	// whose depth simply had not been stamped. Consumers split the main
	// transcript from the subagent lanes on exactly this field, so the web
	// viewer filed the root agent's own prose and reasoning under a lane and
	// showed a transcript of tool cards with nothing between them. When an
	// origin is present at all, its depth is now always stated.
	Depth int `json:"depth"`
	// ParentTask is the Task of the subagent that dispatched this one, empty
	// when the root loop did. Depth reports how deep a run sits; this reports
	// under WHICH run, which two runs at the same depth do not share.
	ParentTask string `json:"parent_task,omitempty"`
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

// AssistantResetPayload is the payload of mivia.chat.v1.assistant.reset.
//
// It carries only the envelope. The block it applies to is the envelope's own
// Block, so one reset can never be mistaken for another turn's or another
// subagent run's. Reason is a short, content-free classification of why the
// turn restarted.
type AssistantResetPayload struct {
	Envelope
	Reason string `json:"reason,omitempty"`
}

// ThinkingDeltaPayload is the payload of mivia.chat.v1.thinking.delta.
type ThinkingDeltaPayload struct {
	Envelope
	Bytes int    `json:"bytes"`
	Index int    `json:"index"`
	Text  string `json:"text,omitempty"`
}

// ThinkingMessagePayload is the payload of mivia.chat.v1.thinking.message:
// one thinking block, settled at the moment the block closed.
//
// Fragments and Text obey INV-1 exactly as the assistant aggregate does - Text
// is empty exactly when Fragments is non-zero - counted per BLOCK rather than
// per turn, because a turn reasons once per step and each step is its own
// block. When the deltas shipped their text the viewer already has it and
// this event only completes the block; when they did not (a redaction policy
// withheld every fragment, or thinking never streamed at all) Text carries
// the whole reasoning, redacted as ONE string. That is the entire point: a
// pattern spanning two fragments is invisible to a per-fragment redactor and
// visible to this one.
type ThinkingMessagePayload struct {
	Envelope
	Fragments int    `json:"fragments"`
	Bytes     int    `json:"bytes"`
	Status    string `json:"status"`
	Text      string `json:"text,omitempty"`
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

// HookRanPayload is the payload of mivia.chat.v1.hook.ran: one lifecycle hook
// run on the operator's machine.
//
// It exists for one case above all. A hook can BLOCK a tool call, and a
// blocked call never produces a tool.ended - so without this event a remote
// reader watches a tool.started that simply never finishes, with no way to
// learn that a local policy stopped it.
//
// ToolCallID ties the row to the call it gated. It is empty for a Stop hook,
// which runs for the turn rather than for one call.
type HookRanPayload struct {
	Envelope
	// Phase is PreToolUse, PostToolUse or Stop.
	Phase string `json:"phase"`
	// Program is the script's NAME, never its path. A path describes the
	// operator's machine and this payload leaves it.
	Program string `json:"program"`
	// Tool is the call the hook ran for; empty for a Stop hook.
	Tool       string `json:"tool,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Blocked is true for the run that refused the call. A reader must not
	// have to infer this from the absence of a later event.
	Blocked bool `json:"blocked"`
	// OutputBytes always reports the real size, even when Output is withheld,
	// so a reader can tell "the hook said nothing" from "the hook said
	// something you are not being shown".
	OutputBytes int `json:"output_bytes"`
	// Output is what the hook printed - for a blocking hook, the reason. It
	// rides the same include-tool-io gate as tool output, because it is the
	// same class of thing: text a local program produced.
	Output string `json:"output,omitempty"`
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

// SubagentStartedPayload is the payload of mivia.chat.v1.subagent.started:
// the run-level opening signal for one subagent.
//
// The envelope's own timestamp is the run's start time, so this carries no
// separate started_at.
//
// Task is a bounded PREVIEW of what the run was asked to do, not the prompt it
// received: the producer caps it at 200 bytes (subagents.maxTaskDescriptionBytes)
// and, for a dispatch whose input is not a bare string, it can be raw JSON
// rather than natural language. It is named and budgeted accordingly - a
// 32 KiB prompt budget could never bind against a 200-byte producer bound, so
// the real limit would never appear in trunc.fields.
type SubagentStartedPayload struct {
	Envelope
	Name string `json:"name,omitempty"`
	Task string `json:"task,omitempty"`
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

// SubagentThinkingMessagePayload is the payload of
// mivia.chat.v1.subagent.thinking.message. Same settled form and same INV-1
// rule as the root aggregate, counted per subagent run: a lane's reasoning is
// withheld by a redaction policy for the same reason the root's is, so it
// needs the same whole-block form to fall back to.
type SubagentThinkingMessagePayload struct {
	Envelope
	Fragments int    `json:"fragments"`
	Bytes     int    `json:"bytes"`
	Status    string `json:"status"`
	Text      string `json:"text,omitempty"`
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
	// ForkedFrom names the session that was abandoned, so the new session's
	// stream self-describes its ancestor. Empty on markers older producers
	// wrote.
	ForkedFrom string `json:"forked_from,omitempty"`
}

// WireEvent is one projected event ready for the outbox or upload batch.
type WireEvent struct {
	Seq     int64  `json:"seq"`
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}
