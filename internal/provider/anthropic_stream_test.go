package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// sseEvent formats one Anthropic-shaped SSE event.
func sseEvent(eventType string, data map[string]any) string {
	data["type"] = eventType
	encoded, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, encoded)
}

func plainTextStreamFixture() string {
	var b strings.Builder
	b.WriteString(sseEvent("message_start", map[string]any{"message": map[string]any{"usage": map[string]any{"input_tokens": 5}}}))
	b.WriteString(sseEvent("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}}))
	b.WriteString(sseEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "Hel"}}))
	b.WriteString(sseEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "lo"}}))
	b.WriteString(sseEvent("content_block_stop", map[string]any{"index": 0}))
	b.WriteString(sseEvent("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 2}}))
	b.WriteString(sseEvent("message_stop", map[string]any{}))
	return b.String()
}

// Text deltas write to the StreamWriter as they arrive, and the final
// Response accumulates the same concatenated text with the mapped
// FinishReason - the streaming path must agree with the non-stream path's
// Response shape.
func TestAnthropicChatTurnStreamPlainText(t *testing.T) {
	client := newTestAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true {
			t.Errorf("request body stream = %v, want true", body["stream"])
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(plainTextStreamFixture()))
	})

	var streamed bytes.Buffer
	req := Request{
		Model:        "claude-sonnet-5",
		Messages:     []Message{{Role: RoleUser, Content: "hi"}},
		Stream:       true,
		StreamWriter: &streamed,
	}
	resp, err := client.ChatTurn(context.Background(), req)
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if streamed.String() != "Hello" {
		t.Fatalf("bytes written to StreamWriter = %q, want %q", streamed.String(), "Hello")
	}
	if resp.Content != "Hello" {
		t.Fatalf("Response.Content = %q, want %q", resp.Content, "Hello")
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("FinishReason = %q, want stop", resp.FinishReason)
	}
	if !resp.TokenUsage.Reported || resp.TokenUsage.InputTokens != 5 || resp.TokenUsage.OutputTokens != 2 {
		t.Fatalf("TokenUsage = %+v", resp.TokenUsage)
	}
}

// ChatStream (the plain io.Writer entry point, as used by callers with no
// tool calls) returns the same accumulated text ChatTurn's streaming path
// would.
func TestAnthropicChatStream(t *testing.T) {
	client := newTestAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(plainTextStreamFixture()))
	})
	var buf bytes.Buffer
	content, err := client.ChatStream(context.Background(), Request{Model: "claude-sonnet-5", Messages: []Message{{Role: RoleUser, Content: "hi"}}}, &buf)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if content != "Hello" || buf.String() != "Hello" {
		t.Fatalf("content=%q buf=%q, want both %q", content, buf.String(), "Hello")
	}
}

