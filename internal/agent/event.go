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
	// EventSubagentBegin is the run-level OPENING signal for one subagent,
	// the mirror of EventSubagentDone. Distinct from EventSubagentStart,
	// which opens a single nested TOOL call.
	//
	// A run was previously only discoverable from its first nested event, so
	// a consumer had no event carrying the run's own identity: the task it
	// was given arrived, if at all, on the dispatching tool call, which a
	// remote consumer has to correlate separately. Detail carries the bounded
	// task description, and the event's own timestamp is the run's start.
	EventSubagentBegin EventKind = "subagent_begin"
	// EventSubagentDone is the run-level terminal signal for one subagent:
	// its loop returned and it will emit nothing further. Distinct from
	// EventSubagentEnd, which closes a single nested tool call - an agent
	// between two tool calls has no open tools but is very much still alive,
	// so only this event may retire it from the parent's live agent view.
	EventSubagentDone EventKind = "subagent_done"
	// EventAssistantReset tells a consumer to discard the assistant text it
	// has accumulated for the current turn and start again.
	//
	// It exists because a turn can be re-driven whole after it has already
	// streamed: a prompt-too-long compaction retry, a bounded empty-response
	// retry, or a subagent schema retry all replay the turn. A consumer that
	// appends deltas has no way to know the second attempt is not a
	// continuation of the first, and shows the answer twice.
	//
	// It is not an error. A turn that resets and then completes is a normal,
	// successful turn.
	EventAssistantReset EventKind = "assistant_reset"
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
	// EventUnactedContinuation reports a bounded continuation that is ABOUT
	// to happen: the turn produced text that announced work, called no tool
	// at all, and continueUnactedTurn (internal/agent/unacted_turn.go) is
	// re-running the loop with the continuation notice appended. Detail
	// carries a human-readable "N/M" message. Observability only: it does
	// not count as EventStep and does not alter retry control flow. It fires
	// only when an operator set [chat] max_unacted_continuations.
	EventUnactedContinuation EventKind = "unacted_continuation"
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
	// SessionID and TurnID are the conversation and turn the subagent is
	// working inside, copied from the runtime.Request that dispatched it.
	//
	// They are on the ORIGIN rather than captured where the event is published
	// because the subagent publish path runs through package-level state
	// (clichat's global bus and progress sink), which has no per-session
	// context to capture: the dispatcher is shared by pointer through the
	// copied tool registry, so construction-time capture would attribute every
	// subagent to whichever session happened to build it first. Carrying the
	// identity on the event is the only place it is unambiguous.
	//
	// Without them a subagent's events reach the bus with an empty SessionID,
	// and internal/hub's receiver drops every event whose SessionID does not
	// match its own - so a second live surface saw the root loop's tool calls
	// and none of its subagents'. Empty for the root loop, which publishes
	// through agent.emit and gets both from Options instead.
	SessionID string
	TurnID    string
	// ParentTaskID is the TaskID of the subagent that dispatched this one,
	// empty when the root loop dispatched it.
	//
	// Depth alone cannot rebuild the tree: two subagents at depth 2 under
	// different depth-1 parents are indistinguishable by depth, so a consumer
	// showing nested runs had to render every run as a sibling of every
	// other. This is the edge that makes the depth a position rather than
	// just a number.
	ParentTaskID string
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
	// Status carries the terminal status of a subagent run on
	// EventSubagentDone only: "completed", "canceled", "timed_out", or
	// "error" - the same fixed vocabulary as the task-result envelope's
	// status field (terminalStatus in internal/subagents). Empty means the
	// emitter did not classify the exit (a legacy emitter); consumers treat
	// empty as "completed" for compatibility.
	Status string
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
