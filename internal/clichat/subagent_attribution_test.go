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
// the publish.
func TestSubagentProgressCarriesSessionAndTurn(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	SetGlobalBus(bus)
	t.Cleanup(func() { SetGlobalBus(nil) })

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
// borrowed session. An empty SessionID is correctly dropped by a hub receiver;
// a WRONG one would render another conversation's activity into this one.
func TestSubagentProgressWithoutAnOriginPublishesNoSession(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	SetGlobalBus(bus)
	t.Cleanup(func() { SetGlobalBus(nil) })

	wait := collectOne(t, bus, events.KindSubagentEnd)

	emitSubagentProgress(agent.Event{Kind: agent.EventSubagentEnd, Name: "x", ToolCallID: "tc2"})

	if ev := wait(); ev.SessionID != "" {
		t.Fatalf("SessionID = %q for an event with no origin, want empty", ev.SessionID)
	}
}
