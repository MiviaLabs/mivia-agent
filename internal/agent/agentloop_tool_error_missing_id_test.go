package agent

// Regression test for the empty-call-ID gap in hostAuthorizedToolMessage
// (agentloop_tool_error.go): a deferred call whose sdkshape.ToolCall.ID is
// empty but whose Name is set must still get a recorded outcome, keyed by
// Name, when RunUnadmittedTool fails. Without the name fallback the failure
// vanishes from turn state and bridgeToolCallEnd (agentloop_events.go) later
// finds no outcome under the name key, so it reports the call as an
// already-served duplicate instead of a failure - on every operator-facing
// surface (TUI, --json, chat-sync).
//
// call.ID goes empty when a provider stream sends a tool-call NAME delta
// before, or without, an ID delta: mergeToolCallDelta in the vendored SDK
// fills ID and Name independently from whichever chunk supplies each first.

import (
	"context"
	"encoding/json"
	"testing"

	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// unmarshalableSchemaTool is a deferred tool whose Parameters() cannot be
// JSON-marshaled, so sdkadapter.ConvertTool (called from RunUnadmittedTool)
// always fails. That failure is what hostAuthorizedToolMessage must convert
// into a recorded outcome even when the call carries no ID.
type unmarshalableSchemaTool struct{}

func (unmarshalableSchemaTool) Name() string        { return "deferred_tool" }
func (unmarshalableSchemaTool) Description() string { return "deferred test tool" }
func (unmarshalableSchemaTool) Parameters() map[string]any {
	// A channel value has no JSON representation, so json.Marshal of this
	// schema always errors - the deterministic way to make ConvertTool fail
	// without depending on argument decoding or the run path.
	return map[string]any{"bad": make(chan int)}
}
func (unmarshalableSchemaTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}

// TestHostAuthorizedToolMessage_EmptyCallIDStillRecordsFailure drives the
// same OnToolCallError path TestUnadmittedHandlerHookRunsReachTheTranscript
// uses, but with call.ID empty (the streamed-name-before-id case). It
// asserts the failure is recorded under the call's Name, not silently
// dropped.
func TestHostAuthorizedToolMessage_EmptyCallIDStillRecordsFailure(t *testing.T) {
	opts := Options{UnadmittedToolHandler: func(context.Context, string, json.RawMessage) UnadmittedToolResult {
		return UnadmittedToolResult{Handled: true, Execute: unmarshalableSchemaTool{}}
	}}
	turn := newSDKTurnState()
	reporter := sdkToolCallErrorReporter(opts, turn)

	call := sdkshape.ToolCall{ID: "", Name: "deferred_tool", Arguments: json.RawMessage(`{}`)}
	msg, err := reporter(context.Background(), call, sdktools.ErrUnknownName)
	if err != nil {
		t.Fatalf("reporter: %v", err)
	}

	outcome := turn.takeToolCallOutcome("deferred_tool")
	if outcome == nil {
		t.Fatalf("no outcome recorded under the name key %q; the failure vanished from turn state", "deferred_tool")
	}
	if !outcome.failed {
		t.Fatalf("outcome.failed = false, want true (RunUnadmittedTool errored)")
	}

	// The model-visible message must still carry the error body, whether or
	// not the outcome was recorded - that half of the contract already held.
	if msg.Content == "" {
		t.Fatalf("Content is empty, want the RunUnadmittedTool error body")
	}
}
