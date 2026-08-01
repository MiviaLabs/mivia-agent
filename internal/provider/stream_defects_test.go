package provider

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func sseServer(t *testing.T, chunks []string, sendDone bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			_, _ = w.Write([]byte("data: " + c + "\n\n"))
		}
		if sendDone {
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
}

func streamingClient(t *testing.T, srv *httptest.Server) *OpenAICompat {
	t.Helper()
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	return NewOpenAICompat("test", srv.URL, "k", "", "")
}

// A stream that ends without [DONE] and without a finish_reason is a truncated
// response (proxy cut, HTTP/2 END_STREAM, connection close). bufio.Scanner
// reports nil at EOF, so it is otherwise indistinguishable from a complete one
// - and the caller then executes a tool whose argument JSON is cut in half, or
// presents half an answer as final and persists it.
func TestChatTurnRejectsTruncatedStream(t *testing.T) {
	chunk := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"run_command","arguments":"{\"argv\":[\"rm\",\"-rf\",\"/tm"}}]}}]}`
	srv := sseServer(t, []string{chunk}, false) // no [DONE]
	defer srv.Close()

	c := streamingClient(t, srv)
	resp, err := c.ChatTurn(context.Background(), Request{
		Model:    "m",
		Stream:   true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatalf("truncated stream reported as success: content=%q tools=%+v finish=%q",
			resp.Content, resp.ToolCalls, resp.FinishReason)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "truncat") &&
		!strings.Contains(strings.ToLower(err.Error()), "incomplete") {
		t.Fatalf("error should name the truncation, got: %v", err)
	}
}

// A complete stream that ends with finish_reason but no [DONE] is fine.
func TestChatTurnAcceptsFinishReasonWithoutDone(t *testing.T) {
	chunk := `{"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}`
	srv := sseServer(t, []string{chunk}, false)
	defer srv.Close()

	c := streamingClient(t, srv)
	resp, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Stream: true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("finish_reason is a valid completion signal: %v", err)
	}
	if resp.Content != "hello" {
		t.Fatalf("content=%q", resp.Content)
	}
}

