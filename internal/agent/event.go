package agent

import (
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

type EventKind string

const (
	EventAssistant EventKind = "assistant"
	// EventToolPending is emitted BEFORE EventToolStart when a tool call
	// needs user approval. It is the only "pre-start" event: the loop
	// gates Dispatcher.Invoke on ApprovalGate when Emit returns, and
	// the resulting decision (approve / deny) determines whether
	// EventToolStart follows. Detail carries the execution class as
	// its string name so downstream consumers can route without
	// re-deriving from the registry.
	EventToolPending EventKind = "tool_pending"
	EventToolStart   EventKind = "tool_start"
	EventToolEnd     EventKind = "tool_end"
	EventStep        EventKind = "step"
	// EventHeartbeat is a wall-clock progress tick (model thinking, tool
	// batch, batch shaping). It is NOT a step: only real loop steps emitted
	// by emitStep may be EventStep, so consumers that budget or count steps
	// (e.g. subagent schema-retry step budgets) are not inflated by time.
	EventHeartbeat         EventKind = "heartbeat"
	EventPrune             EventKind = "prune"
	EventToolParallel      EventKind = "tool_parallel"
	EventSubagentStart     EventKind = "subagent_start"
	EventSubagentEnd       EventKind = "subagent_end"
	EventSubagentHeartbeat EventKind = "subagent_heartbeat"
	// EventSubagentDone is the run-level terminal signal for one subagent:
	// its loop returned and it will emit nothing further. Distinct from
	// EventSubagentEnd, which closes a single nested tool call - an agent
	// between two tool calls has no open tools but is very much still alive,
	// so only this event may retire it from the parent's live agent view.
	EventSubagentDone EventKind = "subagent_done"
	// EventThinking carries model reasoning (chain of thought) for providers
	// that expose it. Content is the reasoning delta.
	EventThinking EventKind = "thinking"
	// EventHook reports one lifecycle hook execution. Name is the event
	// (PreToolUse/PostToolUse), Detail names the script and what it decided,
	// and Output carries what it said. It is operator-facing only: the model's
	// copy of hook text travels in the tool result, framed.
	EventHook EventKind = "hook"
	// EventCompaction is emitted only after the context checkpoint commits.
	EventCompaction EventKind = "compaction"
	// EventCacheUsage carries provider-reported prompt-cache accounting for
	// one completion turn. See EmitCacheUsage.
	EventCacheUsage EventKind = "cache_usage"
	// EventTokenUsage carries provider-reported input/output token counts
	// for one completion turn. See EmitTokenUsage.
	EventTokenUsage EventKind = "token_usage"
	// EventWorkLimit is the soft conclude notice: the loop told the model to
	// wrap up because a work bound (deadline, output budget, or tool-call
	// budget) is close. It is observability only; the injected instruction
	// itself travels inside the provider request.
	EventWorkLimit EventKind = "work_limit"
	// EventSchemaRetry reports a subagent schema-validation corrective
	// re-entry that is ABOUT to happen: the previous reply failed schema
	// validation and runValidatedReply (internal/subagents/multi_step_schema.go)
	// is about to send a corrective turn and run a full new LLM turn. Without
	// this, a schema-repair retry ran with zero observable signal between the
	// first attempt's visible output and the retry's eventual completion -
	// indistinguishable from a stalled task. Detail carries a human-readable
	// "attempt N/M" message. Observability only: it does not count as
	// EventStep (must not inflate a schema-retry step budget) and must never
	// be confused with EventSubagentDone.
	EventSchemaRetry EventKind = "schema_retry"
	// EventEmptyResponseRetry reports a bounded empty-response retry that is
	// ABOUT to happen: the provider returned a genuinely empty response (no
	// text, no tool calls) and retryOnEmptyResponse
	// (internal/agent/agentloop_run.go) is about to re-run the whole SDK
	// completion loop from the same preparedMsgs. Without this, an
	// empty-response retry ran with zero observable signal - indistinguishable
	// from a stalled turn, the same silent-retry shape EventSchemaRetry fixes
	// for the subagent schema-repair retry. Detail carries a human-readable
	// "attempt N/M" message. Observability only: it does not count as
	// EventStep and does not alter retry control flow.
	EventEmptyResponseRetry EventKind = "empty_response_retry"
)

// EventOrigin identifies the agent that produced an event. The zero value
// means the session's root loop. Subagent handlers stamp it (see
// subagents.StampEventOrigin) so nested tool events stay attributable to
// their run - without it, parallel agents are indistinguishable in the UI.
type EventOrigin struct {
	TaskID string // runtime request/task id - the attribution key
	Agent  string // dispatched subagent/skill name
	Depth  int    // nesting depth (root loop = 0)
	// TaskDescription is a bounded preview of the task this subagent was
	// given (see StampEventOrigin's caller), so a consumer attributing
	// events by TaskID can show what the subagent is actually doing without
	// having to separately correlate the initiating delegate/dispatch_tasks/
	// spawn_agent tool call's own Input. Empty for the root loop (zero
	// EventOrigin) and for any subagent kind that doesn't stamp origin at
	// all (a one-shot delegate has no nested tool calls to attribute).
	TaskDescription string
}

// IsZero reports whether the origin is the root loop.
func (o EventOrigin) IsZero() bool { return o == EventOrigin{} }

type Event struct {
	Kind       EventKind
	ToolCallID string // stable correlation key for tool lifecycle events
	Name       string
	Detail     string
	Content    string
	Input      string // bounded, redacted tool input preview
	Output     string // bounded, redacted tool output preview
	// Denied is set only for EventHook: true when this run blocked its tool
	// call (a PreToolUse hook that denied). Renderers use it to give a
	// blocking run a distinct visual treatment from an advisory one.
	Denied bool
	// Program and Tool are set only for EventHook: the hook script's name
	// (not its path) and the tool it fired for. Name already carries the
	// hook's own event (PreToolUse/PostToolUse/Stop) for this kind, so these
	// are separate fields rather than an overload of an existing one.
	Program, Tool string
	// Origin attributes the event to the producing agent (zero = root loop).
	Origin EventOrigin
	// Identity is an optional typed runtime identity supplied by a routed
	// invocation. It contains no content or authorization material.
	Identity *events.Identity
	// Compaction is present only for the post-commit typed progress event. It
	// is not copied into generic content/input/output envelopes.
	Compaction *events.CompactionEvent
	// CacheUsage is present only for the typed prompt-cache accounting
	// event. It is not copied into generic content/input/output envelopes.
	CacheUsage *events.CacheUsageEvent
	// TokenUsage is present only for the typed token accounting event. It is
	// not copied into generic content/input/output envelopes.
	TokenUsage *events.TokenUsageEvent
}
