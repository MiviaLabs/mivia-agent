# Phase 01 - Retry budget and replay safety

Files:

- Modify `internal/provider/retry.go`.
- Modify `internal/provider/retry_test.go`.

Tests first (RED):

- `TestRetryRoundTripper_SucceedsOnFifthAttempt` uses four 429 responses and a
  final 200, asserting five calls.
- `TestRetryRoundTripper_StopsAfterFiveAttempts` asserts the final 429 and five
  calls for a persistent failure.
- `TestRetryRoundTripper_NonReplayableBodyReturnsError` supplies a body with no
  `GetBody` and proves there is no panic or second transport call.
- `TestRetryRoundTripper_PreservesBodyAndIdempotencyKey` proves replayed bytes
  and the request key are stable; separate logical requests remain distinct.

Implementation (GREEN):

- Change only the production default from three to four retries.
- Preserve the shared `retryRoundTripper` boundary and positive custom retry
  option behavior.
- Detect a nil `GetBody` before replay and return a wrapped, controlled error.
- Keep response draining/closing and the existing idempotency-key behavior.

Gate: `go test -run 'TestRetryRoundTripper_(SucceedsOnFifthAttempt|StopsAfterFiveAttempts|NonReplayableBodyReturnsError|PreservesBodyAndIdempotencyKey)' ./internal/provider`.
