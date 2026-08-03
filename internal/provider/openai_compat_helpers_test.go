package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// deadlineReadErrorReader surfaces the request deadline as a plain read error.
// The HTTP transport usually aborts a stalled body read by closing the
// connection, which bufio.Scanner reports as clean EOF; the sc.Err() deadline
// branches in readStream/readTurnStream are only reachable when the underlying
// reader returns the deadline error itself.
type deadlineReadErrorReader struct{}

func (deadlineReadErrorReader) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }

// TestHTTPErrorStatusMessages covers the status switch in httpError: every
// non-OK response must be drained (so the connection can be reused) and mapped
// to a typed message. A parser that never intercepts keeps every status on the
// drain + switch path.
func TestHTTPErrorStatusMessages(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   string
	}{
		{"unauthorized", http.StatusUnauthorized, "auth failed"},
		{"forbidden", http.StatusForbidden, "auth failed"},
		{"rate limited", http.StatusTooManyRequests, "rate limited"},
		{"generic server error", http.StatusInternalServerError, "HTTP 500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
			}))
			defer srv.Close()

			c := &OpenAICompat{
				name:        "test",
				baseURL:     srv.URL,
				apiKey:      "k",
				errorParser: func(int, []byte) error { return nil },
				client:      &http.Client{Timeout: 5 * time.Second},
			}
			_, err := c.Chat(context.Background(), Request{
				Model:    "m",
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("status %d: err=%v, want substring %q", tc.status, err, tc.want)
			}
		})
	}
}

// TestReadStreamSurfacedDeadlineError covers readStream's scanner-error
// deadline branch: a DeadlineExceeded surfaced by the underlying reader (rather
// than a clean EOF from the transport closing a stalled connection) must
// produce an error naming the armed request deadline while staying
// errors.Is(context.DeadlineExceeded).
func TestReadStreamSurfacedDeadlineError(t *testing.T) {
	c := &OpenAICompat{name: "test", errorParser: openaiErrorParser}
	full, err := c.readStream(
		context.Background(),
		Request{
			Model:    "m",
			Messages: []Message{{Role: RoleUser, Content: "hi"}},
			Timeout:  50 * time.Millisecond,
		},
		deadlineReadErrorReader{},
		io.Discard,
	)
	if err == nil {
		t.Fatal("expected a deadline error from the failing stream reader")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(err, context.DeadlineExceeded) = false; err=%v", err)
	}
	if !strings.Contains(err.Error(), "request deadline") || !strings.Contains(err.Error(), "50ms") {
		t.Fatalf("err=%q should name the armed 50ms request deadline", err)
	}
	if full != "" {
		t.Fatalf("full=%q, want empty (no content arrived before the error)", full)
	}
}
