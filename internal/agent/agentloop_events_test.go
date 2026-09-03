package agent

import (
	"context"
	"testing"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkevents "github.com/MiviaLabs/mivia-ai-sdk/events"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestTheBridgeReportsEachCompletedAssistantMessage pins the one signal the
// chat-sync projector needs to release a held tail before the turn ends: the
// SDK loop's per-message EventAssistant, forwarded as a content-free
// KindAssistant flag. Before the bridge subscribed to it, Emit failed with
// "no subscriber" and the CLI never learned a message had completed.
func TestTheBridgeReportsEachCompletedAssistantMessage(t *testing.T) {
	var captured []Event
	opts := Options{OnEvent: func(e Event) { captured = append(captured, e) }}
	bus := bridgeAgentLoopEvents(opts, &sdkTurnState{})

	err := bus.Emit(context.Background(), sdkevents.Event{Name: sdkagentloop.EventAssistant, Data: "x"})
	if err != nil {
		t.Fatalf("Emit(%s) = %v; the bridge has no subscriber for the loop's "+
			"message-complete event", sdkagentloop.EventAssistant, err)
	}
	if len(captured) != 1 {
		t.Fatalf("captured %d events, want exactly 1: %+v", len(captured), captured)
	}
	got := captured[0]
	want := Event{Kind: EventAssistant, Detail: events.DetailAssistantComplete}
	if got.Kind != want.Kind || got.Detail != want.Detail || got.Content != "" {
		t.Fatalf("captured %+v, want Kind=%s Detail=%q and NO content - the "+
			"flag must not read as a second aggregate", got, want.Kind, want.Detail)
	}
}
