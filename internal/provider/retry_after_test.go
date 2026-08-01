package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// --- parseRetryAfter tests ---

// reference is a fixed clock so HTTP-date cases never depend on wall time.
var reference = time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)

// RFC 9110 allows delay-seconds or an HTTP-date, and an HTTP-date may arrive in
// any of the three forms Go's http.ParseTime accepts. A form the parser cannot
// read is silently downgraded to exponential backoff, so the server's own
// pacing is ignored exactly when it matters.
func TestParseRetryAfter_StandardForms(t *testing.T) {
	for name, tc := range map[string]struct {
		header string
		want   time.Duration
	}{
		// Zero is a real instruction ("retry now"), not a missing header.
		"zero seconds":  {"0", 0},
		"delay seconds": {"5", 5 * time.Second},
		"large seconds": {"3600", time.Hour},
		"rfc1123":       {reference.Add(30 * time.Second).Format(http.TimeFormat), 30 * time.Second},
		"rfc850":        {reference.Add(2 * time.Minute).Format(time.RFC850), 2 * time.Minute},
		"ansic":         {reference.Add(90 * time.Second).Format(time.ANSIC), 90 * time.Second},
		// A window that has already closed means retry now, not "no header".
		"past date": {reference.Add(-time.Hour).Format(http.TimeFormat), 0},
	} {
		t.Run(name, func(t *testing.T) {
			got := parseRetryAfterAt(tc.header, reference)
			if !got.valid {
				t.Fatalf("%q was rejected", tc.header)
			}
			if got.delay != tc.want {
				t.Fatalf("%q: delay=%v, want %v", tc.header, got.delay, tc.want)
			}
		})
	}
}

// A malformed value carries no usable instruction. Accepting one would either
// park the request on a nonsense delay or - worse for a negative value - read
// as "retry immediately" forever.
func TestParseRetryAfter_RejectsMalformedAndOverflow(t *testing.T) {
	for name, header := range map[string]string{
		"empty":         "",
		"blank":         "   ",
		"negative":      "-5",
		"signed":        "+5",
		"fractional":    "1.5",
		"non numeric":   "soon",
		"trailing text": "5 seconds",
		"hex":           "0x10",
		// Beyond what a time.Duration can hold: multiplying would wrap to a
		// negative delay.
		"overflow":      "9223372037",
		"huge overflow": "99999999999999999999",
	} {
		t.Run(name, func(t *testing.T) {
			got := parseRetryAfterAt(header, reference)
			if got.valid {
				t.Fatalf("%q was accepted as %v", header, got.delay)
			}
			if got.delay != 0 {
				t.Fatalf("%q: rejected values must carry no delay, got %v", header, got.delay)
			}
		})
	}
}

// A date beyond what a time.Duration spans (roughly 292 years) must not wrap to
// a negative delay, which would read as "retry immediately" against a server
// that just asked for the opposite. time.Sub saturates, so the delay stays huge
// and the over-cap rule ends the retries.
func TestParseRetryAfter_ExtremeDatesSaturate(t *testing.T) {
	far := parseRetryAfterAt("Fri, 31 Dec 9999 23:59:59 GMT", reference)
	if !far.valid || far.delay <= 0 {
		t.Fatalf("far future date wrapped: %+v", far)
	}
	rt := newRetryRoundTripper(nil, retryOptions{MaxRetries: 4, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Second})
	if _, retry := rt.retryDelay(0, nil, &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": {"Fri, 31 Dec 9999 23:59:59 GMT"}},
	}); retry {
		t.Fatal("retried inside a window it cannot wait out")
	}
	// The symmetric case: a date older than a Duration can express is still
	// "the window has closed", so retry now.
	old := parseRetryAfterAt("Mon, 01 Jan 1601 00:00:00 GMT", reference)
	if !old.valid || old.delay != 0 {
		t.Fatalf("far past date wrapped: %+v", old)
	}
}

// An absent header is not an error either - it just hands pacing back to the
// exponential schedule.
func TestParseRetryAfter_AbsentHeaderIsNotValid(t *testing.T) {
	if got := parseRetryAfter(&http.Response{Header: http.Header{}}); got.valid {
		t.Fatalf("absent header reported as valid: %+v", got)
	}
	if got := parseRetryAfter(&http.Response{Header: http.Header{"Retry-After": {"7"}}}); !got.valid || got.delay != 7*time.Second {
		t.Fatalf("present header parsed as %+v", got)
	}
}

