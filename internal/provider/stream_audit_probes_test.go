package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// partialWriter forwards every write to a channel and records the text. The
// test reads the channel to wait for the live partial to reach the writer, and
// reads text only after the call returned, so the parse goroutine's writes and
// the test's reads never race.
type partialWriter struct {
	ch   chan string
	text strings.Builder
}

func (w *partialWriter) Write(p []byte) (int, error) {
	s := string(p)
	w.text.WriteString(s)
	w.ch <- s
	return len(p), nil
}

// H2 probe. A no-index, no-ID continuation fragment lands on the highest
// existing tool-call slot, which with monotonic provider numbering is the most
// recently started call - the documented "no ID continues the most recent
// call" contract (deltaSlot). The probe locks that behaviour; it is a contract
// match, not a defect.
func TestStreamToolCallsInterleavedNoIndexContinuations(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"f","arguments":"{\"a\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","function":{"name":"g","arguments":"{\"b\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"2}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	}
	srv := sseServer(t, chunks, true)
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
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v, want 2", resp.ToolCalls)
	}
	byID := map[string]string{}
	for _, tc := range resp.ToolCalls {
		byID[tc.ID] = tc.Function.Arguments
	}
	if byID["call_a"] != `{"a":` {
		t.Fatalf("call_a arguments = %q, want %q", byID["call_a"], `{"a":`)
	}
	if byID["call_b"] != `{"b":2}` {
		t.Fatalf("call_b arguments = %q, want %q (no-ID continuation must land on the most recent call)", byID["call_b"], `{"b":2}`)
	}
}

// H3 probe. A negative tool-call index is stored on a map slot below zero and
// silently dropped by orderedToolCalls, which iterates from index 0 upward.
// Recorded as DC-14 malformed-input tolerance (deterministic drop, no crash);
// not a defect in this slice.
func TestStreamToolCallsNegativeIndexIsDropped(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":-1,"id":"call_neg","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
	}
	srv := sseServer(t, chunks, true)
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
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("negative-index tool call survived: %+v", resp.ToolCalls)
	}
}

// H3 probe. Two distinct calls sharing one index collapse onto the same
// accumulator: the second ID overwrites the first and their argument fragments
// concatenate. Recorded as DC-14 malformed-input tolerance (deterministic, no
// crash); not a defect in this slice.
func TestStreamToolCallsDuplicateIndexMergesDistinctCalls(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"f","arguments":"{\"a\":1}"}},{"index":0,"id":"call_b","function":{"name":"g","arguments":"{\"b\":2}"}}]},"finish_reason":"tool_calls"}]}`,
	}
	srv := sseServer(t, chunks, true)
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
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v, want 1 merged accumulator", resp.ToolCalls)
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_b" || tc.Function.Arguments != `{"a":1}{"b":2}` {
		t.Fatalf("merged tool call = %+v, want id=call_b args=`{\"a\":1}{\"b\":2}`", tc)
	}
}

