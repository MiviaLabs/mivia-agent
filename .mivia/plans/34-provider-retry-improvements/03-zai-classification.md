# Phase 03 - z.ai 429 classification

Files:

- Modify `internal/provider/zai_error_test.go`.
- Modify `internal/provider/retry_test.go` only if shared transport fixtures
  are required by the z.ai coverage.

Tests first (RED):

- Known permanent quota/plan codes make exactly one HTTP 429 attempt.
- Transient z.ai codes 1302 and 1305 retry and either succeed or exhaust at
  five total attempts.
- An unknown 429 remains governed by the shared default retry policy.
- A `Retry-After` header does not override the permanent classification.

Implementation (GREEN):

- Keep `zaiNonRetryable` as the provider hook and do not alter its static-code-
  only error policy or expose provider response bodies.
- Do not add z.ai-specific retry loops or configuration.

Gate: `go test -run 'TestZAI|TestRetryRoundTripper' ./internal/provider` and
`go test -race ./internal/provider`.
