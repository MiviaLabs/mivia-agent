package agent

import (
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

type EventKind string

const (
	EventAssistant         EventKind = "assistant"
	EventToolStart         EventKind = "tool_start"
	EventToolEnd           EventKind = "tool_end"
	EventStep              EventKind = "step"
	EventPrune             EventKind = "prune"
	EventToolParallel      EventKind = "tool_parallel"
	EventSubagentStart     EventKind = "subagent_start"
	EventSubagentEnd       EventKind = "subagent_end"
	EventSubagentHeartbeat EventKind = "subagent_heartbeat"
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
)

// EventOrigin identifies the agent that produced an event. The zero value
// means the session's root loop. Subagent handlers stamp it (see
// subagents.StampEventOrigin) so nested tool events stay attributable to
// their run - without it, parallel agents are indistinguishable in the UI.
type EventOrigin struct {
	TaskID string // runtime request/task id - the attribution key
	Agent  string // dispatched subagent/skill name
	Depth  int    // nesting depth (root loop = 0)
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
