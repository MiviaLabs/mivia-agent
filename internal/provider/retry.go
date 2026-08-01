// Package provider implements LLM chat adapters for mivia.
package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// retryOptions configures the retry round tripper.
type retryOptions struct {
	// MaxRetries is the maximum number of retry attempts (0 disables retry).
	MaxRetries int
	// BaseDelay is the initial backoff delay.
	BaseDelay time.Duration
	// MaxDelay is the maximum backoff delay cap.
	MaxDelay time.Duration
	// NonRetryable lets a provider classify an error response as permanent from
	// its body, which the status code alone cannot express: z.ai reports both a
	// transient rate limit and an exhausted plan as HTTP 429. It is consulted
	// only for statuses the shared policy already retries, so nil leaves that
	// policy exactly as it was.
	NonRetryable func(statusCode int, body []byte) bool
}

// maxErrorPeekBytes bounds how much of an error body NonRetryable sees. It
// matches the cap the error parsers use, so both read the same prefix.
const maxErrorPeekBytes = 4096

// errNonReplayableBody marks a request the transport cannot retry: it carries a
// body but no GetBody, so the bytes cannot be produced a second time.
var errNonReplayableBody = errors.New("request body has no GetBody")

// defaultRetryOptions provides sensible defaults. Four retries plus the initial
// request give five attempts per outbound transport exchange, which is the
// budget one provider request gets - a stream fallback or a further agent-loop
// step is a separate request with its own budget.
func defaultRetryOptions() retryOptions {
	return retryOptions{
		MaxRetries: 4,
		BaseDelay:  200 * time.Millisecond,
		MaxDelay:   5 * time.Second,
	}
}

// retryRoundTripper wraps an http.RoundTripper with retry logic for
// transient failures (network errors, rate limits, server errors).
type retryRoundTripper struct {
	inner http.RoundTripper
	opts  retryOptions
}

// newRetryRoundTripper wraps inner with retry logic.
func newRetryRoundTripper(inner http.RoundTripper, opts retryOptions) *retryRoundTripper {
	if inner == nil {
		inner = http.DefaultTransport
	}
	if opts.MaxRetries <= 0 {
		opts.MaxRetries = defaultRetryOptions().MaxRetries
	}
	if opts.BaseDelay <= 0 {
		opts.BaseDelay = defaultRetryOptions().BaseDelay
	}
	if opts.MaxDelay <= 0 {
		opts.MaxDelay = defaultRetryOptions().MaxDelay
	}
	return &retryRoundTripper{inner: inner, opts: opts}
}

// RoundTrip performs the request with retries on transient failures.
func (r *retryRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	var (
		lastErr  error
		lastResp *http.Response
	)

	for attempt := 0; attempt <= r.opts.MaxRetries; attempt++ {
		resp, err := r.inner.RoundTrip(req)
		lastResp = resp
		lastErr = err

		// If successful (2xx), return immediately.
		if err == nil && resp.StatusCode < 400 {
			return resp, nil
		}

		delay, retry := r.retryDelay(attempt, err, resp)
		if !retry {
			if err != nil {
				return nil, err
			}
			return resp, nil
		}

		// Release the response before the wait so the transport can reuse the
		// connection, then restage the body for the next attempt.
		drainAndClose(resp)
		if rewindErr := rewindBody(req); rewindErr != nil {
			return nil, rewindErr
		}
		if waitErr := waitBeforeRetry(req.Context(), delay, lastErr); waitErr != nil {
			// The restaged body never reached a transport, so nothing else will
			// close it. GetBody may hand back a real handle, not just a reader
			// over bytes, and abandoning it here leaks it for the process's life.
			if req.Body != nil {
				req.Body.Close()
			}
			return nil, waitErr
		}
	}

	// Should not reach here, but handle defensively.
	if lastErr != nil {
		return nil, lastErr
	}
	if lastResp != nil {
		return lastResp, nil
	}
	return nil, fmt.Errorf("retry: exhausted attempts with no response")
}

