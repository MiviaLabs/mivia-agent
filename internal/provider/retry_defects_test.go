package provider

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Retry-After is server-controlled. Honouring it verbatim lets a single 429
// park the CLI far past the configured cap, with no output, until the HTTP
// client timeout fires - indistinguishable from a hang.
func TestBackoffClampsRetryAfterToMaxDelay(t *testing.T) {
	rt := newRetryRoundTripper(nil, retryOptions{MaxRetries: 3, BaseDelay: 100 * time.Millisecond, MaxDelay: 5 * time.Second})
	got := rt.backoff(0, retryAfterHeader{delay: time.Hour, valid: true})
	if got > rt.opts.MaxDelay {
		t.Fatalf("Retry-After bypassed MaxDelay: got %s, cap %s", got, rt.opts.MaxDelay)
	}
}

// rand.Int63n panics when its argument is <= 0. A Retry-After date under 4ns
// away divides to zero and takes down the process from inside the transport.
func TestBackoffSurvivesSubNanosecondRetryAfter(t *testing.T) {
	rt := newRetryRoundTripper(nil, retryOptions{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: time.Second})
	for _, d := range []time.Duration{1, 2, 3} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("backoff panicked on Retry-After=%v: %v", d, r)
				}
			}()
			_ = rt.backoff(0, retryAfterHeader{delay: d, valid: true})
		}()
	}
}

// A user cancel must stay identifiable as context.Canceled all the way up: the
// TUI keys off errors.Is(err, context.Canceled) to show the cancel footer and
// to decide whether to drain queued prompts. Reporting the earlier transport
// error instead renders a cancel as a hard provider failure.
// failingTransport fails like a refused dial: a transport error, not a status.
type failingTransport struct{ calls int }

func (f *failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls++
	return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
}

// A user cancel must stay identifiable as context.Canceled all the way up: the
// TUI keys off errors.Is(err, context.Canceled) to show the cancel footer and
// to decide whether to drain queued prompts. When an earlier attempt failed at
// the transport level, that stale error was returned instead, so a cancel was
// rendered as a hard provider failure and queued prompts kept running.
func TestRetryCancelDuringBackoffReportsContextError(t *testing.T) {
	rt := newRetryRoundTripper(&failingTransport{}, retryOptions{MaxRetries: 5, BaseDelay: 500 * time.Millisecond, MaxDelay: 2 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://example.invalid/v1", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		<-time.After(100 * time.Millisecond)
		cancel()
	}()
	_, err = rt.RoundTrip(req)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost its identity behind the earlier transport error: %v", err)
	}
}
