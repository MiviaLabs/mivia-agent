package provider

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Structured untrusted SSE input on the no-tools ChatStream path (readStream).
// Each case must be handled deterministically: an empty or malformed chunk is
// skipped without a crash, and a stream that carries no completion signal
// still falls back to the non-streaming request instead of returning a silent
// empty success. Repeated content chunks append in order with no fallback.
func TestChatStreamStructuredInputNoToolsPath(t *testing.T) {
	cases := []struct {
		name    string
		chunks  []string
		content string // expected returned content
		calls   int32  // expected upstream request count
	}{
		{
			name:    "bare data line",
			chunks:  []string{""}, // "data: \n\n"
			content: "fallback",
			calls:   2,
		},
		{
			name:    "empty delta chunk",
			chunks:  []string{`{"choices":[{"delta":{}}]}`},
			content: "fallback",
			calls:   2,
		},
		{
			name:    "empty choices chunk",
			chunks:  []string{`{"choices":[]}`},
			content: "fallback",
			calls:   2,
		},
		{
			name:    "empty tool calls array",
			chunks:  []string{`{"choices":[{"delta":{"tool_calls":[]}}]}`},
			content: "fallback",
			calls:   2,
		},
		{
			name:    "invalid json chunk",
			chunks:  []string{`not json`},
			content: "fallback",
			calls:   2,
		},
		{
			name:    "truncated json chunk",
			chunks:  []string{`{"choices":[{"delta":{"content":"hel`},
			content: "fallback",
			calls:   2,
		},
		{
			name:    "repeated content chunks",
			chunks:  []string{`{"choices":[{"delta":{"content":"ab"}}]}`, `{"choices":[{"delta":{"content":"ab"}}]}`},
			content: "abab",
			calls:   1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := countingSSEServer(t, tc.chunks, true)
			defer srv.Close()

			c := streamingClient(t, srv)
			content, err := c.ChatStream(context.Background(), Request{
				Model:    "m",
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			}, io.Discard)
			if err != nil {
				t.Fatalf("ChatStream: %v", err)
			}
			if got := atomic.LoadInt32(calls); got != tc.calls {
				t.Fatalf("made %d upstream requests, want %d", got, tc.calls)
			}
			if content != tc.content {
				t.Fatalf("content = %q, want %q", content, tc.content)
			}
		})
	}
}

// chatTurnStreamToolCase is one ChatTurn tool-path stream case: the SSE
// chunks to serve, the expected upstream request count, and the expected
// content / tool-call arguments in the response.
type chatTurnStreamToolCase struct {
	name      string
	chunks    []string
	content   string
	calls     int32
	toolCalls int
	wantArgs  string
}

// runChatTurnStreamToolCase drives one tool-path stream case against a
// counting SSE server and asserts the request count, content, and tool-call
// arguments. Shared by the structured-input tests so each function stays
// under the structure gate's length limit.
func runChatTurnStreamToolCase(t *testing.T, tc chatTurnStreamToolCase) {
	t.Helper()
	srv, calls := countingSSEServer(t, tc.chunks, true)
	defer srv.Close()

	c := streamingClient(t, srv)
	resp, err := c.ChatTurn(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Stream:   true,
		Tools:    []ToolSpec{{"type": "function", "function": map[string]any{"name": "f"}}},
	})
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != tc.calls {
		t.Fatalf("made %d upstream requests, want %d", got, tc.calls)
	}
	if resp.Content != tc.content {
		t.Fatalf("content = %q, want %q", resp.Content, tc.content)
	}
	if len(resp.ToolCalls) != tc.toolCalls {
		t.Fatalf("tool calls = %d, want %d: %+v", len(resp.ToolCalls), tc.toolCalls, resp.ToolCalls)
	}
	if tc.toolCalls > 0 && tc.wantArgs != "" && resp.ToolCalls[0].Function.Arguments != tc.wantArgs {
		t.Fatalf("arguments = %q, want %q", resp.ToolCalls[0].Function.Arguments, tc.wantArgs)
	}
}