// retryDelay reports whether this attempt earns another try, and how long to
// wait first.
func (r *retryRoundTripper) retryDelay(attempt int, err error, resp *http.Response) (time.Duration, bool) {
	if attempt >= r.opts.MaxRetries {
		return 0, false
	}
	shouldRetry, header := r.isRetryable(err, resp)
	if !shouldRetry {
		return 0, false
	}
	// backoff clamps Retry-After to MaxDelay so one 429 cannot park the CLI
	// for as long as the server likes. That clamp makes a longer wait
	// unhonourable, and every attempt would land inside the window the
	// server just closed - guaranteed-fail traffic against an account that
	// is already rate limited. Surface the error instead.
	if header.valid && header.delay > r.opts.MaxDelay {
		return 0, false
	}
	return r.backoff(attempt, header), true
}

// rewindBody restages a request body for another attempt. GetBody is the only
// way to produce the bytes a second time: calling a nil one panics inside the
// transport and takes the process down, and replaying without it would send an
// empty body - a different question to the one the caller asked. Fail the
// exchange instead, before another request reaches the provider.
func rewindBody(req *http.Request) error {
	if req.Body == nil {
		return nil
	}
	if req.GetBody == nil {
		return fmt.Errorf("retry: cannot rewind request body: %w", errNonReplayableBody)
	}
	body, err := req.GetBody()
	if err != nil {
		return fmt.Errorf("retry: cannot rewind request body: %w", err)
	}
	req.Body = body
	return nil
}

// waitBeforeRetry blocks for delay unless the request context ends first.
func waitBeforeRetry(ctx context.Context, delay time.Duration, cause error) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		// Return the context error, not the earlier transport failure:
		// callers test errors.Is(err, context.Canceled) to distinguish a
		// user cancel from a provider outage. Join keeps the prior cause
		// visible without shadowing the cancellation.
		if cause != nil {
			return errors.Join(ctx.Err(), cause)
		}
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// drainAndClose releases a response the transport is about to discard. Draining
// (up to 64 KiB) lets the HTTP transport reuse the TCP connection; without it,
// Go opens a fresh connection for every retry.
func drainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.CopyN(io.Discard, resp.Body, 64*1024)
	resp.Body.Close()
}

// isRetryable checks whether a failed request should be retried.
func (r *retryRoundTripper) isRetryable(err error, resp *http.Response) (bool, retryAfterHeader) {
	// Network/transport errors are always retryable.
	if err != nil {
		// Don't retry context cancellations. Transports wrap the cause
		// (net.OpError, url.Error), so compare with errors.Is: == would read a
		// wrapped cancel as a transient fault and replay a request the user
		// just cancelled.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, retryAfterHeader{}
		}
		return true, retryAfterHeader{}
	}

	if resp == nil {
		return true, retryAfterHeader{}
	}

	switch resp.StatusCode {
	case http.StatusTooManyRequests: // 429
		return r.retryableWithBody(resp), parseRetryAfter(resp)
	case http.StatusRequestTimeout: // 408
		return true, retryAfterHeader{}
	case http.StatusServiceUnavailable: // 503
		return r.retryableWithBody(resp), parseRetryAfter(resp)
	case http.StatusBadGateway: // 502
		return true, retryAfterHeader{}
	case http.StatusGatewayTimeout: // 504
		return true, retryAfterHeader{}
	default:
		// 5xx server errors (not 501 Not Implemented, etc.)
		if resp.StatusCode >= 500 && resp.StatusCode <= 599 {
			return true, retryAfterHeader{}
		}
		return false, retryAfterHeader{}
	}
}

// retryableWithBody asks the provider classifier, if any, whether this error is
// permanent. The body is restored afterwards so the caller still reads it whole.
func (r *retryRoundTripper) retryableWithBody(resp *http.Response) bool {
	if r.opts.NonRetryable == nil {
		return true
	}
	return !r.opts.NonRetryable(resp.StatusCode, peekBody(resp))
}

// peekBody reads the head of resp.Body and puts it back, leaving the response
// readable from the start.
func peekBody(resp *http.Response) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}
	original := resp.Body
	head, err := io.ReadAll(io.LimitReader(original, maxErrorPeekBytes))
	if err != nil && len(head) == 0 {
		return nil
	}
	resp.Body = peekedBody{Reader: io.MultiReader(bytes.NewReader(head), original), Closer: original}
	return head
}

// peekedBody re-fronts a partially read body with the bytes already consumed.
type peekedBody struct {
	io.Reader
	io.Closer
}

