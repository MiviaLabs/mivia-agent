package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
// reports nil at EOF, so it is otherwise indistinguishable from a complete one.
//
// Text-only streams without a finish signal are treated as usable: the content
// was fully received via streaming deltas. Only tool calls missing their
// minimum structure (ID and name) trigger the truncation error.
func TestChatTurnRejectsTruncatedToolCall(t *testing.T) {
	// Tool call with no ID → definitely truncated before completion.
	chunk := `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"run_command","arguments":"{}"}}]}}]}`
	srv := sseServer(t, []string{chunk}, false) // no [DONE]
	defer srv.Close()

	c := streamingClient(t, srv)
	resp, err := c.ChatTurn(context.Background(), Request{
		Model:    "m",
		Stream:   true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatalf("truncated tool call reported as success: content=%q tools=%+v finish=%q",
			resp.Content, resp.ToolCalls, resp.FinishReason)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "truncat") &&
		!strings.Contains(strings.ToLower(err.Error()), "incomplete") {
		t.Fatalf("error should name the truncation, got: %v", err)
	}
}

// A stream with tool calls that have both ID and name but no finish signal
// must still carry valid JSON in function.arguments. Truncated argument JSON
// is not a usable tool call, so it must be rejected.
func TestChatTurnRejectsTruncatedStreamWithInvalidArguments(t *testing.T) {
	chunk := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"run_command","arguments":"{\"argv\":[\"rm\",\"-rf\",\"/tm"}}]}}]}`
	srv := sseServer(t, []string{chunk}, false) // no [DONE]
	defer srv.Close()

	c := streamingClient(t, srv)
	_, err := c.ChatTurn(context.Background(), Request{
		Model:    "m",
		Stream:   true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatalf("truncated stream with malformed tool-call arguments reported as success")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "malformed") &&
		!strings.Contains(strings.ToLower(err.Error()), "invalid") &&
		!strings.Contains(strings.ToLower(err.Error()), "truncat") {
		t.Fatalf("error should name malformed/invalid/truncated arguments, got: %v", err)
	}
	// A stream cut mid-tool-call never delivered a usable answer, so the step
	// retry loop must be allowed to re-run the turn: the truncation must
	// surface as transient even when the arguments are malformed JSON. The
	// missing-ID sibling branch already wraps in TransientError; before this
	// fix the bare fmt.Errorf here matched no transient phrase, so the step
	// failed terminal instead of retrying.
	if !IsTransient(err) {
		t.Fatalf("truncated stream with malformed tool-call arguments must be transient, got: %v", err)
	}
}

// A stream with tool calls that have both ID and name, valid argument JSON,
// but no finish signal is treated as complete (the minimum viable structure
// is present).
func TestChatTurnAcceptsTruncatedStreamWithValidArguments(t *testing.T) {
	chunk := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"run_command","arguments":"{\"argv\":[\"ls\"]}"}}]}}]}`
	srv := sseServer(t, []string{chunk}, false) // no [DONE]
	defer srv.Close()

	c := streamingClient(t, srv)
	resp, err := c.ChatTurn(context.Background(), Request{
		Model:    "m",
		Stream:   true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("tool call with ID+name and valid JSON args should be treated as complete: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool call lost: %+v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].Function.Arguments != `{"argv":["ls"]}` {
		t.Fatalf("arguments=%q, want %q", resp.ToolCalls[0].Function.Arguments, `{"argv":["ls"]}`)
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

func TestChatTurnPanelMalformedBodyDoesNotReplay(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":`))
	}))
	defer srv.Close()

	c := streamingClient(t, srv)
	_, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}, DisableProviderReplay: true,
	})
	if err == nil {
		t.Fatal("malformed panel response succeeded")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
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

func TestChatTurnSilentPanelStreamDoesNotReplay(t *testing.T) {
	srv, calls := countingSSEServer(t, nil, false)
	defer srv.Close()

	c := streamingClient(t, srv)
	_, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Stream: true, DisableProviderReplay: true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("silent panel stream succeeded")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("silent panel stream made %d requests, want 1", got)
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

// TestChatStreamNoContentNoFinishReasonStillFallsBack proves R0-1: a chunk
// with an empty delta object (no content, no finish_reason, no usage) must not
// set received=true. Without this fix, the empty delta silently suppresses the
// non-streaming fallback and the caller gets an empty answer.
func TestChatStreamNoContentNoFinishReasonStillFallsBack(t *testing.T) {
	srv, calls := countingSSEServer(t, []string{`{"choices":[{"delta":{}}]}`}, false)
	defer srv.Close()

	c := streamingClient(t, srv)
	out, err := c.ChatStream(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if out != "fallback" {
		t.Fatalf("ChatStream returned %q, want %q (fallback was suppressed)", out, "fallback")
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("made %d upstream requests, want 2 (stream + fallback)", got)
	}
}

// TestChatStreamExplicitEmptyFinishReasonStillFallsBack proves R0-2: an explicit
// finish_reason:"" decodes to the Go empty string and fails != "", so it is NOT
// a completion signal — matching readTurnStream's behaviour exactly.
func TestChatStreamExplicitEmptyFinishReasonStillFallsBack(t *testing.T) {
	srv, calls := countingSSEServer(t, []string{`{"choices":[{"delta":{},"finish_reason":""}]}`}, false)
	defer srv.Close()

	c := streamingClient(t, srv)
	out, err := c.ChatStream(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if out != "fallback" {
		t.Fatalf("ChatStream returned %q, want %q (fallback was suppressed)", out, "fallback")
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("made %d upstream requests, want 2 (stream + fallback)", got)
	}
}

// TestChatStreamEmptyReasoningDetailsEntryStillFallsBack closes the R0-1 gap
// on the no-tools path (PC-1). readStream's received gate (deltaCountsAsReceived)
// counted ANY non-empty reasoning_details array as received, even an entry with
// neither text nor summary. readTurnStream folds only text/summary entries into
// the reasoning builder that gates its payload, so the same wire shape falls
// back to a non-streamed re-ask on the tools path (2 calls) while the plain
// path returned a silent empty reply after 1 call. An entry with no payload
// must not suppress the fallback on either path. RED on the current code
// (1 call, empty output); GREEN after the fix (fallback, 2 calls).
func TestChatStreamEmptyReasoningDetailsEntryStillFallsBack(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text"}]}}]}`,
	}
	srv, calls := countingSSEServer(t, chunks, true)
	defer srv.Close()

	c := streamingClient(t, srv)
	out, err := c.ChatStream(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("empty reasoning_details entry made %d upstream requests, want 2 (stream + fallback)", got)
	}
	if out != "fallback" {
		t.Fatalf("ChatStream returned %q, want %q (fallback was suppressed)", out, "fallback")
	}
}

// TestChatStreamReasoningDetailsWithTextCountsAsReceived locks the positive
// boundary on the no-tools path: a reasoning_details entry carrying text or
// summary IS a delivered shape and must count as received (no fallback, 1
// call), matching readTurnStream's reasoning.Len()>0 payload term and
// resolveReasoningContent's text/summary-only concatenation.
func TestChatStreamReasoningDetailsWithTextCountsAsReceived(t *testing.T) {
	tests := []struct {
		name  string
		chunk string
	}{
		{"text entry", `{"choices":[{"delta":{"reasoning_details":[{"type":"thinking","text":"think"}]}}]}`},
		{"summary entry", `{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.summary","summary":"sum"}]}}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, calls := countingSSEServer(t, []string{tt.chunk}, true)
			defer srv.Close()

			c := streamingClient(t, srv)
			out, err := c.ChatStream(context.Background(), Request{
				Model:    "m",
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			}, nil)
			if err != nil {
				t.Fatalf("ChatStream: %v", err)
			}
			if got := atomic.LoadInt32(calls); got != 1 {
				t.Fatalf("payload-bearing reasoning_details stream made %d upstream requests, want 1 (no fallback)", got)
			}
			// The no-tools path returns only accumulated content; reasoning
			// stays internal to the received gate.
			if out != "" {
				t.Fatalf("ChatStream returned %q, want empty", out)
			}
		})
	}
}

// TestChatStreamTopLevelWebSearchCountsAsReceived (WS-1) locks the no-tools
// path's received gate. readStream cannot surface web_search entries (it
// returns content only), but a chunk carrying TOP-LEVEL web_search is a
// delivered shape and must count as received, mirroring readTurnStream's
// len(webSearch)>0 payload gate. Without the fix a web_search-only turn looks
// like an empty stream, retryWithoutStreaming re-asks it non-streamed, and the
// same turn is billed twice. Two shapes are covered: a sole-signal
// web_search-only empty-choices chunk, and a choices-bearing chunk whose delta
// is empty. RED on the unfixed code (2-call re-bill), GREEN after the fix
// (1 call, empty content returned as-is).
func TestChatStreamTopLevelWebSearchCountsAsReceived(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
	}{
		{
			name: "sole-signal web_search-only empty-choices chunk",
			chunks: []string{
				`{"choices":[],"web_search":[{"title":"only"}]}`,
			},
		},
		{
			name: "choices-bearing chunk with empty delta and top-level web_search",
			chunks: []string{
				`{"choices":[{"delta":{}}],"web_search":[{"title":"only"}]}`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, calls := countingSSEServer(t, tt.chunks, false)
			defer srv.Close()

			c := streamingClient(t, srv)
			out, err := c.ChatStream(context.Background(), Request{
				Model:    "m",
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			}, nil)
			if err != nil {
				t.Fatalf("ChatStream: %v", err)
			}
			if got := atomic.LoadInt32(calls); got != 1 {
				t.Fatalf("made %d upstream requests, want 1 (web_search-only turn must not be re-asked)", got)
			}
			if out != "" {
				t.Fatalf("ChatStream returned %q, want empty (only web_search was delivered)", out)
			}
		})
	}
}

// readTurnReceivedShared mirrors readTurnStream's received flag on the
// dimensions both stream paths share: content, the reasoning builder
// (reasoning_content, reasoning, and payload-bearing reasoning_details),
// top-level body.WebSearch, finish_reason, and usage. tool_calls and
// delta-level web_search are excluded: readTurnStream tracks those in its
// payload while the no-tools readStream cannot receive them, so disagreement
// on those dimensions is an expected scope difference, not a bug.
func reasoningDeltaCarriesPayload(reasoningContent, reasoning string, details []reasoningDetailWire) bool {
	if reasoningContent != "" || reasoning != "" {
		return true
	}
	for _, d := range details {
		if d.Text != "" || d.Summary != "" {
			return true
		}
	}
	return false
}

func readTurnReceivedShared(body chatResponseBody) bool {
	finishReason := ""
	contentLen, reasoningLen, webSearchLen := 0, 0, 0
	if len(body.Choices) > 0 {
		ch := body.Choices[0]
		finishReason = ch.FinishReason
		if ch.Delta.Content != "" {
			contentLen = 1 // content.Len() > 0 in readTurnStream
		}
		if reasoningDeltaCarriesPayload(ch.Delta.ReasoningContent, ch.Delta.Reasoning, ch.Delta.ReasoningDetails) {
			reasoningLen = 1 // reasoning.Len() > 0 in readTurnStream
		}
		if len(ch.Delta.WebSearch) > 0 {
			webSearchLen = 1 // delta-level append into the same accumulator
		}
	}
	if len(body.WebSearch) > 0 {
		webSearchLen = 1 // top-level append
	}
	// payload := content || reasoning || webSearch (tools excluded above)
	return contentLen > 0 || reasoningLen > 0 || webSearchLen > 0 ||
		finishReason != "" || body.Usage != nil
}

// readStreamReceivedShared mirrors readStream's received flag on the shared
// dimensions. It models readStream's gate by calling the production predicate
// deltaCountsAsReceived (plus the top-level body.WebSearch term), so a
// divergence between the two paths' received rules is reported here instead
// of hidden by a hand-rewritten copy: reasoning and reasoning_details are
// received dimensions on both paths, and an empty reasoning_details entry
// must not count on either (R0-1). An empty-choices chunk still counts as
// received when it carries usage or top-level web_search, matching readStream.
func readStreamReceivedShared(body chatResponseBody) bool {
	if len(body.Choices) == 0 {
		return body.Usage != nil || len(body.WebSearch) > 0
	}
	ch := body.Choices[0]
	return deltaCountsAsReceived(ch.Delta.Content, ch.Delta.ReasoningContent, ch.Delta.Reasoning, ch.FinishReason, ch.Delta.ReasoningDetails, body.Usage) ||
		len(body.WebSearch) > 0
}

// FuzzReadStreamReceived compares the received-flag behaviour of readStream
// against readTurnStream for identical single-chunk JSON inputs on their
// shared dimensions (see readTurnReceivedShared and readStreamReceivedShared).
// The reasoning shape is shared in full: readStream counts reasoning_content,
// reasoning, and a reasoning_details entry carrying text or summary exactly
// like readTurnStream's reasoning.Len()>0 payload term, and an entry with
// neither counts on NO path (R0-1). TOP-LEVEL body.WebSearch is shared too:
// both paths count it as a received signal, so the motivating sole-signal
// shape {"choices":[{"delta":{}}],"web_search":[{"title":"x"}]} must agree on
// received=true. Chunks whose delta carries tool_calls or delta-level
// web_search are skipped, since readStream cannot receive those dimensions.
func FuzzReadStreamReceived(f *testing.F) {
	seeds := []string{
		`{"choices":[{"delta":{}}]}`,
		`{"choices":[{"delta":{"content":""}}]}`,
		`{"choices":[{"delta":{"content":"hello"}}]}`,
		`{"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
		`{"choices":[{"delta":{},"finish_reason":""}]}`,
		`{"choices":[],"usage":{"prompt_tokens":1}}`,
		`{"choices":[],"web_search":[{"title":"x"}]}`,
		`{"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1}}`,
		`{"choices":[{"delta":{"reasoning_content":"think"}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"think"},"finish_reason":"stop"}]}`,
		`{"choices":[{"delta":{"reasoning":"think"}}]}`,
		`{"choices":[{"delta":{"reasoning_details":[{"type":"thinking","text":"think"}]}}]}`,
		`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.summary","summary":"s"}]}}]}`,
		`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text"}]}}]}`,
		`{"choices":[{"delta":{}}],"web_search":[{"title":"x"}]}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		// Must be valid JSON.
		var body chatResponseBody
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Skip()
		}
		// Empty stream (bare [DONE]) — no chunk to evaluate. An empty-choices
		// chunk carrying usage or top-level web_search IS a chunk: both paths
		// count those as received, so it must be evaluated, not skipped.
		if len(body.Choices) == 0 && body.Usage == nil && len(body.WebSearch) == 0 {
			t.Skip()
		}
		// Skip chunks whose delta carries dimensions readTurnStream tracks but
		// readStream does not (tool_calls, delta-level web_search). TOP-LEVEL
		// body.WebSearch is NOT skipped: both paths count it as received.
		hasToolCalls := len(body.Choices) > 0 && len(body.Choices[0].Delta.ToolCalls) > 0
		hasDeltaWebSearch := len(body.Choices) > 0 && len(body.Choices[0].Delta.WebSearch) > 0
		if hasToolCalls || hasDeltaWebSearch {
			t.Skip()
		}
		if readStreamReceivedShared(body) != readTurnReceivedShared(body) {
			t.Errorf("received mismatch for %q: readStream=%v readTurnStream=%v",
				string(raw), readStreamReceivedShared(body), readTurnReceivedShared(body))
		}
	})
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

// deadlineTransport returns the exact error shape http.Client produces when
// its own Timeout backstop fires: a *url.Error wrapping context.DeadlineExceeded.
// The failing RoundTripper never dials, so no server or insecure-HTTP env is
// needed to exercise chatTurnStream's client.Do error branch.
type deadlineTransport struct{}

func (deadlineTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, &url.Error{Op: "Post", URL: "https://example.invalid/chat/completions", Err: context.DeadlineExceeded}
}

// TestChatTurnStreamDoDeadlineIsTransient locks chatTurnStream's client.Do
// error branch to the same deadline marking as doJSONOnce and ChatStream.
// When the transport backstop (http.Client.Timeout, 15min default) or a parent
// deadline fires before the armed per-call req.Timeout (hours), client.Do
// returns *url.Error wrapping context.DeadlineExceeded. A bare deadline is not
// transient, so without the mark the step fails terminal and loses finished
// work (DC-8) instead of engaging runStepWithTransientRetry.
func TestChatTurnStreamDoDeadlineIsTransient(t *testing.T) {
	c := &OpenAICompat{
		name:        "test",
		baseURL:     "https://example.invalid",
		apiKey:      "k",
		errorParser: openaiErrorParser,
		client:      &http.Client{Transport: deadlineTransport{}},
	}

	// Positive: the armed req.Timeout is still in the future when the
	// transport backstop cut the call, so the cut is transient and the step
	// retry loop may re-run the call under a fresh context.
	_, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Stream: true, Timeout: 12 * time.Hour,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	var transient *TransientError
	if !errors.As(err, &transient) {
		t.Fatalf("deadline cut before the armed req.Timeout must be transient, got: %v", err)
	}

	// Negative (a): no armed per-call deadline (Timeout==0). A parent/step
	// deadline is the caller's decision, not a cut call, so the url.Error
	// deadline must stay permanent: retrying under the expired context fails
	// at once, every time.
	_, err = c.ChatTurn(context.Background(), Request{
		Model: "m", Stream: true, Timeout: 0,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if errors.As(err, &transient) {
		t.Fatalf("bare deadline with no armed req.Timeout must stay permanent, got transient: %v", err)
	}

	// Negative (b): the armed deadline genuinely fired - the parent context
	// is already expired, so the provider had its full budget and answered
	// nothing. That is a permanent statement about the call, not a cut answer.
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err = c.ChatTurn(expired, Request{
		Model: "m", Stream: true, Timeout: time.Hour,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if errors.As(err, &transient) {
		t.Fatalf("genuinely fired armed deadline must stay permanent, got transient: %v", err)
	}
}
