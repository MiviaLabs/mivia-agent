package provider

// Transport retries used to be silent: a transport timeout burned up to five
// fresh client windows with no observable signal anywhere. These tests pin
// the per-retry observer: a custom observer sees every granted retry, the
// default observer logs one line per retry, and the replay-disabled bypass
// stays silent.

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"
)

// restoreObserver puts the default (logging) observer back when the test
// ends, so a leaked custom observer cannot pollute sibling tests.
func restoreObserver(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { SetTransportRetryObserver(nil) })
}

func TestTransportRetryObserverCustomSeesEveryGrantedRetry(t *testing.T) {
	restoreObserver(t)
	var got []TransportRetryEvent
	SetTransportRetryObserver(func(e TransportRetryEvent) {
		got = append(got, e)
	})

	inner := &countingTransport{status: http.StatusServiceUnavailable, body: `{"error":{"message":"down"}}`}
	rt := newRetryRoundTripper(inner, retryOptions{MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond})
	req, err := http.NewRequest(http.MethodGet, "http://example.invalid/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("a status-only sequence returns the final response, got error %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("final status = %d, want 503", resp.StatusCode)
	}
	resp.Body.Close()
	if n := inner.calls.Load(); n != 3 {
		t.Fatalf("attempts = %d, want 3", n)
	}
	// One event per granted retry: attempts 0 and 1 earned a retry, the
	// final attempt did not.
	if len(got) != 2 {
		t.Fatalf("observer saw %d events, want 2 (one per granted retry)", len(got))
	}
	for i, e := range got {
		if e.Attempt != i {
			t.Fatalf("event %d Attempt = %d", i, e.Attempt)
		}
		if e.MaxRetries != 2 {
			t.Fatalf("event %d MaxRetries = %d, want 2", i, e.MaxRetries)
		}
		if e.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("event %d StatusCode = %d, want 503", i, e.StatusCode)
		}
		if e.Err != nil {
			t.Fatalf("event %d Err = %v, want nil for a status retry", i, e.Err)
		}
		if e.Delay <= 0 {
			t.Fatalf("event %d Delay = %v, want the granted backoff", i, e.Delay)
		}
	}
}

// The default observer writes one log line per granted retry. It swaps
// process-global log output, so it must not run in parallel with anything.
func TestTransportRetryObserverDefaultWritesLogLine(t *testing.T) {
	restoreObserver(t)
	SetTransportRetryObserver(nil) // explicit default restore, as a caller would

	orig := log.Writer()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })

	inner := &countingTransport{status: http.StatusTooManyRequests, body: `{"error":{"message":"limited"}}`}
	rt := newRetryRoundTripper(inner, retryOptions{MaxRetries: 1, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond})
	req, err := http.NewRequest(http.MethodGet, "http://example.invalid/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("a status-only sequence returns the final response, got error %v", err)
	}
	resp.Body.Close()
	out := buf.String()
	if !strings.Contains(out, "provider: transport retry attempt 0/1") {
		t.Fatalf("default observer wrote no retry line, got: %q", out)
	}
	if !strings.Contains(out, "status 429") {
		t.Fatalf("log line lost the status, got: %q", out)
	}
}

// The replay-disabled bypass skips the retry loop entirely, so it must emit
// no retry event: the caller asked for single-shot provider traffic.
func TestTransportRetryObserverSilentWhenReplayDisabled(t *testing.T) {
	restoreObserver(t)
	var fired int
	SetTransportRetryObserver(func(TransportRetryEvent) { fired++ })

	inner := &countingTransport{status: http.StatusServiceUnavailable, body: `{"error":{"message":"down"}}`}
	rt := newRetryRoundTripper(inner, retryOptions{MaxRetries: 4, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond})
	ctx := context.WithValue(context.Background(), disableProviderReplayContextKey{}, true)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.invalid/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("bypass returns the response as-is, got error %v", err)
	}
	resp.Body.Close()
	if n := inner.calls.Load(); n != 1 {
		t.Fatalf("attempts = %d, want 1 (replay disabled)", n)
	}
	if fired != 0 {
		t.Fatalf("observer fired %d times on the replay-disabled bypass, want 0", fired)
	}
}
