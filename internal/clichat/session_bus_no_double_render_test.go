package clichat

import (
	"context"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestNoDoubleRender is an IN-PROCESS-ONLY test: one published subagent
// lifecycle event must produce exactly one bus delivery and exactly one
// projector call, through the session-keyed registry this plan installs.
//
// hub-echo (internal/hub relaying this process's own publish back to
// itself, which a naive dedup gap could double-render) is deliberately NOT
// exercisable here and is out of scope for this test: the TUI - the one
// surface that registers its buses through SessionBusRegistrar at all -
// never joins the hub. internal/clichat/chat_hub.go's own package comment
// states this outright ("THE TUI DOES NOT [join hub]... uiadapter/build.go
// constructs its session with EventBus: nil"), and
// internal/uiadapter/build.go:204 sets EventBus: nil at construction for
// exactly that reason. Only the classic REPL and line mode call JoinHub
// (internal/clichat/chat_hub.go's startClassicReplHub/startLineModeHub),
// and neither of those surfaces is what this plan routes subagent
// lifecycle events through to chat-sync. A single in-process events.Bus
// with a single subscriber is therefore the correct and complete model of
// what this wiring can ever deliver.
func TestNoDoubleRender(t *testing.T) {
	const sessionID = "sess-no-double-render"

	bus := events.New()
	t.Cleanup(bus.Close)
	release := RegisterSessionBus(sessionID, bus)
	t.Cleanup(release)

	proj := chatsync.NewProjector(sessionID, 0, chatsync.ProjectorOptions{})

	var mu sync.Mutex
	var busDeliveries int
	var projectorCalls int
	var lastWire []chatsync.WireEvent
	bus.Subscribe(events.KindSubagentStart, events.HandlerFunc(func(_ context.Context, ev events.Event) {
		mu.Lock()
		defer mu.Unlock()
		busDeliveries++
		projectorCalls++
		lastWire = proj.Project(ev)
	}))

	emitSubagentProgress(agent.Event{
		Kind: agent.EventSubagentStart, Name: "researcher", ToolCallID: "tc-once",
		Origin: agent.EventOrigin{TaskID: "task-1", Agent: "researcher", SessionID: sessionID, TurnID: "turn:1"},
	})
	bus.Flush()

	mu.Lock()
	defer mu.Unlock()
	if busDeliveries != 1 {
		t.Fatalf("bus deliveries = %d, want exactly 1", busDeliveries)
	}
	if projectorCalls != 1 {
		t.Fatalf("projector calls = %d, want exactly 1", projectorCalls)
	}
	if len(lastWire) != 1 || lastWire[0].Type != chatsync.TypeSubagentToolStarted {
		t.Fatalf("projected wire events = %+v, want exactly one %s", lastWire, chatsync.TypeSubagentToolStarted)
	}
}