// Some upstreams omit "index" on tool_call fragments. It decodes to 0, so every
// distinct call collapses onto the same map key: earlier calls are erased and
// their arguments concatenated into garbage. The non-streaming path returns
// both calls correctly, so behaviour silently depends on whether streaming is on.
func TestStreamToolCallsWithoutIndexStaySeparate(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"tool_calls":[{"id":"call_a","function":{"name":"f","arguments":"{\"a\":1}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"id":"call_b","function":{"name":"g","arguments":"{\"b\":2}"}}]},"finish_reason":"tool_calls"}]}`,
	}
	srv := sseServer(t, chunks, true)
	defer srv.Close()

	c := streamingClient(t, srv)
	resp, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Stream: true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("expected 2 distinct tool calls, got %d: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	byID := map[string]string{}
	for _, tc := range resp.ToolCalls {
		byID[tc.ID] = tc.Function.Arguments
	}
	if byID["call_a"] != `{"a":1}` || byID["call_b"] != `{"b":2}` {
		t.Fatalf("arguments corrupted by index collision: %+v", byID)
	}
}

// countingSSEServer replays chunks and reports how many upstream requests it saw.
func countingSSEServer(t *testing.T, chunks []string, sendDone bool) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		body, _ := io.ReadAll(r.Body)
		// The non-streaming fallback re-posts with stream:false; answer it in
		// plain JSON so a fallback shows up as a call count, not a decode error.
		if !bytes.Contains(body, []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"fallback"},"finish_reason":"stop"}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			_, _ = w.Write([]byte("data: " + c + "\n\n"))
		}
		if sendDone {
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	return srv, &calls
}

// A model may legitimately answer with nothing (a refusal-free empty stop, a
// turn whose whole output was filtered). Counting that as "nothing arrived"
// re-sends the entire prompt non-streamed: the user is billed twice for one
// turn, and the retry carries a fresh Idempotency-Key so no upstream can dedupe.
func TestChatTurnEmptyCompletionDoesNotResend(t *testing.T) {
	chunk := `{"choices":[{"delta":{"content":""},"finish_reason":"stop"}]}`
	srv, calls := countingSSEServer(t, []string{chunk}, true)
	defer srv.Close()

	c := streamingClient(t, srv)
	resp, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Stream: true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("empty completion caused %d upstream requests, want 1", got)
	}
	if resp.Content != "" {
		t.Fatalf("content=%q, want empty", resp.Content)
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("finish_reason=%q, want stop", resp.FinishReason)
	}
}

// A stream that carries no chunk at all is not a completion signal, so the
// non-streaming fallback must still run.
func TestChatTurnSilentStreamStillFallsBack(t *testing.T) {
	srv, calls := countingSSEServer(t, nil, false)
	defer srv.Close()

	c := streamingClient(t, srv)
	if _, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Stream: true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("silent stream made %d requests, want 2 (stream + fallback)", got)
	}
}

// TestNoMessageLossFallbackWritesToStreamWriter locks the defect where the
// non-streaming fallback dropped req.StreamWriter. The caller's `stream` flag
// stays true, so the agent loop skips its own rewrite and the TUI - which takes
// the final answer only from the writer - showed a completed turn with no
// answer while the text was still persisted to the session transcript.
func TestNoMessageLossFallbackWritesToStreamWriter(t *testing.T) {
	srv, calls := countingSSEServer(t, nil, false)
	defer srv.Close()

	var sink bytes.Buffer
	c := streamingClient(t, srv)
	resp, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Stream: true, StreamWriter: &sink,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("silent stream made %d requests, want 2 (stream + fallback)", got)
	}
	if resp.Content != "fallback" {
		t.Fatalf("resp.Content=%q, want %q", resp.Content, "fallback")
	}
	// Exactly once: the stream produced nothing, so nothing was live-written.
	if sink.String() != "fallback" {
		t.Fatalf("fallback content reached the writer as %q, want %q", sink.String(), "fallback")
	}
}

// --- Stream commitment boundary ---

// rateLimitedThenSSEServer refuses the first request with an HTTP 429 - a
// status that arrives before any stream is committed - and serves a complete
// SSE stream to every request after it.
func rateLimitedThenSSEServer(t *testing.T, chunks []string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			// Retry-After must be set before WriteHeader to reach the wire.
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			_, _ = w.Write([]byte("data: " + c + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	return srv, &calls
}

// A 429 arrives before a single byte of stream is committed, so the shared
// transport can replay the request: nothing has been shown to the user and
// nothing has been billed. This is the direct SSE path - ChatStream with no
// tools - which bypasses ChatTurn entirely.
func TestChatStreamRetriesPreCommitmentRateLimit(t *testing.T) {
	srv, calls := rateLimitedThenSSEServer(t, []string{
		`{"choices":[{"delta":{"content":"streamed"},"finish_reason":"stop"}]}`,
	})
	defer srv.Close()

	var sink bytes.Buffer
	c := streamingClient(t, srv)
	out, err := c.ChatStream(context.Background(), Request{
		Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, &sink)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if out != "streamed" || sink.String() != "streamed" {
		t.Fatalf("out=%q writer=%q", out, sink.String())
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("made %d requests, want 2 (429 + retry)", got)
	}
}

// The same pre-commitment 429, on the tool-capable path: ChatStream with tools
// delegates to ChatTurn/chatTurnStream, which shares the one transport. Neither
// path may grow a retry loop of its own.
func TestChatTurnStreamRetriesPreCommitmentRateLimit(t *testing.T) {
	srv, calls := rateLimitedThenSSEServer(t, []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
	})
	defer srv.Close()

	c := streamingClient(t, srv)
	resp, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Stream: true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools:    []ToolSpec{{"type": "function"}},
	})
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool calls lost across the retry: %+v", resp.ToolCalls)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("made %d requests, want 2 (429 + retry)", got)
	}
}

// inBandErrorServer answers with HTTP 200 and reports the failure inside the
// stream, which is how OpenRouter surfaces a mid-stream provider error. The
// status line is already committed, so the transport cannot replay it.
func inBandErrorServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	return countingSSEServer(t, []string{`{"error":{"message":"upstream exploded"}}`}, false)
}

// Past commitment the request is spent: the upstream accepted it, may have
// billed it, and the reply is already partly on the wire. Replaying would ask
// the same question twice, so the in-band error is surfaced once - no transport
// retry, and no empty-stream fallback either.
func TestChatStreamInBandErrorIsNotReplayed(t *testing.T) {
	srv, calls := inBandErrorServer(t)
	defer srv.Close()

	c := streamingClient(t, srv)
	if _, err := c.ChatStream(context.Background(), Request{
		Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil); err == nil {
		t.Fatal("an in-band stream error was reported as success")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("made %d requests, want 1 (committed streams are not replayed)", got)
	}
}

// The same boundary on the tool-capable path. A fallback here would show up as
// a second request answering in plain JSON.
func TestChatTurnStreamInBandErrorIsNotReplayed(t *testing.T) {
	srv, calls := inBandErrorServer(t)
	defer srv.Close()

	c := streamingClient(t, srv)
	if _, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Stream: true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools:    []ToolSpec{{"type": "function"}},
	}); err == nil {
		t.Fatal("an in-band stream error was reported as success")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("made %d requests, want 1 (committed streams are not replayed)", got)
	}
}

// TestNoMessageLossFallbackToleratesNilWriter guards the same path when no
// writer is attached (every non-TUI caller). Writing to a nil io.Writer panics.
func TestNoMessageLossFallbackToleratesNilWriter(t *testing.T) {
	srv, _ := countingSSEServer(t, nil, false)
	defer srv.Close()

	c := streamingClient(t, srv)
	resp, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Stream: true, StreamWriter: nil,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if resp.Content != "fallback" {
		t.Fatalf("resp.Content=%q, want %q", resp.Content, "fallback")
	}
}
