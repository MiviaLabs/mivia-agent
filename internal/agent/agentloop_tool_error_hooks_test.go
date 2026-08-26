package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

func collectHookEventsFromToolError(t *testing.T, opts Options, call sdkshape.ToolCall) (sdkshape.Message, []Event) {
	t.Helper()
	var got []Event
	opts.OnEvent = func(e Event) {
		if e.Kind == EventHook {
			got = append(got, e)
		}
	}
	reporter := sdkToolCallErrorReporter(opts, newSDKTurnState())
	msg, err := reporter(context.Background(), call, sdktools.ErrUnknownName)
	if err != nil {
		t.Fatalf("reporter: %v", err)
	}
	return msg, got
}

// The deferred-tool analogue of TestDispatcherShimEmitsHookEventsForPreAndPost:
// a call served synchronously by UnadmittedToolHandler must surface every
// hook run it reports, correlated to the SDK call's own ID.
func TestUnadmittedHandlerHookRunsReachTheTranscript(t *testing.T) {
	opts := Options{UnadmittedToolHandler: func(context.Context, string, json.RawMessage) UnadmittedToolResult {
		return UnadmittedToolResult{Handled: true, Ran: true, Content: "body", HookRuns: []runtime.HookRun{
			{Event: "PreToolUse", Program: "guard.sh"},
			{Event: "PostToolUse", Program: "fmt.sh"},
		}}
	}}
	msg, events := collectHookEventsFromToolError(t, opts, sdkshape.ToolCall{ID: "call1", Name: "grep"})

	if len(events) != 2 {
		t.Fatalf("got %d hook events, want 2: %+v", len(events), events)
	}
	if events[0].ToolCallID != "call1" || events[1].ToolCallID != "call1" {
		t.Fatalf("hook events must correlate to the SDK call id, got %+v", events)
	}
	if msg.Content != "body" {
		t.Fatalf("Content = %q, want the unprefixed served result", msg.Content)
	}
}

// A PreToolUse block (Ran=false) is exactly the run an operator most needs
// to see, and the denial framing on Content must be unchanged.
func TestUnadmittedHandlerBlockedHookRunReachesTheTranscript(t *testing.T) {
	opts := Options{UnadmittedToolHandler: func(context.Context, string, json.RawMessage) UnadmittedToolResult {
		return UnadmittedToolResult{Handled: true, Ran: false, Content: "denial text", HookRuns: []runtime.HookRun{
			{Event: "PreToolUse", Program: "guard.sh", Denied: true, Output: "policy forbids this"},
		}}
	}}
	msg, events := collectHookEventsFromToolError(t, opts, sdkshape.ToolCall{ID: "call1", Name: "grep"})

	if len(events) != 1 || !events[0].Denied {
		t.Fatalf("want 1 denied hook event, got %+v", events)
	}
	if msg.Content != "error: denial text" {
		t.Fatalf("Content = %q, want the unchanged error-framed denial text", msg.Content)
	}
}

// Nil HookRuns (the common case: no hooks configured) must emit nothing -
// guards against a phantom row on every deferred call.
func TestUnadmittedHandlerWithNoHookRunsEmitsNothing(t *testing.T) {
	opts := Options{UnadmittedToolHandler: func(context.Context, string, json.RawMessage) UnadmittedToolResult {
		return UnadmittedToolResult{Handled: true, Ran: true, Content: "body"}
	}}
	_, events := collectHookEventsFromToolError(t, opts, sdkshape.ToolCall{ID: "call1", Name: "grep"})
	if len(events) != 0 {
		t.Fatalf("want 0 hook events, got %+v", events)
	}
}

// An ID-less call falls back to the tool name for correlation, matching
// recordToolOutcomeWithPreview's own fallback.
func TestUnadmittedHandlerHookRunsFallBackToNameForCorrelation(t *testing.T) {
	opts := Options{UnadmittedToolHandler: func(context.Context, string, json.RawMessage) UnadmittedToolResult {
		return UnadmittedToolResult{Handled: true, Ran: true, Content: "body", HookRuns: []runtime.HookRun{
			{Event: "PostToolUse", Program: "fmt.sh"},
		}}
	}}
	_, events := collectHookEventsFromToolError(t, opts, sdkshape.ToolCall{ID: "", Name: "grep"})
	if len(events) != 1 || events[0].ToolCallID != "grep" {
		t.Fatalf("want 1 hook event correlated to the tool name, got %+v", events)
	}
}