// backoff computes the delay before the next retry attempt.
func (r *retryRoundTripper) backoff(attempt int, header retryAfterHeader) time.Duration {
	// A valid Retry-After is the server's own minimum and outranks the
	// exponential schedule, including when it says zero. Treating zero as
	// "absent" would wait longer than the server asked for.
	if header.valid {
		delay := header.delay
		// Int63n panics on a non-positive bound, which a sub-4ns Retry-After
		// date produces - inside the transport, so it kills the process.
		if quarter := int64(delay) / 4; quarter > 0 {
			delay += time.Duration(rand.Int63n(quarter))
		}
		// Retry-After is server-controlled: honour it, but never past our own
		// cap, or one 429 parks the CLI for as long as the server likes.
		if delay > r.opts.MaxDelay {
			delay = r.opts.MaxDelay
		}
		return delay
	}

	// Exponential backoff: base * 2^attempt with jitter.
	delay := float64(r.opts.BaseDelay) * math.Pow(2, float64(attempt))
	jitter := rand.Float64() * delay * 0.5 // up to 50% jitter
	delay += jitter

	if delay > float64(r.opts.MaxDelay) {
		delay = float64(r.opts.MaxDelay)
	}
	return time.Duration(delay)
}

// retryAfterHeader is a parsed Retry-After response header. Validity is carried
// separately from the delay because zero is a real instruction - "retry now" -
// and collapsing it into the absent case silently hands pacing back to the
// exponential schedule.
type retryAfterHeader struct {
	delay time.Duration
	valid bool
}

// maxRetryAfterSeconds is the largest delay-seconds value a time.Duration can
// still represent. Anything above it would wrap to a negative delay.
const maxRetryAfterSeconds = int64(math.MaxInt64) / int64(time.Second)

// parseRetryAfter extracts the Retry-After header from a response.
func parseRetryAfter(resp *http.Response) retryAfterHeader {
	if resp == nil {
		return retryAfterHeader{}
	}
	return parseRetryAfterAt(resp.Header.Get("Retry-After"), time.Now())
}

// parseRetryAfterAt parses a Retry-After field value against a reference time,
// which keeps HTTP-date handling testable without a wall clock. RFC 9110 allows
// delay-seconds or an HTTP-date; anything else carries no usable instruction
// and is reported as invalid so the caller falls back to exponential jitter.
func parseRetryAfterAt(value string, now time.Time) retryAfterHeader {
	value = strings.TrimSpace(value)
	if value == "" {
		return retryAfterHeader{}
	}
	// delay-seconds is 1*DIGIT: no sign, no decimal point, no trailing units.
	// strconv would accept "+5" and "-5", and a negative delay reads as "retry
	// immediately, forever", so screen the digits first.
	if isASCIIDigits(value) {
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil || seconds > maxRetryAfterSeconds {
			return retryAfterHeader{}
		}
		return retryAfterHeader{delay: time.Duration(seconds) * time.Second, valid: true}
	}
	// http.ParseTime covers the three HTTP-date forms: RFC1123 (preferred),
	// RFC850, and ANSI C asctime.
	deadline, err := http.ParseTime(value)
	if err != nil {
		return retryAfterHeader{}
	}
	delay := deadline.Sub(now)
	if delay < 0 {
		// The window has already closed, so the instruction is "retry now" -
		// still a valid header, not a missing one.
		delay = 0
	}
	return retryAfterHeader{delay: delay, valid: true}
}

// isASCIIDigits reports whether s is one or more decimal digits and nothing else.
func isASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isRetryableErrorFromTransport checks if an error from RoundTrip is retryable.
// This is used to detect connection refused, DNS failures, TLS handshake errors.
func isRetryableTransportError(err error) bool {
	if err == nil {
		return false
	}
	// Context errors are not retryable, wrapped or bare: the phrase match
	// below would otherwise read a wrapped deadline as an "i/o timeout".
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	errStr := err.Error()
	// Connection refused, no such host, timeout, TLS handshake, etc.
	retryablePhrases := []string{
		"connection refused",
		"no such host",
		"connection reset",
		"broken pipe",
		"tls handshake",
		"i/o timeout",
		"dial tcp",
		"connect: connection refused",
		"eof",
	}
	lower := strings.ToLower(errStr)
	for _, phrase := range retryablePhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}
