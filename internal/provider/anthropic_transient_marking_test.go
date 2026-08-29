package provider

// markTransientReadDeadline's contract is stated by transient_test.go: its
// first argument must be the ARMED REQUEST context. It marks a deadline
// transient only when that context still has time left, which proves some
// tighter bound fired rather than the request's own budget.
//
// The Anthropic client armed req.Timeout on a shadowed local inside
// newHTTPRequest and handed back only the request and its cancel, so the send
// paths still held the caller's parent context and passed THAT. Under a live
// parent deadline the test then reads backwards: a request that spent its
// entire req.Timeout and answered nothing is marked transient, and the step
// loop re-asks a call that will fail identically after another full timeout.
//
// The OpenAI-compatible client arms its deadline at the entry point and hands
// the armed context down, which is why only these sites were wrong.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// stalledServer accepts the request and then answers nothing until released,
// optionally flushing headers first so the stall lands on the body read.
func stalledServer(t *testing.T, sendHeaders bool) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if sendHeaders {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
		}
		<-release
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv
}

// anthropicSpentBudgetError drives one turn whose own req.Timeout expires
// while the PARENT context still has plenty of time, and returns the error.
func anthropicSpentBudgetError(t *testing.T, srv *httptest.Server, stream bool) error {
	t.Helper()
	// A live parent deadline is the condition that made the bug visible: with
	// no parent deadline the check reports not-ok and nothing is marked.
	parent, cancelParent := context.WithTimeout(context.Background(), time.Minute)
	defer cancelParent()

	c := newAnthropicCompleter("anthropic", srv.URL, "key", nil, false)
	req := anthropicTestRequest([]Message{{Role: RoleUser, Content: "hello"}})
	req.Timeout = 250 * time.Millisecond // the request's OWN budget
	if stream {
		req.Stream = true
		req.StreamWriter = io.Discard
	}

	done := make(chan error, 1)
	go func() {
		_, err := c.ChatTurn(parent, req)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("the request timeout never fired")
		return nil
	}
}

// A request that spends its whole budget and answers nothing is NOT transient:
// re-asking it burns another full timeout for the same result.
func TestAnthropicSpentRequestBudgetIsNotTransient(t *testing.T) {
	// Keep the watchdogs far away so the deadline is what fires.
	withWatchdogTimeouts(t, time.Minute, time.Minute)

	for _, tc := range []struct {
		name        string
		sendHeaders bool
		stream      bool
	}{
		{"no headers, non-stream", false, false},
		{"headers then body stall, non-stream", true, false},
		{"no headers, streaming", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := anthropicSpentBudgetError(t, stalledServer(t, tc.sendHeaders), tc.stream)
			if err == nil {
				t.Fatal("expected the request timeout to surface")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v, want the request deadline", err)
			}
			if IsTransient(err) {
				t.Fatalf("a spent request budget must not be transient: %v\n"+
					"  marking it transient re-asks a call that will fail identically\n"+
					"  after another full req.Timeout", err)
			}
		})
	}
}

// Parity with the sibling client, which is the property that actually matters
// here. A tighter PARENT deadline cannot be told apart from a spent request
// budget by either client - context.WithTimeout adopts an earlier parent
// deadline, so the armed context reports the parent's - and that is fine,
// because a fired parent deadline ends the step anyway. What must never differ
// is how the two clients classify the SAME scenario.
func TestAnthropicAndCompatAgreeOnSpentBudget(t *testing.T) {
	withWatchdogTimeouts(t, time.Minute, time.Minute)

	parent, cancelParent := context.WithTimeout(context.Background(), time.Minute)
	defer cancelParent()

	srv := stalledServer(t, false)
	compat := NewOpenAICompatWithOptions(CompatOptions{Name: "compat", BaseURL: srv.URL, APIKey: "k"})
	anthropic := newAnthropicCompleter("anthropic", srv.URL, "key", nil, false)

	ask := func(c Completer) error {
		req := Request{
			Model:    "m",
			Messages: []Message{{Role: RoleUser, Content: "hello"}},
			Timeout:  250 * time.Millisecond,
		}
		done := make(chan error, 1)
		go func() { _, err := c.ChatTurn(parent, req); done <- err }()
		select {
		case err := <-done:
			return err
		case <-time.After(30 * time.Second):
			t.Fatal("the request timeout never fired")
			return nil
		}
	}

	compatErr, anthropicErr := ask(compat), ask(anthropic)
	if compatErr == nil || anthropicErr == nil {
		t.Fatal("both clients should have surfaced the spent budget")
	}
	if IsTransient(compatErr) != IsTransient(anthropicErr) {
		t.Fatalf("clients disagree on the same spent budget: compat transient=%v (%v), anthropic transient=%v (%v)",
			IsTransient(compatErr), compatErr, IsTransient(anthropicErr), anthropicErr)
	}
}
