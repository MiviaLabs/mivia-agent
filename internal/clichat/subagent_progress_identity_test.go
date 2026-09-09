package clichat

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestEmitSubagentProgress_CopiesIdentity pins emitSubagentProgress's own
// e.Identity != nil branch: a published wire event must carry a COPY of
// the origin event's Identity, not a nil one, when the source event set it.
func TestEmitSubagentProgress_CopiesIdentity(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	release := RegisterSessionBus("sess-identity-test", bus)
	t.Cleanup(release)

	wait := collectOne(t, bus, events.KindSubagentStart)

	ident := events.Identity{DefinitionName: "worker", DefinitionSource: "project", InstanceID: "inst-1", ModelGeneration: 3}
	emitSubagentProgress(agent.Event{
		Kind: agent.EventSubagentStart, Name: "worker",
		Identity: &ident,
		Origin:   agent.EventOrigin{TaskID: "t1", SessionID: "sess-identity-test"},
	})

	ev := wait()
	if ev.Identity == nil {
		t.Fatal("published event has a nil Identity despite the source event setting one")
	}
	if *ev.Identity != ident {
		t.Fatalf("published Identity = %+v, want %+v", *ev.Identity, ident)
	}
}
