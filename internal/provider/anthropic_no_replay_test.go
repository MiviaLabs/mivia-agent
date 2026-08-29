package provider

// DisableProviderReplay means exactly one provider request. The
// OpenAI-compatible client enforces it by stamping the request context, which
// the retry round tripper and the redirect guard both read. The native
// Anthropic client never stamped anything, so the flag reached the wire as a
// suggestion: a single 503 replayed the same generation up to five times, and
// each replay is a separate billable completion.
//
// Panel actors set DisableProviderReplay unconditionally, so this is the
// default path for that whole surface whenever the model is Anthropic-native.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// countingFailures answers every request with a retryable 503 and counts how
// many arrived.
func countingFailures(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error"}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestAnthropicDisableProviderReplayIssuesOneRequest(t *testing.T) {
	srv, calls := countingFailures(t)
	c := newAnthropicCompleter("anthropic", srv.URL, "key", nil, false)

	req := anthropicTestRequest([]Message{{Role: RoleUser, Content: "hello"}})
	req.DisableProviderReplay = true

	if _, err := c.ChatTurn(context.Background(), req); err == nil {
		t.Fatal("expected the 503 to surface")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("provider saw %d requests, want exactly 1: DisableProviderReplay must reach the transport", got)
	}
}

// The same contract on the streaming path, which builds its request through
// the same helper.
func TestAnthropicDisableProviderReplayOnStreamPath(t *testing.T) {
	srv, calls := countingFailures(t)
	c := newAnthropicCompleter("anthropic", srv.URL, "key", nil, false)

	req := anthropicTestRequest([]Message{{Role: RoleUser, Content: "hello"}})
	req.DisableProviderReplay = true
	req.StreamTransport = true

	if _, err := c.ChatTurn(context.Background(), req); err == nil {
		t.Fatal("expected the 503 to surface")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("streamed path saw %d requests, want exactly 1", got)
	}
}

// Without the flag the retry budget still applies, so the fix cannot be "never
// retry Anthropic".
func TestAnthropicRetriesWhenReplayIsAllowed(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"recovered"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)

	c := newAnthropicCompleter("anthropic", srv.URL, "key", nil, false)
	resp, err := c.ChatTurn(context.Background(), anthropicTestRequest([]Message{
		{Role: RoleUser, Content: "hello"},
	}))
	if err != nil {
		t.Fatalf("a retryable 503 should have been retried: %v", err)
	}
	if resp.Content != "recovered" {
		t.Fatalf("Content = %q, want %q", resp.Content, "recovered")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("provider saw %d requests, want 2", got)
	}
}
