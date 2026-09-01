package clichat

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// collectOne subscribes to kind and returns a function that waits for one
// event or fails.
func collectOne(t *testing.T, bus *events.Bus, kind events.Kind) func() events.Event {
	t.Helper()
	got := make(chan events.Event, 4)
	bus.Subscribe(kind, events.HandlerFunc(func(_ context.Context, ev events.Event) {
		got <- ev
	}))
	return func() events.Event {
		t.Helper()
		select {
		case ev := <-got:
			return ev
		case <-time.After(5 * time.Second):
			t.Fatal("no event reached the bus")
			return events.Event{}
		}
	}
}

// TestSubagentProgressCarriesSessionAndTurn is the attribution gate.
//
// emitSubagentProgress is package-level: it has no session of its own, so it
// published every subagent event with an empty SessionID. internal/hub's
// receiver drops any event whose SessionID does not match its own
// (externalEventBelongsToSession), so a second live surface watching this
// session saw the root loop's tool calls and none of its subagents' - the
// events were on the wire and silently discarded at the far end.
//
// The identity therefore has to ride on the event. This asserts it survives
// the publish, now via RegisterSessionBus's session-keyed registry (the
// dead global-bus singleton this test used to pin, SetGlobalBus, is gone).
func TestSubagentProgressCarriesSessionAndTurn(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	release := RegisterSessionBus("sess-abc", bus)
	t.Cleanup(release)

	wait := collectOne(t, bus, events.KindSubagentStart)

	emitSubagentProgress(agent.Event{
		Kind: agent.EventSubagentStart, Name: "reviewer", ToolCallID: "tc1",
		Origin: agent.EventOrigin{
			TaskID: "task-7", Agent: "reviewer", Depth: 1,
			SessionID: "sess-abc", TurnID: "turn:3",
		},
	})

	ev := wait()
	if ev.SessionID != "sess-abc" {
		t.Fatalf("SessionID = %q, want the dispatching session; a hub receiver drops this event", ev.SessionID)
	}
	if ev.TurnID != "turn:3" {
		t.Fatalf("TurnID = %q, want the dispatching turn", ev.TurnID)
	}
	if ev.AgentTask != "task-7" || ev.AgentName != "reviewer" || ev.AgentDepth != 1 {
		t.Fatalf("agent attribution lost: %+v", ev)
	}
}

// TestSubagentProgressWithoutAnOriginPublishesNoSession guards the other
// direction: a root-loop event reaching this sink must not be stamped with a
// borrowed session, and an event with an empty Origin.SessionID must never
// publish at all (fail closed on lookup by empty key) rather than borrow
// whatever the process happens to have registered.
func TestSubagentProgressWithoutAnOriginPublishesNoSession(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	// Deliberately register under a DIFFERENT session id than the emitted
	// event carries (none at all), so a bug that fell back to "the only
	// registered bus" would be caught here rather than passing by accident.
	release := RegisterSessionBus("unrelated-session", bus)
	t.Cleanup(release)

	got := make(chan events.Event, 1)
	bus.Subscribe(events.KindSubagentEnd, events.HandlerFunc(func(_ context.Context, ev events.Event) {
		got <- ev
	}))

	emitSubagentProgress(agent.Event{Kind: agent.EventSubagentEnd, Name: "x", ToolCallID: "tc2"})

	bus.Flush()
	select {
	case ev := <-got:
		t.Fatalf("event with no origin must not publish, got SessionID=%q", ev.SessionID)
	default:
	}
}
