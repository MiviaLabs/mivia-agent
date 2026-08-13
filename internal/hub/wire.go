package hub

import (
	"errors"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// WireEvent is the hub socket's newline-delimited JSON framing: a bounded,
// explicit projection of events.Event, not the raw struct - that carries an
// `error` field encoding/json cannot round-trip, and every internal Kind,
// including process-local ones (UI resize, config change) this package
// never wants to commit to a cross-process wire contract.
type WireEvent struct {
	Kind       string    `json:"kind"`
	Timestamp  time.Time `json:"timestamp"`
	SessionID  string    `json:"session_id"`
	TurnID     string    `json:"turn_id"`
	ToolCallID string    `json:"tool_call_id,omitempty"`
	Name       string    `json:"name,omitempty"`
	// Detail carries KindTurnStart's user-submitted text (events.Event.Detail
	// - see relayedKinds' doc comment); other relayed kinds don't use it.
	Detail     string `json:"detail,omitempty"`
	Content    string `json:"content,omitempty"`
	Input      string `json:"input,omitempty"`
	Output     string `json:"output,omitempty"`
	ErrorText  string `json:"error,omitempty"`
	AgentTask  string `json:"agent_task,omitempty"`
	AgentName  string `json:"agent_name,omitempty"`
	AgentDepth int    `json:"agent_depth,omitempty"`
}

// toWire projects an internal events.Event onto the wire format. Metadata
// and Identity are dropped: neither is meaningful to a second live surface
// today, and both are free-form/typed internal detail this package doesn't
// want to commit to a public wire contract.
func toWire(ev events.Event) WireEvent {
	w := WireEvent{
		Kind: string(ev.Kind), Timestamp: ev.Timestamp,
		SessionID: ev.SessionID, TurnID: ev.TurnID, ToolCallID: ev.ToolCallID,
		Name: ev.Name, Detail: ev.Detail, Content: ev.Content, Input: ev.Input, Output: ev.Output,
		AgentTask: ev.AgentTask, AgentName: ev.AgentName, AgentDepth: ev.AgentDepth,
	}
	if ev.Err != nil {
		w.ErrorText = ev.Err.Error()
	}
	return w
}

// fromWire reconstructs the subset of events.Event a received WireEvent can
// populate - the inverse of toWire, used by a hub member rendering another
// process's events onto this process's own live surface.
func fromWire(w WireEvent) events.Event {
	ev := events.Event{
		Kind: events.Kind(w.Kind), Timestamp: w.Timestamp,
		SessionID: w.SessionID, TurnID: w.TurnID, ToolCallID: w.ToolCallID,
		Name: w.Name, Detail: w.Detail, Content: w.Content, Input: w.Input, Output: w.Output,
		AgentTask: w.AgentTask, AgentName: w.AgentName, AgentDepth: w.AgentDepth,
	}
	if w.ErrorText != "" {
		ev.Err = errors.New(w.ErrorText)
	}
	return ev
}
