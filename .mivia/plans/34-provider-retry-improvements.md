# 34 - Provider 429 retry and backoff improvements

**Status:** VALIDATED - implementation-ready after source validation and phase breakdown.
**Date:** 2026-08-01
**Depends on:** current shared OpenAI-compatible provider transport.
**Blocks:** nothing.
**Blast radius:** MEDIUM - provider request reliability, streaming error behavior,
request replay, and provider-specific 429 classification.

## 0. Goal

Make the default LLM provider transport retry transient HTTP 429 responses with
bounded, cancellable, header-aware backoff for **five attempts per outbound
transport exchange**: one initial request plus four retries. Preserve provider
specific classification, especially z.ai's distinction between transient rate
limits and permanent quota/plan failures.

The five-attempt limit is per `RoundTrip`/outbound provider request. A separate
stream fallback, agent-loop step, redirect, or logical follow-up request is a
separate request and has its own transport budget. This plan does not attempt to
bound an entire chat turn or agent run.

## 1. Evidence and locked decisions

The investigation found that all built-in LLM providers use the same client:

- DeepSeek, OpenRouter, and z.ai are registered in
  `internal/providerregistry/registry.go`.
- Their factories all construct `OpenAICompat`.
- `OpenAICompat` installs `retryRoundTripper` over `http.DefaultTransport`.
- The current default is `MaxRetries=3`, which yields four total attempts.
- The coordinator retry policy is separate and does not process provider HTTP
  response headers.

Locked decisions:

| Concern | Decision |
|---|---|
| Attempt count | Change the production default from three retries to four retries, yielding five total transport attempts. Keep the internal option's existing `MaxRetries` meaning. |
| Retry scope | Keep one shared transport boundary. Do not add retry loops to CLI, session, agent, or coordinator callers. |
| Header | Support the standard `Retry-After` response header only. Do not guess undocumented `X-RateLimit-*` semantics for these providers. |
| Header parsing | Accept non-negative decimal seconds and all HTTP-date forms through `http.ParseTime`. Distinguish absent/invalid from valid zero. Reject malformed and overflowing values. |
| Backoff precedence | A valid, in-cap `Retry-After` is the server minimum and is used with existing jitter policy. Missing or invalid headers use exponential jitter. |
| Over-cap header | Preserve the current fail-fast rule: a valid `Retry-After` beyond `MaxDelay` ends retrying rather than issuing a request inside the provider's declared limit window. |
| z.ai | Keep body classification in `zaiNonRetryable`; known permanent quota/plan codes remain one-attempt failures, while transient codes remain retryable. |
| Streaming | HTTP 429 before stream commitment is transport-retryable. HTTP-200 in-band SSE errors are not automatically replayed after commitment. |
| Configuration | Do not add TOML retry settings or export `retryOptions` in this slice. Production constructors currently use internal defaults. |
| Privacy | Do not log raw provider bodies, prompts, keys, or model output. Preserve z.ai's static-code-only error policy. |

Authoritative external references:

