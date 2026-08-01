# Plan 34 - Implementation overview

Parent plan: `../34-provider-retry-improvements.md`

Status: ✅ Implemented 2026-08-01. Every item in the cross-phase acceptance
contract below is covered by a test in `internal/provider`.

Goal: make the shared OpenAI-compatible provider transport perform five total
attempts for retryable exchanges, honor bounded `Retry-After` delays safely,
preserve request replay and cancellation guarantees, and retain z.ai's
permanent-versus-transient 429 distinction.

Required order:

1. Land phase 01 before changing retry production behavior.
2. Land phase 02 after phase 01; it owns parser validity, backoff precedence,
   replay safety, and cancellation.
3. Land phase 03 after the transport contract is green; it owns z.ai exact
   attempt counts and must not change the classifier's privacy policy.
4. Land phase 04 after phases 02-03; it proves both stream entry paths and the
   commitment boundary.
5. Land phase 05 last; update owned architecture documentation and run the
   repository verification ladder.

Cross-phase acceptance contract:

- Four retryable 429 responses produce exactly five transport calls.
- A valid `Retry-After: 0` is honored as zero delay, not treated as absent;
  invalid or overflowing values use exponential jitter.
- HTTP-dates use `http.ParseTime` against an injectable/reference time and are
  bounded by `MaxDelay`.
- Wrapped context cancellation and deadline errors are not retried.
- A request body without `GetBody` fails safely before replay.
- Permanent known z.ai quota/plan 429 responses make one attempt; transient
  429 responses use the shared budget.
- HTTP 429 before SSE commitment may be retried; HTTP-200 in-band SSE errors
  are returned once and are never replayed by fallback.

Non-goals: user-configurable retry settings, coordinator retries, a logical-turn
retry budget, circuit breaking, typed public rate-limit errors, and changes to
provider factories.
