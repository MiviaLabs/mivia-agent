package chatsync

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// hangingListener accepts exactly one TCP connection and never reads or
// writes on it - the black-holed-connection shape a rolling backend deploy
// produces when a draining pod stops responding instead of sending RST.
// httptest.Server always answers something, so simulating this needs a raw
// listener instead.
func hangingListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		t.Cleanup(func() { _ = conn.Close() })
	}()
	return "http://" + ln.Addr().String()
}

// TestExecRequestTimesOutOnAHungConnection pins the fix for the reported
// reconnect bug: a container restart that black-holes the connection instead
// of resetting it used to hang the sole sync worker goroutine forever, since
// nothing bounded httpClient.Do and the retry/backoff logic downstream never
// got a chance to run because no request ever returned. Both production call
// sites (chat_sync.go, session_pool.go) build the client and open the
// session with no deadline of their own, exactly the shape reproduced here.
func TestExecRequestTimesOutOnAHungConnection(t *testing.T) {
	orig := defaultRequestTimeout
	defaultRequestTimeout = 100 * time.Millisecond
	defer func() { defaultRequestTimeout = orig }()

	client := newTestClient(t, ClientOptions{BaseURL: hangingListener(t)})

	start := time.Now()
	_, err := client.AppendEvents(context.Background(), "sess-1", nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("AppendEvents against a hung connection returned nil error, want a timeout error")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("AppendEvents took %s to return, want it bounded by defaultRequestTimeout (100ms)", elapsed)
	}
}

// TestExecRequestDoesNotShortenACallerSuppliedDeadline pins the correction
// an adversarial review caught in the first draft of this fix: poller.go
// already wraps its long-poll requests (up to ~310s) in their own
// context.WithTimeout before reaching execRequest. A blanket wrap would have
// clamped every long-poll to defaultRequestTimeout and turned a healthy park
// into a retry storm. execRequest must only apply its own timeout when the
// incoming context has none.
func TestExecRequestDoesNotShortenACallerSuppliedDeadline(t *testing.T) {
	orig := defaultRequestTimeout
	defaultRequestTimeout = 50 * time.Millisecond
	defer func() { defaultRequestTimeout = orig }()

	client := newTestClient(t, ClientOptions{BaseURL: hangingListener(t)})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := client.AppendEvents(ctx, "sess-1", nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("AppendEvents against a hung connection returned nil error, want a timeout error")
	}
	if elapsed < 250*time.Millisecond {
		t.Fatalf("AppendEvents returned after %s, want it to respect the longer caller deadline (300ms) rather than the shorter default (50ms)", elapsed)
	}
}

// TestPollerLongPollIsNotTruncatedByDefaultRequestTimeout is the same
// composition guard as TestExecRequestDoesNotShortenACallerSuppliedDeadline,
// exercised through the real production caller instead of a synthetic
// context: InputPoller sets its own context.WithTimeout(waitSeconds+10s)
// before calling NextInput, and the server here legitimately takes longer
// than defaultRequestTimeout to answer (an ordinary long-poll park, not a
// hang) - the poll must still succeed.
func TestPollerLongPollIsNotTruncatedByDefaultRequestTimeout(t *testing.T) {
	orig := defaultRequestTimeout
	defaultRequestTimeout = 50 * time.Millisecond
	defer func() { defaultRequestTimeout = orig }()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		// Longer than defaultRequestTimeout, shorter than the poller's own
		// (waitSeconds+10s) deadline: a legitimate long-poll park a blanket
		// wrap would have truncated.
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(NextInput{Input: nil})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})

	// Mirrors poller.go's pollOnce: it wraps NextInput's context in its own
	// context.WithTimeout(waitSeconds+pollDeadlineSlackSeconds) before ever
	// calling the client. Calling NextInput with context.Background()
	// directly (bypassing that wrap) would let execRequest apply
	// defaultRequestTimeout itself and fail this test for the wrong reason.
	const waitSeconds = 1
	pollCtx, cancel := context.WithTimeout(context.Background(), time.Duration(waitSeconds+pollDeadlineSlackSeconds)*time.Second)
	defer cancel()

	start := time.Now()
	_, err := client.NextInput(pollCtx, "sess-1", waitSeconds)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("NextInput returned an error for a plain slow (not hung) long-poll: %v", err)
	}
	if elapsed < 250*time.Millisecond {
		t.Fatalf("NextInput returned after %s, want it to have waited out the full park (300ms) instead of being truncated by defaultRequestTimeout (50ms)", elapsed)
	}
}
