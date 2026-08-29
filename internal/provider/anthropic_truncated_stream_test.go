package provider

// A torn Anthropic stream must not be presented as a finished answer when it
// was cut mid tool call. finishAnthropicStream substitutes "{}" for a tool_use
// block whose arguments never arrived, and the early-close path returned that
// with a nil error - so the agent loop executed a tool with fabricated or
// syntactically invalid arguments and nothing marked the turn transient.
//
// The OpenAI-compatible reader has refused this since validateTruncatedToolCalls;
// the native client never got the same guard.

import (
	"errors"
	"strings"
	"testing"
)

const anthropicToolCallPreamble = "event: message_start\n" +
	`data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"run_command"}}` + "\n\n"

// Arguments arrive across several deltas. Here a whole delta event lands
// cleanly and the connection then ends, so the SSE framing is intact but the
// accumulated arguments are half a JSON document.
func TestAnthropicTruncatedToolArgumentsRejected(t *testing.T) {
	stream := anthropicToolCallPreamble +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"rm -rf /tm"}}` + "\n\n"

	resp, err := decodeAnthropicStream(strings.NewReader(stream), nil)
	if err == nil {
		t.Fatalf("a stream cut mid tool call must not be returned as an answer; got %+v", resp)
	}
	if !IsTransient(err) {
		t.Errorf("a torn stream never delivered an answer, so it must be transient: %v", err)
	}
	var transient *TransientError
	if !errors.As(err, &transient) {
		t.Errorf("error should carry TransientError, got %T: %v", err, err)
	}
}

// Cut right after the tool call opened: no arguments arrived at all, and the
// "{}" substitution would invent an empty-argument call the model never made.
func TestAnthropicTruncatedBeforeToolArgumentsRejected(t *testing.T) {
	resp, err := decodeAnthropicStream(strings.NewReader(anthropicToolCallPreamble), nil)
	if err == nil {
		t.Fatalf("a tool call whose arguments never arrived must not be fabricated; got %+v", resp)
	}
	if !IsTransient(err) {
		t.Errorf("expected a transient failure, got %v", err)
	}
}

// Guard against over-strictness: a COMPLETE stream ends with message_stop, and
// there an absent input_json_delta really does mean "this tool takes no
// arguments". That must still decode to {}.
func TestAnthropicCompleteToolCallWithoutArgumentsAccepted(t *testing.T) {
	stream := anthropicToolCallPreamble +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	resp, err := decodeAnthropicStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatalf("a completed stream must be accepted: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Function.Arguments != "{}" {
		t.Errorf("arguments = %q, want %q", resp.ToolCalls[0].Function.Arguments, "{}")
	}
}

// Text-only truncation keeps the existing tolerant convention: a partial
// answer with no tool call is still an answer, and the caller can use it.
func TestAnthropicTruncatedTextOnlyStillReturnsPartial(t *testing.T) {
	stream := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial thought"}}` + "\n\n"

	resp, err := decodeAnthropicStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatalf("a text-only truncation stays tolerant: %v", err)
	}
	if resp.Content != "partial thought" {
		t.Errorf("Content = %q, want %q", resp.Content, "partial thought")
	}
}

// A complete stream carrying real arguments is unaffected.
func TestAnthropicCompleteToolCallWithArgumentsAccepted(t *testing.T) {
	resp, err := decodeAnthropicStream(strings.NewReader(anthropicSSEAnswer), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Function.Arguments != `{"path":"."}` {
		t.Fatalf("tool calls = %+v, want one call with {\"path\":\".\"}", resp.ToolCalls)
	}
}
