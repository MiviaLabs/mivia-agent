package provider

// Every provider body read must be bounded by the stream watchdogs. The
// OpenAI-compatible client wraps all four of its read sites; the native
// Anthropic client wrapped none, so a connection that accepted the request and
// then went silent blocked to the 15-minute client wall - with no human
// watching, because nested subagent turns do not stream.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// silentAfterHeaders answers 200 with the given content type, writes any
// preamble, flushes so the client sees a live response, and then sends no
// further byte until the test releases it.
// Cleanup order matters and is LIFO: srv.Close waits for the in-flight
// handler, so the release must be registered LAST to run FIRST. Registering
// them the other way round deadlocks the test binary, not the product.
func silentAfterHeaders(t *testing.T, contentType, preamble string) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("test server response is not flushable")
			return
		}
		if preamble != "" {
			_, _ = io.WriteString(w, preamble)
		}
		flusher.Flush()
		<-release
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv
}

// callWithin runs fn and fails the test if it has not returned by limit,
// returning fn's error so the caller can assert on the failure shape.
func callWithin(t *testing.T, limit time.Duration, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatalf("provider read blocked past %s; the watchdog never fired", limit)
		return nil
	}
}

// The non-stream path is the operationally common one: nested and subagent
// turns never stream, so this read is what a stalled connection holds.
func TestAnthropicNonStreamBodyStallFailsFast(t *testing.T) {
	withWatchdogTimeouts(t, 100*time.Millisecond, 100*time.Millisecond)
	srv := silentAfterHeaders(t, "application/json", "")

	c := newAnthropicCompleter("anthropic", srv.URL, "key", nil, false)
	err := callWithin(t, 20*time.Second, func() error {
		_, callErr := c.ChatTurn(context.Background(), anthropicTestRequest([]Message{
			{Role: RoleUser, Content: "hello"},
		}))
		return callErr
	})
	if !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("non-stream body stall = %v, want it to surface ErrStreamIdle", err)
	}
}

// The streaming path stalls the same way once a provider has opened the SSE
// stream and stopped feeding it.
func TestAnthropicStreamBodyStallFailsFast(t *testing.T) {
	withWatchdogTimeouts(t, 100*time.Millisecond, 100*time.Millisecond)
	preamble := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":1}}}` + "\n\n"
	srv := silentAfterHeaders(t, "text/event-stream", preamble)

	c := newAnthropicCompleter("anthropic", srv.URL, "key", nil, false)
	req := anthropicTestRequest([]Message{{Role: RoleUser, Content: "hello"}})
	req.Stream = true
	req.StreamWriter = io.Discard

	err := callWithin(t, 20*time.Second, func() error {
		_, callErr := c.ChatTurn(context.Background(), req)
		return callErr
	})
	if !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("stream body stall = %v, want it to surface ErrStreamIdle", err)
	}
}

// A stalled read never delivered an answer, so callers deciding whether to
// re-run a step must see it as transient rather than as a spent budget.
func TestAnthropicStallIsTransient(t *testing.T) {
	withWatchdogTimeouts(t, 100*time.Millisecond, 100*time.Millisecond)
	srv := silentAfterHeaders(t, "application/json", "")

	c := newAnthropicCompleter("anthropic", srv.URL, "key", nil, false)
	err := callWithin(t, 20*time.Second, func() error {
		_, callErr := c.ChatTurn(context.Background(), anthropicTestRequest([]Message{
			{Role: RoleUser, Content: "hello"},
		}))
		return callErr
	})
	if !IsTransient(err) {
		t.Fatalf("a stalled provider read must be transient, got %v", err)
	}
}

// The retry layer drains a rejected response so the connection can be reused.
// That drain is a body read like any other: a provider that sends error
// headers and then stops must not hold the drain open either.
func TestRetryDrainOfHungErrorBodyIsBounded(t *testing.T) {
	withWatchdogTimeouts(t, 100*time.Millisecond, 100*time.Millisecond)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.(http.Flusher).Flush()
		<-release
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	rt := newRetryRoundTripper(http.DefaultTransport.(*http.Transport).Clone(), retryOptions{
		MaxRetries: 1, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond,
	})
	client := &http.Client{Transport: rt}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)

	err := callWithin(t, 20*time.Second, func() error {
		resp, doErr := client.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		return doErr
	})
	// The exchange still fails (a 500 is a 500); what matters is that it
	// returned at all rather than parking on the undelivered body.
	_ = err
}

// Guard against a watchdog so eager it breaks healthy traffic.
func TestAnthropicWatchdogLeavesHealthyReadsAlone(t *testing.T) {
	withWatchdogTimeouts(t, 2*time.Second, 2*time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"fine"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)

	c := newAnthropicCompleter("anthropic", srv.URL, "key", nil, false)
	resp, err := c.ChatTurn(context.Background(), anthropicTestRequest([]Message{
		{Role: RoleUser, Content: "hello"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "fine" {
		t.Fatalf("content = %q, want %q", resp.Content, "fine")
	}
}

// The FAILING path is a body read too. A provider that sends rejection
// headers and then stops answering must not hold httpError open: this is
// where a stall is most likely (an overloaded provider) and least expected.
// Found by mivia.go.provider-body-read-needs-watchdog after the first pass of
// watchdog fixes missed it.
func TestOpenAICompatErrorBodyStallFailsFast(t *testing.T) {
	withWatchdogTimeouts(t, 100*time.Millisecond, 100*time.Millisecond)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest) // permanent: no retry storm
		w.(http.Flusher).Flush()
		<-release
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "probe", BaseURL: srv.URL, APIKey: "k"})
	err := callWithin(t, 20*time.Second, func() error {
		_, callErr := c.ChatTurn(context.Background(), Request{
			Model:    "m",
			Messages: []Message{{Role: RoleUser, Content: "hello"}},
		})
		return callErr
	})
	if err == nil {
		t.Fatal("expected the 400 to surface as an error")
	}
}