// Structured untrusted SSE input on the tool-capable ChatTurn stream path
// (readTurnStream). Empty and malformed chunks are skipped deterministically;
// duplicate tool-call fragments accumulate on their index slot without a
// crash; repeated content chunks append in order. The scan edge cases (bare
// data line, empty delta, empty choices) live in
// TestChatTurnStreamStructuredInputEdgeCases.
func TestChatTurnStreamStructuredInputToolsPath(t *testing.T) {
	duplicateFragments := []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"f","arguments":"{\"a\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"arguments":"1}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	}
	repeatedContent := []string{
		`{"choices":[{"delta":{"content":"ab"}}]}`,
		`{"choices":[{"delta":{"content":"ab"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	}
	cases := []chatTurnStreamToolCase{
		{
			name:    "empty tool calls array",
			chunks:  []string{`{"choices":[{"delta":{"tool_calls":[]}}]}`},
			content: "fallback",
			calls:   2,
		},
		{
			name:    "invalid json chunk",
			chunks:  []string{`not json`},
			content: "fallback",
			calls:   2,
		},
		{
			name:      "duplicate same index same id fragments",
			chunks:    duplicateFragments,
			toolCalls: 1,
			wantArgs:  `{"a":1}`,
			calls:     1,
		},
		{
			name:    "repeated content chunks",
			chunks:  repeatedContent,
			content: "abab",
			calls:   1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runChatTurnStreamToolCase(t, tc) })
	}
}

// The scan edge cases on the tool path: a bare data line, an empty delta, and
// an empty choices array must each be skipped without a crash and fall back
// to the non-streamed retry (calls == 2).
func TestChatTurnStreamStructuredInputEdgeCases(t *testing.T) {
	cases := []chatTurnStreamToolCase{
		{
			name:    "bare data line",
			chunks:  []string{""}, // "data: \n\n"
			content: "fallback",
			calls:   2,
		},
		{
			name:    "empty delta chunk",
			chunks:  []string{`{"choices":[{"delta":{}}]}`},
			content: "fallback",
			calls:   2,
		},
		{
			name:    "empty choices chunk",
			chunks:  []string{`{"choices":[]}`},
			content: "fallback",
			calls:   2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runChatTurnStreamToolCase(t, tc) })
	}
}

// A single SSE line past the 1 MiB scanner cap (bufio.ErrTooLong) must fail
// the stream read on both parse paths with an error that names the stream
// read, and must classify identically under IsTransient: neither path treats
// an oversized line as a transient fault (readTurnStream's asTransient is a
// no-op here because IsTransient(bufio.ErrTooLong) is false, so both paths
// surface the same non-transient error). This locks the scanner-cap edge that
// no test covered on either path.
func TestChatStreamOversizedLineFailsStreamRead(t *testing.T) {
	longLine := "data: " + strings.Repeat("x", 1024*1024) + "\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, longLine)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	assertOversized := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("oversized SSE line succeeded")
		}
		if !errors.Is(err, bufio.ErrTooLong) {
			t.Fatalf("errors.Is(err, bufio.ErrTooLong) = false; err=%v", err)
		}
		if !strings.Contains(err.Error(), "stream read") {
			t.Fatalf("err=%q should name the stream read", err)
		}
		if IsTransient(err) {
			t.Fatalf("oversized line must not be transient on either path; err=%v", err)
		}
	}

	t.Run("no tools", func(t *testing.T) {
		c := streamingClient(t, srv)
		_, err := c.ChatStream(context.Background(), Request{
			Model:    "m",
			Messages: []Message{{Role: RoleUser, Content: "hi"}},
		}, io.Discard)
		assertOversized(t, err)
	})
	t.Run("tools", func(t *testing.T) {
		c := streamingClient(t, srv)
		_, err := c.ChatTurn(context.Background(), Request{
			Model:    "m",
			Messages: []Message{{Role: RoleUser, Content: "hi"}},
			Stream:   true,
			Tools:    []ToolSpec{{"type": "function", "function": map[string]any{"name": "f"}}},
		})
		assertOversized(t, err)
	})
}
