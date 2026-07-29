// Package provider implements LLM chat adapters for mivia.
package provider

import (
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
}

// defaultRetryOptions provides sensible defaults.
func defaultRetryOptions() retryOptions {
	return retryOptions{
		MaxRetries: 3,
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
		// On retries, clone the request body (if any).
		if attempt > 0 && req.Body != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, fmt.Errorf("retry: cannot rewind request body: %w", err)
			}
			req.Body = body
		}

		resp, err := r.inner.RoundTrip(req)
		lastResp = resp
		lastErr = err

		// If successful (2xx), return immediately.
		if err == nil && resp.StatusCode < 400 {
			return resp, nil
		}

		// Determine if we should retry.
		shouldRetry, retryAfter := r.isRetryable(err, resp)

		// If this was the last attempt or not retryable, return.
		if attempt >= r.opts.MaxRetries || !shouldRetry {
			if err != nil {
				return nil, err
			}
			return resp, nil
		}

		// Calculate delay with jitter.
		delay := r.backoff(attempt, retryAfter)

		// Drain (up to 64 KiB) and close the response body so the underlying
		// TCP connection can be reused by the HTTP transport. Without draining,
		// Go's http.Transport opens a new connection for every retry.
		if resp != nil && resp.Body != nil {
			_, _ = io.CopyN(io.Discard, resp.Body, 64*1024)
			resp.Body.Close()
		}

		// Wait for backoff or context cancellation.
		select {
		case <-req.Context().Done():
			// Return the context error, not the earlier transport failure:
			// callers test errors.Is(err, context.Canceled) to distinguish a
			// user cancel from a provider outage. Join keeps the prior cause
			// visible without shadowing the cancellation.
			if lastErr != nil {
				return nil, errors.Join(req.Context().Err(), lastErr)
			}
			return nil, req.Context().Err()
		case <-time.After(delay):
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

// isRetryable checks whether a failed request should be retried.
func (r *retryRoundTripper) isRetryable(err error, resp *http.Response) (bool, time.Duration) {
	// Network/transport errors are always retryable.
	if err != nil {
		// Don't retry context cancellations.
		if err == context.Canceled || err == context.DeadlineExceeded {
			return false, 0
		}
		return true, 0
	}

	if resp == nil {
		return true, 0
	}

	switch resp.StatusCode {
	case http.StatusTooManyRequests: // 429
		return true, parseRetryAfter(resp)
	case http.StatusRequestTimeout: // 408
		return true, 0
	case http.StatusServiceUnavailable: // 503
		return true, parseRetryAfter(resp)
	case http.StatusBadGateway: // 502
		return true, 0
	case http.StatusGatewayTimeout: // 504
		return true, 0
	default:
		// 5xx server errors (not 501 Not Implemented, etc.)
		if resp.StatusCode >= 500 && resp.StatusCode <= 599 {
			return true, 0
		}
		return false, 0
	}
}

// backoff computes the delay before the next retry attempt.
func (r *retryRoundTripper) backoff(attempt int, retryAfter time.Duration) time.Duration {
	// If the server specified Retry-After, honour it (with some jitter).
	if retryAfter > 0 {
		delay := retryAfter
		// Int63n panics on a non-positive bound, which a sub-4ns Retry-After
		// date produces — inside the transport, so it kills the process.
		if quarter := int64(retryAfter) / 4; quarter > 0 {
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

// parseRetryAfter extracts the Retry-After header as a duration.
func parseRetryAfter(resp *http.Response) time.Duration {
	h := resp.Header.Get("Retry-After")
	if h == "" {
		return 0
	}
	// Try seconds as integer.
	if seconds, err := strconv.Atoi(h); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	// Try HTTP-date format.
	if t, err := time.Parse(time.RFC1123, h); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}
	return 0
}

// isRetryableErrorFromTransport checks if an error from RoundTrip is retryable.
// This is used to detect connection refused, DNS failures, TLS handshake errors.
func isRetryableTransportError(err error) bool {
	if err == nil {
		return false
	}
	// Context errors are not retryable.
	if err == context.Canceled || err == context.DeadlineExceeded {
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
