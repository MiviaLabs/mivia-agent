package cli

import (
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// sessionInvocationSink adapts dispatcher invocation lifecycle events to the
// session event bus. The bus is read when the event publishes because the
// interactive dispatcher is attached before RunTUI creates the bus. A nil
// bus (REPL, one-shot, tests) makes the sink a no-op. The closure is safe
// for concurrent callers: bus.Publish is goroutine-safe. Relocated from
// tui_run.go (a legacytui-destined file); chat_repl.go is its sole
// caller, and this single-purpose helper reads more clearly in its own
// file than folded into either caller or destination.
func sessionInvocationSink(sess *chat.Session) func(runtime.Event) {
	return func(e runtime.Event) {
		var bus *events.Bus
		if sess != nil {
			bus = sess.EventBus
		}
		if bus == nil {
			return
		}
		bus.Publish(invocationEvent(e))
	}
}

// invocationEvent maps one runtime lifecycle observation to a session bus
// event. The three lifecycle kinds map by name; any other type is terminal
// and surfaces as completed.
func invocationEvent(e runtime.Event) events.Event {
	kind := events.KindInvocationCompleted
	switch e.Type {
	case "started":
		kind = events.KindInvocationStarted
	case "retrying":
		kind = events.KindInvocationRetrying
	}
	return events.Event{
		Kind:      kind,
		Timestamp: time.Now(),
		Name:      e.Metadata.Name,
		Detail:    e.Metadata.Kind + " " + e.Metadata.Status,
		Metadata: map[string]string{
			"id":     e.Metadata.ID,
			"turn":   e.Metadata.TurnID,
			"parent": e.Metadata.ParentID,
		},
		AgentTask:  e.Metadata.ID,
		AgentName:  e.Metadata.Name,
		AgentDepth: 1,
	}
}
