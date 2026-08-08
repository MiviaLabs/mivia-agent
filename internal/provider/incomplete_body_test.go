package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A truncated response body must not fail the caller. The body arrives with
// status 200, so no HTTP layer sees a fault, and one cut response used to fail
// the workflow step that made the call. A step failure routes to the failure
// terminal, so a whole run lost its finished work.
func TestChatRetriesATruncatedResponseBody(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if calls == 1 {
			// Cut short: valid JSON never arrives.
			_, _ = w.Write([]byte(`{"choices":[{"message":{"cont`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"recovered"}}]}`))
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	reply, err := c.Chat(context.Background(), Request{
		Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v, want the retry to recover", err)
	}
	if reply != "recovered" {
		t.Fatalf("reply = %q, want %q", reply, "recovered")
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (one cut short, one good)", calls)
	}
}

// A body that stays truncated still fails, and the caller still learns why.
// The final error must be transient: the per-call budget is spent on a body
// that was provably cut short, so the call never delivered an answer and the
// step-level retry (runStepWithTransientRetry) must still fire instead of
// failing the whole run on the first attempt's bare JSON syntax error.
func TestChatFailsWhenEveryTryIsTruncated(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"mess`))
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	_, err := c.Chat(context.Background(), Request{
		Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("Chat() = nil error, want the decode failure")
	}
	if calls != 1+maxIncompleteBodyRetries {
		t.Fatalf("calls = %d, want %d", calls, 1+maxIncompleteBodyRetries)
	}
	if !IsTransient(err) {
		t.Fatalf("final error = %v, want transient: a persistently cut body must let the step-level retry fire", err)
	}
	var transient *TransientError
	if !errors.As(err, &transient) {
		t.Fatalf("final error = %T %v, want a *TransientError", err, err)
	}
}

// A good response is never repeated.
func TestChatDoesNotRepeatAGoodResponse(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	if _, err := c.Chat(context.Background(), Request{
		Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestIsIncompleteBody(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"eof", io.EOF, true},
		{"json syntax", &json.SyntaxError{}, true},
		{"wrapped syntax", errors.Join(errors.New("decode response"), &json.SyntaxError{}), true},
		{"other", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isIncompleteBody(tc.err); got != tc.want {
				t.Fatalf("isIncompleteBody(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// A JSON syntax error that did NOT come from this package's transport is not a
// transport fault. Callers parse agent output with the same standard library,
// and a malformed answer is a bad answer, not a broken connection. Retrying it
// repeats it.
func TestAgentOutputSyntaxErrorIsNotTransient(t *testing.T) {
	var target map[string]any
	err := json.Unmarshal([]byte(`{"verdict": `), &target)
	if err == nil {
		t.Fatal("fixture must produce a syntax error")
	}
	if IsTransient(err) {
		t.Fatal("a bare JSON syntax error must not be a transport fault: at a call site parsing agent output it is a bad answer")
	}
}

// The provider's OWN cut body is still transient, because the read site marks
// it where the difference is known.
func TestProviderMarkedIncompleteBodyIsStillTransient(t *testing.T) {
	var target map[string]any
	syntaxErr := json.Unmarshal([]byte(`{"choices": `), &target)
	if !IsTransient(asTransient(&TransientError{Err: syntaxErr})) {
		t.Fatal("a body the provider marked as cut short must stay transient")
	}
}

// context.DeadlineExceeded satisfies net.Error with Timeout() == true, so a
// generic net.Error timeout test classifies it as a transport fault. It is not
// one: a step deadline and a run deadline both surface this way, and retrying
// repeats a call under the context that just expired. This pinned a real
// regression where an expired run retried its step three times over 100s.
func TestDeadlineIsNotATransportFault(t *testing.T) {
	if IsTransient(context.DeadlineExceeded) {
		t.Fatal("context.DeadlineExceeded must not be transient: it satisfies net.Error with Timeout() true")
	}
	if IsTransient(fmt.Errorf("run step: %w", context.DeadlineExceeded)) {
		t.Fatal("a wrapped deadline must not be transient either")
	}
	if IsTransient(context.Canceled) {
		t.Fatal("a cancelled call must not be transient")
	}
}
