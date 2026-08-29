package provider

// The wire-stream transport is stream:true on the wire with the non-stream
// contract on the return path. Every nested and subagent turn asks for it
// (subagents wire WireStreamTransport from [subagents] wire_stream, which
// defaults on), and OpenAICompat.ChatTurn honors it - but the native Anthropic
// client ignored req.StreamTransport entirely and sent a plain non-stream
// request.
//
// That is not a style difference. A non-stream completion sends no byte until
// the whole generation is done, so its wait for response headers IS the
// model's thinking time, and the transport's 120-second header bound then caps
// every generation at two minutes regardless of the configured request budget.
// Streaming on the wire returns headers immediately and moves the model's work
// into the body phase, where the watchdogs measure progress instead.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// anthropicWireProbe records whether the request body asked for a stream, and
// answers in the shape that request implies.
type anthropicWireProbe struct {
	sawStream atomic.Bool
	srv       *httptest.Server
}

func newAnthropicWireProbe(t *testing.T, sse string, jsonBody string) *anthropicWireProbe {
	t.Helper()
	probe := &anthropicWireProbe{}
	probe.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		streamed, _ := body["stream"].(bool)
		probe.sawStream.Store(streamed)
		if streamed {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, sse)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, jsonBody)
	}))
	t.Cleanup(probe.srv.Close)
	return probe
}

// anthropicSSEAnswer is a complete Messages stream carrying one text block and
// one tool call, so the assembled Response can be checked field by field.
const anthropicSSEAnswer = "event: message_start\n" +
	`data: {"type":"message_start","message":{"usage":{"input_tokens":11}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"listing it"}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"list_dir"}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\".\"}"}}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

func TestAnthropicHonorsWireStreamTransport(t *testing.T) {
	probe := newAnthropicWireProbe(t, anthropicSSEAnswer, `{"content":[{"type":"text","text":"non-stream"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	c := newAnthropicCompleter("anthropic", probe.srv.URL, "key", nil, false)

	req := anthropicTestRequest([]Message{{Role: RoleUser, Content: "list the directory"}})
	req.StreamTransport = true // and no StreamWriter: the nested-turn shape

	resp, err := c.ChatTurn(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !probe.sawStream.Load() {
		t.Fatal("StreamTransport must put stream:true on the wire; the request went out non-stream")
	}
	// The non-stream contract on the return path: a fully assembled Response,
	// indistinguishable from what the plain endpoint would have produced.
	if resp.Content != "listing it" {
		t.Errorf("Content = %q, want %q", resp.Content, "listing it")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Function.Name != "list_dir" {
		t.Errorf("tool name = %q, want %q", resp.ToolCalls[0].Function.Name, "list_dir")
	}
	if resp.ToolCalls[0].Function.Arguments != `{"path":"."}` {
		t.Errorf("tool arguments = %q, want %q", resp.ToolCalls[0].Function.Arguments, `{"path":"."}`)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "tool_calls")
	}
	if !resp.TokenUsage.Reported || resp.TokenUsage.OutputTokens != 7 {
		t.Errorf("TokenUsage = %+v, want reported with 7 output tokens", resp.TokenUsage)
	}
}

// A caller that asked for neither live streaming nor the wire-stream
// transport still gets the plain endpoint.
func TestAnthropicWithoutStreamTransportStaysNonStream(t *testing.T) {
	probe := newAnthropicWireProbe(t, anthropicSSEAnswer, `{"content":[{"type":"text","text":"non-stream"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	c := newAnthropicCompleter("anthropic", probe.srv.URL, "key", nil, false)

	resp, err := c.ChatTurn(context.Background(), anthropicTestRequest([]Message{
		{Role: RoleUser, Content: "hello"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if probe.sawStream.Load() {
		t.Fatal("a plain turn must not stream on the wire")
	}
	if resp.Content != "non-stream" {
		t.Errorf("Content = %q, want %q", resp.Content, "non-stream")
	}
}

// Live streaming keeps its writer: StreamTransport must not steal a turn whose
// caller wants text as it arrives.
func TestAnthropicLiveStreamingStillWritesDeltas(t *testing.T) {
	probe := newAnthropicWireProbe(t, anthropicSSEAnswer, `{}`)
	c := newAnthropicCompleter("anthropic", probe.srv.URL, "key", nil, false)

	var sink stringSink
	req := anthropicTestRequest([]Message{{Role: RoleUser, Content: "hello"}})
	req.Stream = true
	req.StreamWriter = &sink
	req.StreamTransport = true

	if _, err := c.ChatTurn(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := sink.String(); got != "listing it" {
		t.Fatalf("streamed text = %q, want %q", got, "listing it")
	}
}

type stringSink struct{ b []byte }

func (s *stringSink) Write(p []byte) (int, error) {
	s.b = append(s.b, p...)
	return len(p), nil
}
func (s *stringSink) String() string { return string(s.b) }
