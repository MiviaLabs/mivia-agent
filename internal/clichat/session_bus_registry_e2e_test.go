package clichat

import (
	"context"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestSessionBusRegistry_E2E_ChatsyncProjection is the required end-to-end
// test: a REAL events.Bus, bound through RegisterSessionBus, feeding a
// test-only chatsync.Projector constructed via NewProjector/Project (no
// chatsync package edits - this is exactly the seam
// internal/clichat/chat_sync.go's production wiring uses). It proves the
// four lifecycle kinds reach the projector as the documented wire types
// carrying AgentOrigin, and that a foreign-session event published on the
// SAME process-wide bus registry is filtered out by session identity
// before it ever reaches this session's projector.
func TestSessionBusRegistry_E2E_ChatsyncProjection(t *testing.T) {
	const sessionID = "sess-e2e"
	const foreignSessionID = "sess-e2e-foreign"
	const turnID = "turn:e2e-1"

	bus := events.New()
	t.Cleanup(bus.Close)
	release := RegisterSessionBus(sessionID, bus)
	t.Cleanup(release)

	// The foreign session shares the SAME process (a second live surface,
	// or a second turn's subagent registered on its own bus) but has its
	// own bus - registered here purely so LookupSessionBus resolves it,
	// proving isolation is by SessionID routing, not by "only one bus
	// exists in the process".
	foreignBus := events.New()
	t.Cleanup(foreignBus.Close)
	releaseForeign := RegisterSessionBus(foreignSessionID, foreignBus)
	t.Cleanup(releaseForeign)

	proj := chatsync.NewProjector(sessionID, 0, chatsync.ProjectorOptions{})
	snapshot := subscribeProjector(bus, proj)

	assertForeignSessionIsolation(t, bus, proj, foreignSessionID)

	// Now drive the four real lifecycle kinds through emitSubagentProgress
	// for THIS session and confirm each one reaches the wire vocabulary
	// with AgentOrigin attached.
	origin := agent.EventOrigin{
		TaskID: "task-e2e", Agent: "researcher", Depth: 1,
		SessionID: sessionID, TurnID: turnID,
	}
	emitSubagentProgress(agent.Event{Kind: agent.EventSubagentStart, Name: "researcher", ToolCallID: "tc-1", Origin: origin})
	emitSubagentProgress(agent.Event{Kind: agent.EventSubagentHeartbeat, Detail: "elapsed=1s steps=1", Origin: origin})
	emitSubagentProgress(agent.Event{Kind: agent.EventSubagentEnd, Name: "researcher", ToolCallID: "tc-1", Detail: "completed", Origin: origin})
	emitSubagentProgress(agent.Event{Kind: agent.EventSubagentDone, Name: "researcher", Status: "completed", Origin: origin})
	bus.Flush()

	got := snapshot()
	assertSubagentWireMultiset(t, got)
}

// assertSubagentWireMultiset asserts the four projected wire events: exactly
// one of each type, every envelope carrying the researcher AgentOrigin, and
// - on subagent.ended - the producer's terminal classification riding the
// status field. bus.SubscribeMany registers the handler on FOUR independent
// per-kind subscriptions, each with its own delivery goroutine
// (internal/events/bus.go), so cross-kind delivery order is not guaranteed
// even though publish order was; assert as a multiset, not by position.
func assertSubagentWireMultiset(t *testing.T, got []chatsync.WireEvent) {
	t.Helper()
	wantTypes := []string{
		chatsync.TypeSubagentToolStarted,
		chatsync.TypeSubagentProgress,
		chatsync.TypeSubagentToolEnded,
		chatsync.TypeSubagentEnded,
	}
	if len(got) != len(wantTypes) {
		t.Fatalf("projected %d wire events, want %d: %+v", len(got), len(wantTypes), got)
	}
	seen := make(map[string]int, len(wantTypes))
	for _, want := range wantTypes {
		seen[want] = 0
	}
	for _, ev := range got {
		if _, known := seen[ev.Type]; !known {
			t.Fatalf("unexpected wire type %q in %+v", ev.Type, got)
		}
		seen[ev.Type]++
		env := envelopeOf(t, ev.Payload)
		if env.Agent == nil {
			t.Fatalf("event %s carries no AgentOrigin", ev.Type)
		}
		if env.Agent.Task != "task-e2e" || env.Agent.Name != "researcher" || env.Agent.Depth != 1 {
			t.Fatalf("event %s AgentOrigin = %+v, want task-e2e/researcher/1", ev.Type, env.Agent)
		}
		// subagent.ended must carry the producer's terminal status: the Done
		// emitter sets Event.Status, which the publish path maps onto the
		// Detail the projector reads. Empty would be read as "completed"
		// downstream and mask canceled/failed runs.
		if ev.Type == chatsync.TypeSubagentEnded {
			done, ok := ev.Payload.(*chatsync.SubagentEndedPayload)
			if !ok {
				t.Fatalf("subagent.ended payload type %T", ev.Payload)
			}
			if done.Status != "completed" {
				t.Fatalf("subagent.ended status = %q, want the producer's terminal classification", done.Status)
			}
		}
	}
	for _, want := range wantTypes {
		if seen[want] != 1 {
			t.Fatalf("wire type %q appeared %d times, want exactly 1: %+v", want, seen[want], got)
		}
	}
}

// subscribeProjector pipes every subagent lifecycle event published on bus
// through proj and returns a snapshot func the assertions read after
// bus.Flush. bus.SubscribeMany registers one subscription per kind, each
// with its own delivery goroutine, so the mutex only protects the append and
// the snapshot - cross-kind delivery order is deliberately not asserted.
func subscribeProjector(bus *events.Bus, proj *chatsync.Projector) func() []chatsync.WireEvent {
	var mu sync.Mutex
	var projected []chatsync.WireEvent
	bus.SubscribeMany([]events.Kind{
		events.KindSubagentStart, events.KindSubagentEnd,
		events.KindSubagentHeartbeat, events.KindSubagentDone,
	}, events.HandlerFunc(func(_ context.Context, ev events.Event) {
		mu.Lock()
		defer mu.Unlock()
		projected = append(projected, proj.Project(ev)...)
	}))
	return func() []chatsync.WireEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]chatsync.WireEvent(nil), projected...)
	}
}

