# 30 - Bound source work for ledger-read paging

**Status:** DESIGN-READY - implementation must pass ADLC Step 0 before code is
written.
**Date:** 2026-07-31
**Depends on:** `19` (execution-history tools) and the shipped bounded
`ledger_read` page contract.
**Blocks:** nothing.
**Blast radius:** HIGH - recorded sub-agent output is untrusted data, redaction is
configured by users, and this changes the execution-history storage boundary.

---

## 1. Problem

`ledger_read` now returns bounded, cap-safe pages, but every request still calls
`LoadContent`, normalizes the whole recorded value, applies redaction to the whole
value, and then selects one page. A large stored output therefore costs memory and
CPU proportional to its complete size on every page request.

This is deliberately not solved by a raw `LoadContentRange(offset, limit)` method.
Configured redaction expressions may match across a page boundary, and redaction
changes the byte positions in the model-visible stream. Slicing first can disclose a
secret prefix or make the next cursor skip or repeat content.

The invariant to preserve is:

> Every byte delivered to a model is from the complete redacted representation; no
> continuation may weaken untrusted-data framing, return invalid JSON, or exceed its
> result cap.

## 2. Goals and non-goals

### Goals

- Bound model-visible pages and their complete JSON envelopes, as `ledger_read`
  already does.
- Avoid reloading and re-redacting an entire record for every sequential page.
- Bound memory, CPU, cursor count, cursor lifetime, and concurrent source reads.
- Preserve the framing-first, `content`-last response order.
- Keep raw recorded content out of logs, test fixtures, and persistent cache files.

### Non-goals

- Do not weaken or bypass configured redaction.
- Do not make execution-history references an authorization boundary.
- Do not introduce freeform storage queries.
- Do not persist a redacted-output cache by default.
- Do not claim random access in redacted content when the configured pattern set
  cannot make it safe.

## 3. Decisions required before implementation

### A. Redaction strategy - required decision

Arbitrary Go regular expressions are not generally streamable with a finite overlap:
an unbounded expression can begin in one chunk and finish arbitrarily far later. A
fixed overlap would silently miss it and can leak data.

Choose one, explicitly:

1. **Bounded pattern subset (recommended for true streaming).** Accept streaming
   only for a documented redaction-pattern subset with a statically known maximum
   match width. Retain the required overlap between chunks. Reject unsupported
   streaming patterns at config load with a safe diagnostic.
2. **Ephemeral complete redacted snapshot.** Materialize once per `{ref, policy
   version}` into a bounded in-memory cache, then serve subsequent pages from the
   snapshot. This removes repeated work but does not bound the first request's memory.
3. **Keep materialization with an explicit source-work refusal.** Add a configured
   maximum source size/scan budget and refuse a record that exceeds it. This is safer
   than exhaustion but cannot page extremely large records.

Do not implement raw ranged reads alone. They are unsound under the existing
redaction contract.

### B. Continuation contract - recommended decision

Keep `offset` for the existing stateless page contract. Add an optional opaque
server-owned `cursor` for efficient sequential reads:

- Initial call: `ref`, optional `limit`; response contains `cursor`,
  `next_offset`, and `has_more` when more data remains.
- Follow-up: same `ref`, returned `cursor`, and optional smaller `limit`.
- Cursor carries only server-side identity; it must be unguessable, scoped to its
  source reference and redaction-policy version, expire quickly, and never encode
  recorded content.
- A missing, expired, mismatched, or exhausted cursor returns a bounded typed status
  that tells the caller to restart from `offset: 0`; it must not fall back to a raw
  range silently.

`offset` requests without a cursor remain correct but may consume a bounded scan
budget. The documentation must state that `next_offset` is a cursor into the
redacted UTF-8 stream and should be copied verbatim.

### C. Resource policy

Use explicit defaults, all testable:

