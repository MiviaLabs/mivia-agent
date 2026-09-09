package agent

import (
	"context"
	"testing"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkevents "github.com/MiviaLabs/mivia-ai-sdk/events"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"

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

// TestBridgeToolCallEndFallsBackToToolName pins the call-key fallback:
// a provider that omits the tool-call id must still produce an
// operator tool_end, keyed by the tool name. Without the fallback the
// key stays empty and the event is dropped entirely.
func TestBridgeToolCallEndFallsBackToToolName(t *testing.T) {
	for _, tc := range []struct {
		name    string
		call    sdkshape.ToolCall
		wantKey string
	}{
		{name: "id present", call: sdkshape.ToolCall{ID: "call-1", Name: "my_tool"}, wantKey: "call-1"},
		{name: "id omitted", call: sdkshape.ToolCall{Name: "my_tool"}, wantKey: "my_tool"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var captured []Event
			opts := Options{OnEvent: func(e Event) { captured = append(captured, e) }}
			ctx := sdkagentloop.WithToolCall(context.Background(), tc.call)
			bridgeToolCallEnd(opts, newSDKTurnState(), ctx)
			if len(captured) != 1 {
				t.Fatalf("captured %d events, want exactly 1: %+v", len(captured), captured)
			}
			got := captured[0]
			if got.Kind != EventToolEnd || got.ToolCallID != tc.wantKey {
				t.Fatalf("captured %+v, want Kind=%s ToolCallID=%q", got, EventToolEnd, tc.wantKey)
			}
			if got.Name != "my_tool" {
				t.Fatalf("Name = %q, want my_tool", got.Name)
			}
			// A call with no recorded outcome never reached the dispatcher
			// shim, so it never ran: the SDK rejected it (unknown tool,
			// scope denial, schema violation) or a hook vetoed it. See
			// pre_shim_failure_reporting_test.go for why this is not the
			// dedup-served vocabulary.
			if got.Detail != "failed" {
				t.Fatalf("Detail = %q, want failed for a call that never ran", got.Detail)
			}
		})
	}
}
