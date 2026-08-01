# Phase 02 - Retry-After parsing, backoff, and cancellation

Files:

- Modify `internal/provider/retry.go`.
- Modify `internal/provider/retry_test.go`.

Tests first (RED):

- `TestParseRetryAfter_StandardForms` covers zero, positive decimal seconds,
  RFC1123, RFC850, and ANSI-C HTTP-date forms.
- `TestParseRetryAfter_RejectsMalformedAndOverflow` covers empty, signed,
  negative, decimal, non-numeric, and overflowing values.
- `TestRetryRoundTripper_UsesReceivedRetryAfterHeader` proves a valid header
  takes precedence without a long or flaky sleep.
- `TestRetryRoundTripper_StopsForOverCapRetryAfter` preserves fail-fast behavior.
- `TestRetryRoundTripper_CancelDuring429Backoff` uses a deterministic transport
  and proves `errors.Is(err, context.Canceled)`.

Implementation (GREEN):

- Return presence/validity separately from the parsed duration.
- Parse decimal seconds with overflow checks; parse all HTTP-date forms with
  `http.ParseTime` and a reference-time seam.
- Treat valid zero and past dates as valid zero-delay headers. Use exponential
  jitter only when the header is absent or invalid.
- Preserve the existing in-cap/over-cap rule and cancellable wait.
- Replace direct context equality checks with `errors.Is` in retryability paths.

Gate: focused parser/backoff tests, then `go test -race ./internal/provider`.
