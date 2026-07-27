package events

import "time"

// Kind is the type for event kinds.
type Kind string

const (
	// Agent loop events (mirror agent.EventKind values).
	KindAssistant         Kind = "assistant"
	KindToolStart         Kind = "tool_start"
	KindToolEnd           Kind = "tool_end"
	KindStep              Kind = "step"
	KindPrune             Kind = "prune"
	KindToolParallel      Kind = "tool_parallel"
	KindSubagentStart     Kind = "subagent_start"
	KindSubagentEnd       Kind = "subagent_end"
	KindSubagentHeartbeat Kind = "subagent_heartbeat"

	// Session/turn lifecycle events.
	KindSessionStart Kind = "session_start"
	KindSessionEnd   Kind = "session_end"
	KindTurnStart    Kind = "turn_start"
	KindTurnEnd      Kind = "turn_end"

	// UI/system events.
	KindUIResize     Kind = "ui_resize"
	KindUserInput    Kind = "user_input"
	KindUIReady      Kind = "ui_ready"
	KindConfigChange Kind = "config_change"

	// Error events.
	KindError Kind = "error"
)

// Event is the universal event type for the event bus.
type Event struct {
	Kind       Kind
	Timestamp  time.Time
	SessionID  string
	TurnID     string
	ToolCallID string
	Name       string
	Detail     string
	Content    string
	Input      string
	Output     string
	Metadata   map[string]string
	Err        error
}

// NewEvent creates an Event with the given Kind and the current timestamp.
func NewEvent(kind Kind) Event {
	return Event{Kind: kind, Timestamp: time.Now()}
}