// H4 probe. On the z.ai error parser, a 200 SSE chunk carrying code+message is
// an in-band error signal (z.ai reports failures this way inside a stream),
// not a legitimate clean-payload chunk, so surfacing the static code-only error
// is intended. A code-only chunk (no message, no error field) is ignored by
// the clean-payload guard. The surfaced text never forwards the provider's
// message (INV-SEC-3).
func TestZAIStream200CodeMessageChunk(t *testing.T) {
	t.Run("code plus message surfaces error", func(t *testing.T) {
		chunks := []string{`{"code":4001,"message":"provider failure"}`, `{"choices":[{"delta":{"content":"ok"}}]}`}
		srv, calls := countingSSEServer(t, chunks, true)
		defer srv.Close()

		c := NewOpenAICompatWithOptions(CompatOptions{Name: "zai", BaseURL: srv.URL, APIKey: "k", ErrorParser: zaiErrorParser})
		_, err := c.ChatStream(context.Background(), Request{
			Model:    "m",
			Messages: []Message{{Role: RoleUser, Content: "hi"}},
		}, io.Discard)
		if err == nil {
			t.Fatal("code+message chunk on a 200 stream was treated as success")
		}
		if !strings.Contains(err.Error(), "zai: provider error (HTTP 200, code 4001)") {
			t.Fatalf("err=%q, want static code-only zai error", err)
		}
		if strings.Contains(err.Error(), "provider failure") {
			t.Fatalf("err=%q must not forward the provider's message", err)
		}
		if got := atomic.LoadInt32(calls); got != 1 {
			t.Fatalf("made %d upstream requests, want 1 (in-band error is not replayed)", got)
		}
	})
	t.Run("code only is ignored", func(t *testing.T) {
		chunks := []string{`{"code":4001}`, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`}
		srv, calls := countingSSEServer(t, chunks, true)
		defer srv.Close()

		c := NewOpenAICompatWithOptions(CompatOptions{Name: "zai", BaseURL: srv.URL, APIKey: "k", ErrorParser: zaiErrorParser})
		content, err := c.ChatStream(context.Background(), Request{
			Model:    "m",
			Messages: []Message{{Role: RoleUser, Content: "hi"}},
		}, io.Discard)
		if err != nil {
			t.Fatalf("ChatStream: %v", err)
		}
		if content != "ok" {
			t.Fatalf("content = %q, want ok", content)
		}
		if got := atomic.LoadInt32(calls); got != 1 {
			t.Fatalf("made %d upstream requests, want 1", got)
		}
	})
}

// H5 probe (DC-12 consistency). When the request context is cancelled
// mid-stream after a flushed content delta, both parse paths keep the live
// writer's partial text; only the returned value differs (readStream preserves
// the partial string, readTurnStream returns a nil Response and discards it).
// The probe locks the message-loss-free invariant on both paths.
func TestChatTurnStreamCancelPartialContent(t *testing.T) {
	for _, path := range []struct {
		name string
		tool bool
	}{{"tools", true}, {"no tools", false}} {
		t.Run(path.name, func(t *testing.T) {
			runCancelPartialContent(t, path.tool)
		})
	}
}

// runCancelPartialContent exercises one stream path: it serves a flushed
// content delta, waits for it to reach the writer, then cancels the context
// and asserts the live partial was delivered exactly once on the writer.
func runCancelPartialContent(t *testing.T, tool bool) {
	type result struct {
		content string
		err     error
	}
	release := make(chan struct{})
	flushed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"hel"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		close(flushed)
		<-release
	}))
	defer srv.Close()
	defer closeOnce(release)

	c := streamingClient(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	w := &partialWriter{ch: make(chan string, 1)}
	done := make(chan result, 1)
	go func() {
		content, err := streamCancelPartialCall(tool, c, ctx, w)
		done <- result{content, err}
	}()

	// Wait for the flushed partial to reach the writer before cancelling:
	// the cancel must land mid-stream on a stream the client has already
	// received, so the no-message-loss assertion tests delivery, not
	// scheduler timing between the server flush and the client's read.
	<-flushed
	select {
	case got := <-w.ch:
		if got != "hel" {
			t.Fatalf("writer got %q, want live partial %q (no message loss)", got, "hel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("live partial never reached the writer (no message loss)")
	}
	cancel()
	var out result
	select {
	case out = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("call did not return after cancel")
	}
	if out.err == nil {
		t.Fatal("cancelled mid-stream turn succeeded")
	}
	// Nothing further may reach the writer after the cancel.
	select {
	case extra := <-w.ch:
		t.Fatalf("writer received %q after the cancel", extra)
	default:
	}
	if w.text.String() != "hel" {
		t.Fatalf("writer got %q, want live partial %q (no message loss)", w.text.String(), "hel")
	}
	if tool {
		if out.content != "" {
			t.Fatalf("tools path returned partial %q, want empty (nil Response on ctx.Done)", out.content)
		}
	} else if out.content != "hel" {
		t.Fatalf("no-tools path returned %q, want preserved partial %q", out.content, "hel")
	}
}

// streamCancelPartialCall invokes the audited stream path: ChatTurn streaming
// for the tools path, ChatStream for the no-tools path.
func streamCancelPartialCall(tool bool, c *OpenAICompat, ctx context.Context, w *partialWriter) (string, error) {
	if tool {
		resp, err := c.ChatTurn(ctx, Request{
			Model:        "m",
			Stream:       true,
			StreamWriter: w,
			Messages:     []Message{{Role: RoleUser, Content: "hi"}},
			Tools:        []ToolSpec{{"type": "function"}},
		})
		if resp != nil {
			return resp.Content, err
		}
		return "", err
	}
	return c.ChatStream(ctx, Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, w)
}

// H6 probe. readStream's Do and scanner-error branches lack asTransient
// wrapping while readTurnStream wraps both. The probe checks the observable
// classification, not the wrapping: IsTransient must agree across the two paths
// for the same fault. For a refused dial it does - a real ECONNREFUSED matches
// via errors.Is on both paths, and the text-only failingTransport shape matches
// on neither - so the suspected divergence is masked by IsTransient's own
// checks. The oversized-line parity is locked by
// TestChatStreamOversizedLineFailsStreamRead.
func TestChatStreamNoToolsConnectionRefusedTransientParity(t *testing.T) {
	refusedClient := func() *OpenAICompat {
		return &OpenAICompat{
			name:        "test",
			baseURL:     "http://127.0.0.1:1",
			apiKey:      "k",
			errorParser: openaiErrorParser,
			client:      &http.Client{Transport: &failingTransport{}},
		}
	}

	_, noToolsErr := refusedClient().ChatStream(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, io.Discard)
	_, toolsErr := refusedClient().ChatTurn(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Stream:   true,
		Tools:    []ToolSpec{{"type": "function"}},
	})
	if noToolsErr == nil || toolsErr == nil {
		t.Fatalf("refused dial succeeded: noToolsErr=%v toolsErr=%v", noToolsErr, toolsErr)
	}
	if IsTransient(noToolsErr) != IsTransient(toolsErr) {
		t.Fatalf("IsTransient parity broken: no-tools=%v tools=%v (no-tools err=%v, tools err=%v)",
			IsTransient(noToolsErr), IsTransient(toolsErr), noToolsErr, toolsErr)
	}
}
