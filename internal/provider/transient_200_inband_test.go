package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A linear (non-streamed) response can still carry a provider fault the HTTP
// status cannot express: the status line is 200 but the body is an in-band
// error envelope. A transient-class envelope (server_error, internal_error,
// overload, ...) means the call never delivered an answer, so it must classify
// as transient for the coordinator's step-retry layer (runStepWithTransientRetry)
// to re-run the step instead of failing the whole run. This drives the same
// non-streamed Chat entrypoint the linear path uses.
func TestChatRetriesTransient200InBand(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if calls == 1 {
			// In-band transient provider fault on HTTP 200: only the body says
			// something broke, and it is a retryable (transient) class.
			_, _ = w.Write([]byte(`{"error":{"type":"server_error","message":"boom"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"recovered"}}]}`))
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	req := Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}

	// The step-retry layer re-issues the same non-streamed Chat call while it
	// reports a transient provider fault. The first 200-in-band fault must
	// surface as a transient error (NOT a terminal failure), and the re-issued
	// call must then recover against the healthy second response.
	var reply string
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		reply, err = c.Chat(context.Background(), req)
		if err == nil {
			break
		}
		// The fault surfaced at all: prove it is transient, so the step layer
		// is allowed to retry rather than treating it as terminal.
		if !IsTransient(err) {
			t.Fatalf("attempt %d: 200-in-band fault (server_error) must be transient, got IsTransient=false: %v", attempt, err)
		}
		// A real step-level retry would back off here, then re-issue the call.
	}
	if err != nil {
		t.Fatalf("Chat() did not recover from the transient 200-in-band fault: %v", err)
	}
	if reply != "recovered" {
		t.Fatalf("reply = %q, want %q", reply, "recovered")
	}
	if calls < 2 {
		t.Fatalf("calls = %d, want >= 2 (the transient 200-in-band fault must be retried)", calls)
	}
}

// A permanent-class in-band error (invalid_request_error) is a refusal the
// provider will repeat: the call must fail and the server must see exactly one
// request - the step layer must NOT retry a stable rejection.
func TestChatDoesNotRetryPermanent200InBand(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"nope"}}`))
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	_, err := c.Chat(context.Background(), Request{
		Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("a permanent 200-in-band error must surface as an error")
	}
	if IsTransient(err) {
		t.Fatalf("permanent 200-in-band error must NOT be transient, got IsTransient=true: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (a permanent in-band fault must not be retried)", calls)
	}
}