// A streamed tool_use block accumulates its partial_json fragments into a
// valid JSON object, surfaced as ToolCall.Function.Arguments (the OpenAI
// shape's string convention) with the id preserved.
func TestAnthropicChatTurnStreamToolUse(t *testing.T) {
	var b strings.Builder
	b.WriteString(sseEvent("message_start", map[string]any{"message": map[string]any{"usage": map[string]any{}}}))
	b.WriteString(sseEvent("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_1", "name": "read_file"}}))
	b.WriteString(sseEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"path":`}}))
	b.WriteString(sseEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `"a.go"}`}}))
	b.WriteString(sseEvent("content_block_stop", map[string]any{"index": 0}))
	b.WriteString(sseEvent("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]any{}}))
	b.WriteString(sseEvent("message_stop", map[string]any{}))

	client := newTestAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(b.String()))
	})
	var buf bytes.Buffer
	req := Request{Model: "claude-sonnet-5", Messages: []Message{{Role: RoleUser, Content: "hi"}}, Stream: true, StreamWriter: &buf}
	resp, err := client.ChatTurn(context.Background(), req)
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if resp.FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q, want tool_calls", resp.FinishReason)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "toolu_1" {
		t.Fatalf("ToolCalls = %#v", resp.ToolCalls)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(resp.ToolCalls[0].Function.Arguments), &args); err != nil || args["path"] != "a.go" {
		t.Fatalf("Arguments = %q, want the reassembled JSON object", resp.ToolCalls[0].Function.Arguments)
	}
	if buf.Len() != 0 {
		t.Fatalf("StreamWriter got %q, want nothing written for a tool-only turn", buf.String())
	}
}

// A streamed thinking block (thinking_delta + signature_delta fragments)
// reassembles into the same raw-JSON ReasoningContent shape the non-stream
// path produces, so replay is format-identical regardless of which path
// served the turn.
func TestAnthropicChatTurnStreamThinkingBlock(t *testing.T) {
	var b strings.Builder
	b.WriteString(sseEvent("message_start", map[string]any{"message": map[string]any{"usage": map[string]any{}}}))
	b.WriteString(sseEvent("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "thinking"}}))
	b.WriteString(sseEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": "considering "}}))
	b.WriteString(sseEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": "the request"}}))
	b.WriteString(sseEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "signature_delta", "signature": "sig-xyz"}}))
	b.WriteString(sseEvent("content_block_stop", map[string]any{"index": 0}))
	b.WriteString(sseEvent("content_block_start", map[string]any{"index": 1, "content_block": map[string]any{"type": "text", "text": ""}}))
	b.WriteString(sseEvent("content_block_delta", map[string]any{"index": 1, "delta": map[string]any{"type": "text_delta", "text": "done"}}))
	b.WriteString(sseEvent("content_block_stop", map[string]any{"index": 1}))
	b.WriteString(sseEvent("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{}}))
	b.WriteString(sseEvent("message_stop", map[string]any{}))

	client := newTestAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(b.String()))
	})
	var buf bytes.Buffer
	req := Request{Model: "claude-sonnet-5", Messages: []Message{{Role: RoleUser, Content: "hi"}}, Stream: true, StreamWriter: &buf}
	resp, err := client.ChatTurn(context.Background(), req)
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if resp.Content != "done" {
		t.Fatalf("Content = %q, want %q", resp.Content, "done")
	}
	// ReasoningContent is the plain reassembled thinking text - no JSON
	// envelope, no signature (nothing downstream can safely display or
	// replay a signature; see anthropicThinkingDisplayText's doc comment).
	if resp.ReasoningContent != "considering the request" {
		t.Fatalf("ReasoningContent = %q, want the plain reassembled thinking text", resp.ReasoningContent)
	}
}

// A non-2xx status on the streaming path is a wrapped error, the same
// convention the non-stream path follows - never a panic on the SSE
// decoder, and never a response body misread as an event stream.
func TestAnthropicChatTurnStreamNonOKStatus(t *testing.T) {
	client := newTestAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error"}}`))
	})
	req := Request{Model: "claude-sonnet-5", Messages: []Message{{Role: RoleUser, Content: "hi"}}, Stream: true, StreamWriter: &bytes.Buffer{}}
	_, err := client.ChatTurn(context.Background(), req)
	if err == nil {
		t.Fatal("want an error for a non-2xx streaming response")
	}
	if !strings.Contains(err.Error(), "HTTP 400") || !strings.Contains(err.Error(), "invalid_request_error") {
		t.Fatalf("error = %q, want it to name the status and error type", err.Error())
	}
}

// An in-band SSE "error" event mid-stream ends the read as an error rather
// than silently truncating the answer and presenting it as complete.
func TestAnthropicChatTurnStreamInBandError(t *testing.T) {
	var b strings.Builder
	b.WriteString(sseEvent("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}}))
	b.WriteString(sseEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "partial"}}))
	b.WriteString(sseEvent("error", map[string]any{"error": map[string]any{"type": "overloaded_error"}}))

	client := newTestAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(b.String()))
	})
	req := Request{Model: "claude-sonnet-5", Messages: []Message{{Role: RoleUser, Content: "hi"}}, Stream: true, StreamWriter: &bytes.Buffer{}}
	_, err := client.ChatTurn(context.Background(), req)
	if err == nil {
		t.Fatal("want an error for an in-band SSE error event")
	}
	if !strings.Contains(err.Error(), "overloaded_error") {
		t.Fatalf("error = %q, want it to name the in-band error type", err.Error())
	}
}

// A stream that ends without a message_stop event (an early connection
// close) returns whatever was decoded rather than an error - the same
// tolerant convention the OpenAI-compatible stream decoder applies.
func TestAnthropicChatTurnStreamEarlyClose(t *testing.T) {
	var b strings.Builder
	b.WriteString(sseEvent("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}}))
	b.WriteString(sseEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "partial answer"}}))

	client := newTestAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(b.String()))
	})
	req := Request{Model: "claude-sonnet-5", Messages: []Message{{Role: RoleUser, Content: "hi"}}, Stream: true, StreamWriter: &bytes.Buffer{}}
	resp, err := client.ChatTurn(context.Background(), req)
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if resp.Content != "partial answer" {
		t.Fatalf("Content = %q, want the partial text preserved", resp.Content)
	}
}