| Resource | Required policy |
|---|---|
| Active cursors per dispatcher/session | Small fixed cap; reject excess rather than evict a live cursor |
| Cursor idle lifetime | Short TTL, reset only by a successful continuation |
| Bytes scanned per request | Finite cap, including discarded bytes before a stateless offset |
| Concurrent readers | Bounded semaphore shared by ledger reads |
| In-memory buffer | Fixed chunk size plus redaction overlap only |
| Failure output | Fixed, bounded messages; never echo cursor, unknown field, raw value, or content |

No new configuration key lands without an owner, a documented default, validation,
and a cap-enforcement test.

## 4. Architecture

Introduce a narrow read-side streaming capability at the ledger boundary rather than
leaking storage handles into `internal/cli`:

```text
ledger_read tool
  -> cursor manager (session-owned, bounded)
  -> redacting page reader
  -> LedgerRepository content reader
  -> memory or SQLite backend
```

- `LedgerRepository` exposes a read-only content stream or chunk iterator with the
  original byte length. It never exposes SQL, paths, or backend connections.
- The memory backend supplies defensive chunks; SQLite uses a bounded blob/row reader
  or a prepared chunk query. Both honour `context.Context`.
- The redacting page reader owns normalization, approved bounded-pattern overlap, and
  UTF-8 cursor accounting. It emits page bytes only after redaction.
- The cursor manager is owned by the session dispatcher, not global process state. It
  closes readers on expiry, dispatcher close, cancellation, and normal completion.
- `ledgerReadPayload` stays a struct. Status, reference, size, cursor and all
  untrusted-data framing fields precede `Content`, which remains the physical last
  field.

No interface is added until Step 0 proves both shipped backends can implement it
without reversing `internal/cli -> internal/ledger` ownership or creating a test-only
import cycle.

## 5. Implementation waves

Every production task follows a compiling RED test that fails an assertion before
the implementation is added.

| Wave | Scope | Required proof |
|---|---|---|
| 0 | Challenge the chosen redaction strategy; inspect all `LedgerRepository` implementations and callers | Architecture, security, and correctness reviews dispositioned; no unsound raw-range plan |
| 1 | Ledger read-stream interface plus memory backend | Chunk ordering, defensive-copy behaviour, cancellation, EOF, and original-length tests |
| 2 | SQLite implementation and storage tests | Bounded retrieval, no SQL surface, cancellation, reopen/recovery compatibility |
| 3 | Redacting page reader | Cross-chunk redaction, UTF-8 boundaries, invalid source bytes, page reconstruction, and cap-fitting tests |
| 4 | Session-owned cursor manager | TTL, cap, cancellation, dispatcher close, policy-version mismatch, and no-content-in-token tests |
| 5 | `ledger_read` schema/handler/docs | Cursor and offset compatibility, framing order, malformed/duplicate input, bounded failures, and nested-loop 1024-byte-cap integration |
| 6 | Audit and rollout | Race tests, backend integration tests, Semgrep/secret scan, owned docs, and hostile audit with zero confirmed bugs |

## 6. Security acceptance criteria

- A redaction match spanning every possible chunk/page seam is never partially
  exposed.
- Returned `content` is never used as instructions and is always preceded by the
  existing untrusted-data framing.
- Every successful and failure response reaching the agent loop is valid JSON and
  within the configured tool-result ceiling.
- Unknown, duplicate, oversized, malformed, null, fractional, and expired cursor
  inputs are rejected with bounded errors that contain no supplied key or value.
- Cursor tokens are opaque, unguessable, session-owned, short-lived, and absent from
  logs, previews, fixtures, and error messages.
- Memory and SQLite implementations return identical page semantics.

## 7. Verification

Minimum gates after implementation:

```text
go test ./internal/ledger ./internal/storage ./internal/cli -count=1
go test -race ./internal/ledger ./internal/storage ./internal/cli -count=1
go vet ./...
go build ./...
make invariants
make verify
```

Add a bounded fuzz target for cursor and page parameter decoding. Its corpus must use
synthetic data only; no recorded task output, prompts, or secrets belong in fixtures.

## 8. Rollback

The streaming cursor path must be independently disableable. Rollback returns to the
current stateless, fully redacted bounded-page implementation; it must never switch
to raw-range paging or omit framing. Expired cursors remain harmless failures after
rollback.