- [OpenRouter error handling](https://openrouter.ai/docs/api_reference/errors-and-debugging)
  documents `Retry-After` on 429/503 responses and HTTP-200 in-band streaming
  errors.
- [DeepSeek rate limits](https://api-docs.deepseek.com/quick_start/rate_limit/)
  documents HTTP 429 for exceeded concurrency limits.
- [z.ai error codes](https://docs.z.ai/api-reference/api-code) distinguishes
  transient 429 codes such as 1302/1305 from quota and plan errors.
- [RFC 9110](https://www.rfc-editor.org/rfc/rfc9110.html) defines
  `Retry-After` as delay-seconds or HTTP-date.

## 2. Files and API surface

### Modify

- `internal/provider/retry.go`
  - Set the default retry count to four retries.
  - Make `Retry-After` parsing return validity/presence separately from the
    duration so `Retry-After: 0` is not confused with absence.
  - Use safe numeric parsing and `http.ParseTime`.
  - Preserve bounded, cancellable backoff and over-cap termination.
  - Use `errors.Is` for context cancellation/deadline detection.
  - Return a controlled error instead of panicking when a retried request has a
    body but no `GetBody` replay function.

- `internal/provider/retry_test.go`
  - Add unit and transport integration coverage described below.
  - Correct existing wire fixtures so response headers are set before
    `WriteHeader`.

- `internal/provider/zai_error_test.go`
  - Add exact-attempt assertions for transient and permanent z.ai 429 cases,
    including interaction with `Retry-After`.

- `internal/provider/stream_defects_test.go` or the existing provider stream
  test file that owns the scenario
  - Prove pre-commit HTTP 429 retry and committed HTTP-200 in-band error
    non-replay behavior for both stream paths.

- `docs/architecture/overview.md`
  - Add the transport-level provider retry contract, z.ai exception, and
    committed-stream boundary.

### Do not modify in this slice

- `internal/config/*`: no user-configurable retry surface.
- `internal/coordinator/*`: task retries are a separate mechanism.
- Provider factories: they already share the correct transport boundary.
- `docs/product/config.md`: retry behavior is not configurable.

## 2a. Validation amendments

The plan is locked with these implementation clarifications, derived from the
current provider transport and stream paths:

1. `parseRetryAfter` must distinguish absent, invalid, and valid values. A
   valid zero value (including a past HTTP-date) selects the header-derived
   backoff path and must not fall through to exponential backoff. The helper's
   test seam must accept a reference time so HTTP-date tests do not depend on
   wall-clock timing.
2. Numeric parsing must reject signs, decimals, malformed text, and values that
   overflow `time.Duration` before multiplying seconds by `time.Second`.
   HTTP-date parsing must use `http.ParseTime`, which covers the standard and
   obsolete HTTP-date forms named in the tests.
3. Context checks must use `errors.Is` for wrapped cancellation and deadline
   errors in every retryability helper. A retry body is non-replayable when
   `req.Body != nil` and `req.GetBody == nil`; return a controlled error before
   another transport attempt, without invoking a nil function.
4. “Both stream paths” means `ChatStream`'s direct SSE path and
   `ChatTurn`/`chatTurnStream`'s tool-capable SSE path. A pre-commit HTTP 429
   is retried by the transport in both; an HTTP-200 in-band SSE error is
   surfaced by the parser and is never replayed by stream fallback.

The existing zero-value constructor behavior is preserved: positive custom
`MaxRetries` values are honored, while a zero-valued retry option selects the
production defaults. The default changes from three to four retries; the
internal option's meaning is otherwise unchanged.

## 3. TDD implementation waves

### Wave 1 - RED tests

Create failing tests before production changes:

1. `TestRetryRoundTripper_SucceedsOnFifthAttempt`
   - Four 429 responses followed by a 200 response.
   - Assert exactly five server calls.

2. `TestRetryRoundTripper_StopsAfterFiveAttempts`
   - Persistent 429 responses.
   - Assert exactly five calls and the final 429 is returned.

3. `TestParseRetryAfter_StandardForms`
   - `0`, positive decimal seconds, RFC1123, RFC850, and ANSI-C dates.

4. `TestParseRetryAfter_RejectsMalformedAndOverflow`
   - Empty, negative, signed, decimal, non-numeric, and overflowing values.

5. `TestRetryRoundTripper_UsesReceivedRetryAfterHeader`
   - Set headers before `WriteHeader`.
   - Verify header-derived delay precedence without a long or flaky sleep.

6. `TestRetryRoundTripper_StopsForOverCapRetryAfter`
   - Preserve the existing safety contract.

7. `TestRetryRoundTripper_NonReplayableBodyReturnsError`
   - A retried body without `GetBody` must fail safely, never panic.

8. `TestRetryRoundTripper_PreservesBodyAndIdempotencyKey`
   - Every retry receives identical body bytes and the same idempotency key.
   - Separate logical requests still receive distinct keys.

9. `TestRetryRoundTripper_CancelDuring429Backoff`
   - Use a deterministic fake transport/channel, not wall-clock sleeps.
   - Preserve `errors.Is(err, context.Canceled)`.

10. z.ai and stream regressions:
    - Permanent z.ai quota/plan 429: one call.
    - Transient z.ai 429: success or exhaustion at five calls.
    - HTTP 429 before SSE commitment: retry.
    - HTTP-200 in-band SSE error: one request, propagated error, no replay.

### Wave 2 - GREEN implementation

Implement the smallest changes in `internal/provider/retry.go`. Keep the
existing `retryRoundTripper` boundary and `CompatOptions.NonRetryable` hook.
Run the focused provider tests after each production change.

### Wave 3 - review and documentation

- Run an adversarial review of retry storms, replay safety, cancellation,
  provider classification, and stream commitment.
- Update the canonical architecture documentation.
- Re-read the changed files and confirm no user-config, secret, or raw-payload
  behavior was introduced.

## 4. Verification contract

Focused gates:

```text
go test -count=1 ./internal/provider
go test -race -count=1 ./internal/provider
go vet ./internal/provider
```

Repository gates before completion:

```text
make validate-invariants
make invariants
make verify
go build ./...
```

The implementation must not claim full verification if unrelated worktree
changes prevent a broader gate from running. Never bypass hooks or verification.

## 5. Scope boundaries and residual risks

Deferred follow-ups, not blockers for this slice:

- A single logical-turn budget across stream fallback and agent-loop steps.
- Redirect/inner-transport wire-level attempt accounting.
- Aggregate concurrency admission or circuit breaking for simultaneous 429s.
- Existing simple-stream timeout/truncation and silent-stream fallback behavior.
- A typed public rate-limit error contract.
- Provider-side guarantees that `Idempotency-Key` suppresses duplicate billing;
  the client can only preserve the key across its own retries.

Rollback criterion: return to ADLC Step 0 if the five-attempt tests, cancellation
identity, z.ai permanent/transient partition, or committed-stream tests require
retry logic outside the shared provider transport or introduce a new config/API
boundary.