// assertForeignSessionIsolation proves a foreign session's subagent event,
// published through the SAME package-level emitSubagentProgress sink onto its
// own registered bus, never reaches this session's bus (the routing half) and
// that the projector's own strict SessionID check (ProjectWithDrops) filters
// it out even if routing had somehow failed (the defense-in-depth half).
func assertForeignSessionIsolation(t *testing.T, bus *events.Bus, proj *chatsync.Projector, foreignSessionID string) {
	t.Helper()
	var foreignSeen int
	var foreignMu sync.Mutex
	bus.SubscribeMany([]events.Kind{events.KindSubagentStart}, events.HandlerFunc(func(_ context.Context, ev events.Event) {
		if ev.SessionID == foreignSessionID {
			foreignMu.Lock()
			foreignSeen++
			foreignMu.Unlock()
		}
	}))
	emitSubagentProgress(agent.Event{
		Kind: agent.EventSubagentStart, Name: "other-agent", ToolCallID: "tc-foreign",
		Origin: agent.EventOrigin{SessionID: foreignSessionID, TurnID: "turn:foreign"},
	})
	bus.Flush()
	foreignMu.Lock()
	gotForeign := foreignSeen
	foreignMu.Unlock()
	if gotForeign != 0 {
		t.Fatalf("this session's bus observed %d events from a foreign session; routing crosstalk", gotForeign)
	}

	foreignWire := proj.Project(events.Event{
		Kind: events.KindSubagentStart, SessionID: foreignSessionID, TurnID: "turn:foreign",
		Name: "other-agent", ToolCallID: "tc-foreign",
	})
	if len(foreignWire) != 0 {
		t.Fatalf("projector accepted a foreign-session event: %+v", foreignWire)
	}
}

// envelopeOf extracts the embedded chatsync.Envelope from any of the typed
// wire payload structs via a narrow local interface, so this test does not
// need a type switch over every subagent payload type chatsync.go defines.
func envelopeOf(t *testing.T, payload any) chatsync.Envelope {
	t.Helper()
	switch p := payload.(type) {
	case *chatsync.SubagentToolStartedPayload:
		return p.Envelope
	case *chatsync.SubagentToolEndedPayload:
		return p.Envelope
	case *chatsync.SubagentProgressPayload:
		return p.Envelope
	case *chatsync.SubagentEndedPayload:
		return p.Envelope
	default:
		t.Fatalf("unexpected wire payload type %T", payload)
		return chatsync.Envelope{}
	}
}