// A valid header is the server's own minimum and outranks the exponential
// schedule, including when it says zero. Falling through to exponential on a
// zero header makes the transport slower than the server asked for, and hides
// the header path from every test that uses it.
func TestRetryRoundTripper_BackoffPrefersValidHeader(t *testing.T) {
	rt := newRetryRoundTripper(nil, retryOptions{MaxRetries: 4, BaseDelay: time.Second, MaxDelay: time.Minute})
	if got := rt.backoff(3, retryAfterHeader{delay: 0, valid: true}); got != 0 {
		t.Fatalf("valid zero header produced %v, want 0", got)
	}
	if got := rt.backoff(3, retryAfterHeader{}); got < time.Second {
		t.Fatalf("absent header should use exponential backoff, got %v", got)
	}
	if got := rt.backoff(0, retryAfterHeader{delay: time.Hour, valid: true}); got > rt.opts.MaxDelay {
		t.Fatalf("header bypassed MaxDelay: got %v, cap %v", got, rt.opts.MaxDelay)
	}
}

// End to end: with a base delay far larger than the test's patience, the run
// can only finish quickly if the received header set the pace.
func TestRetryRoundTripper_UsesReceivedRetryAfterHeader(t *testing.T) {
	inner := &countingTransport{
		status: http.StatusTooManyRequests,
		header: http.Header{"Retry-After": {"0"}},
		body:   `{"error":{"message":"rate limited"}}`,
	}
	rt := newRetryRoundTripper(inner, retryOptions{MaxRetries: 2, BaseDelay: 5 * time.Second, MaxDelay: 30 * time.Second})

	req, err := http.NewRequest(http.MethodGet, "http://example.invalid/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waited %v: the exponential schedule was used instead of Retry-After", elapsed)
	}
	if n := inner.calls.Load(); n != 3 {
		t.Fatalf("expected 3 calls, got %d", n)
	}
}

// Retry-After is clamped to MaxDelay, so a longer window cannot be honoured.
// Retrying at the cap would land inside a window the server just closed:
// guaranteed-fail traffic against an account that is already rate limited.
func TestRetryRoundTripper_StopsForOverCapRetryAfter(t *testing.T) {
	for name, header := range map[string]string{
		"delay seconds": "60",
		"http date":     reference.Add(time.Hour).Format(http.TimeFormat),
	} {
		t.Run(name, func(t *testing.T) {
			inner := &countingTransport{
				status: http.StatusTooManyRequests,
				header: http.Header{"Retry-After": {header}},
				body:   `{"error":{"message":"rate limited"}}`,
			}
			rt := newRetryRoundTripper(inner, retryOptions{MaxRetries: 4, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond})
			req, err := http.NewRequest(http.MethodGet, "http://example.invalid/v1", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("status=%d", resp.StatusCode)
			}
			if n := inner.calls.Load(); n != 1 {
				t.Fatalf("retried inside a window it cannot wait out (%d calls)", n)
			}
		})
	}
}

// A cancel during a 429 backoff must surface as context.Canceled: the TUI keys
// off errors.Is to show the cancel footer and to decide whether to drain queued
// prompts, and a cancel rendered as a provider failure keeps them running.
func TestRetryRoundTripper_CancelDuring429Backoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inner := &countingTransport{
		status:  http.StatusTooManyRequests,
		body:    `{"error":{"message":"rate limited"}}`,
		observe: func(int32) { cancel() }, // cancel while the backoff is pending
	}
	// A base delay no test would sit through: the cancel is what ends the wait.
	rt := newRetryRoundTripper(inner, retryOptions{MaxRetries: 4, BaseDelay: time.Minute, MaxDelay: 2 * time.Minute})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://example.invalid/v1", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := rt.RoundTrip(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost its identity: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancel did not interrupt the backoff (waited %v)", elapsed)
	}
	if n := inner.calls.Load(); n != 1 {
		t.Fatalf("expected 1 call, got %d", n)
	}
}

// Transports wrap the context error rather than returning it bare (net.OpError,
// url.Error). Comparing with == treats a wrapped cancel as a transient network
// fault and replays the request the user just cancelled.
func TestRetryRoundTripper_WrappedCancellationIsNotRetried(t *testing.T) {
	for name, cause := range map[string]error{
		"canceled": context.Canceled,
		"deadline": context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			inner := &countingTransport{err: fmt.Errorf("Post \"http://example.invalid\": %w", cause)}
			rt := newRetryRoundTripper(inner, retryOptions{MaxRetries: 4, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond})
			req, err := http.NewRequest(http.MethodGet, "http://example.invalid/v1", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := rt.RoundTrip(req); !errors.Is(err, cause) {
				t.Fatalf("error lost its cause: %v", err)
			}
			if n := inner.calls.Load(); n != 1 {
				t.Fatalf("replayed a cancelled request (%d calls)", n)
			}
		})
	}
}
