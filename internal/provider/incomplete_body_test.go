package provider

import (
	"context"
	"encoding/json"
	"errors"
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
	if _, err := c.Chat(context.Background(), Request{
		Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}); err == nil {
		t.Fatal("Chat() = nil error, want the decode failure")
	}
	if calls != 1+maxIncompleteBodyRetries {
		t.Fatalf("calls = %d, want %d", calls, 1+maxIncompleteBodyRetries)
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
