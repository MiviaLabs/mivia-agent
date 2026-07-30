# 21 — Durable event timestamps, derived ordering

**Status:** PROPOSED
**Date:** 2026-07-30
**Depends on:** `19` (implemented — `list_run_events`, `fromStorageEvent`, INV-AG-11).
**Blocks:** nothing. **Composes with:** nothing in flight.
**Blast radius:** LOW-MEDIUM. Four production edits, all inside `internal/ledger`, none to a schema, none to an exported signature. The medium half is that two of them change the *meaning* of a field three surfaces already display.
**Recommendation:** BUILD. The `KNOWN LIMITATION` comment in `storage_schema.go:341-348` overstates the cost by a wide margin, and §4 establishes why.

---

## 1. The defect

`19` shipped `list_run_events`, which reports `created_at` per event (`internal/cli/ledger_tools.go:382,410`), and documented rather than fixed the fact that the value is a lie for any run this process did not create. Confirmed:

| Step | Site | What happens |
|---|---|---|
| 1 | `internal/ledger/storage.go:247` | `marshalLifecycleEvent(event)` runs on the **caller's** event |
| 2 | `internal/ledger/storage.go:252` | `newStoreEvent(...)` wraps that payload; `storage.Event` has **no timestamp field** (`internal/storage/store.go:31-37`) |
| 3 | `internal/ledger/storage.go:260` | `s.mem.AppendEvent(ctx, event)` finally stamps it |
| 4 | `internal/ledger/memory.go:202-203` | `event.Sequence = seq; event.CreatedAt = m.now()` — **unconditional** |

So the durable payload always holds `"Sequence":0` and `"CreatedAt":"0001-01-01T00:00:00Z"`. `LifecycleEvent` carries no JSON tags (`internal/ledger/types.go:194-204`), so those fields *are* in the stored JSON — present and zero, which matters in §4.

On replay, `applyStoreEventLocked` re-enters the same stamping path (`internal/ledger/storage_projection.go:149-157`), and `fromStorageEvent` recovers `Kind`/`TaskID`/`AttemptID`/`Payload` but deliberately not the other two (`internal/ledger/storage_schema.go:341-368`). Pinned green by `TestListEventsTimestampsAreReplayRelative` (`internal/ledger/storage_events_test.go:110-175`) — the broken behaviour is currently *asserted*.

### 1a. Ordering is fine. Say so plainly.

This resolves in the milder direction, and it changes the plan's whole weight class.

- `nextSequence` (`storage_projection.go:290-304`) hands out a strictly increasing per-run store sequence at append time, so **store sequence order = append order** for a given run, across all eight event kinds.
- `EventsSince` reads `ORDER BY sequence` (`internal/storage/store.go:319`), and `applyTail` re-sorts by `Sequence` before applying (`storage_projection.go:95`). `Changes` uses `rowid` only as the *cursor* (`store.go:337-376`), never as the read order.
- Therefore lifecycle events are replayed in original append order, and `mem.AppendEvent` re-derives `1..N` in that same order. **The `sequence` values `list_run_events` reports after a rebuild are byte-identical to the ones it reported in-process.** Nothing to fix.

Two narrow caveats. First, `mem`'s sequence counts *only* lifecycle events, while the store's counts all eight kinds — two different numbers called "sequence", never compared anywhere. Second, if an in-process `mem.AppendEvent` returned `ErrNotFound` (run absent from the projection), the store row still exists and replay *will* number it, so the rebuilt stream can be one event longer than the live one. Pre-existing, not a timestamp problem, and §11 does not claim to fix it.

### 1b. Is a real timestamp already persisted? Partly — and it is the wrong one.

The DDL is inline in `OpenSQLite` (`internal/storage/store.go:253-257`):

```
CREATE TABLE IF NOT EXISTS events (
  id TEXT PRIMARY KEY, run_id TEXT NOT NULL, sequence INTEGER NOT NULL,
  kind TEXT NOT NULL, payload BLOB NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(run_id, sequence)
)
```

A durable column **does** exist. It is also, for practical purposes, unreachable:

- `Append` never writes it (`store.go:285`, five columns, no `created_at`), so it is whatever `CURRENT_TIMESTAMP` produced — **UTC text, one-second granularity, no fractional part, no zone**.
- No `SELECT` in the file reads it (`store.go:305,319,344`).
- `storage.Event` has no field to hold it (`store.go:31-37`).
- `storage.Memory` — the **default** backend — has no timestamp of any kind (`store.go:76-110`), and no clock to inject one from.

The column's value is the *row insert* instant, which for an original append is within microseconds of the event instant. Semantically good enough, mechanically useless: exposing it costs an interface change, a `SELECT` change and a new clock in the memory store, and it *degrades ordering*, because `mem.ListEvents` sorts by `CreatedAt` using **unstable** `sort.Slice` (`internal/ledger/memory.go:181`). Feed second-granularity values in and every event in the same second becomes an unordered tie. See §4 B.

### 1c. The same bug at run level, on a surface nobody connected to it

`mem.CreateRun` overwrites `rec.snapshot.CreatedAt = m.now()` unconditionally (`internal/ledger/memory.go:89`) — after the caller set a real value (`internal/coordinator/spawn.go:46`) and after `marshalRunSnapshot` persisted it (`storage.go:169`). Replay re-enters `mem.CreateRun` (`storage_projection.go:130`). So **run** `created_at` is replay-relative too, and that one reaches `mivia diagnostics`: `Diagnostics.ListRuns` sorts by it and computes `Elapsed: time.Since(r.CreatedAt)` (`internal/cli/diagnostics.go:51-66`). A run recovered from disk reports an age of a few milliseconds.

`TaskSnapshot.CreatedAt` is *not* overwritten (`memory.go:152` clones as-is), so task timestamps already survive replay. The asymmetry is accidental.

### Who is actually harmed

| Surface | Field | Harmed? |
|---|---|---|
| `list_run_events` (`ledger_tools.go:410`) | event `created_at` | **Yes** — the headline case |
| `mivia diagnostics` (`diagnostics.go:53,64`) | run `CreatedAt`, `Elapsed` | **Yes** (§1c), for recovered runs |
| `inspect_agents` (`internal/cli/orchestrate.go:296`) | — | No. Reports status/counts, no timestamp |
| TUI run dashboard (`tui_run_dashboard.go:167,177`) | run `CreatedAt` | No. Fed by live events stamped `time.Now()`, never from the repo |
| `RebuildProjection` (`storage_schema.go:164`) | event `CreatedAt` | Differently: it appends `fromStorageEvent` output directly, so it returns **zero** timestamps, not replay-now. Two replay implementations, two wrong answers. **No production callers** |

> A reader of a recovered run cannot tell when anything happened. Measured in `19`: reopening the same file 1.1 s later moved every event forward by 1.1 s. The ordering is right, the clock is fiction.

## 2. What this plan is not

Not a durability change. No event is lost or gained; no schema is touched; `sequence` is not made durable (§1a says it does not need to be). It is the narrow claim that a field already persisted-as-zero should be persisted-as-populated, and that the projection should stop clobbering values it was handed.

## 3. Invariant to establish

> A recorded timestamp is when the thing happened, not when it was last read back.

Corollaries:

- **The projection stamps only what arrives unstamped.** A non-zero `CreatedAt` reaching a repository is data, not a suggestion.
- **Stamp before you serialise.** A value assigned after marshalling is a value that was never durable. `storage.go:247` before `:260` is the defect's shape.
- **Order comes from `sequence`, never from a timestamp.** Two events may legally share a wall-clock instant (coarse clock, injected test clock); an ordering that degrades when they do is not an ordering.

The third corollary is not decoration. Today `mem.ListEvents` orders by `CreatedAt` through an unstable sort (`memory.go:181`); it *appears* to work only because `time.Now()` has nanosecond resolution and every event gets a fresh call. The moment §5 makes timestamps durable — or a test injects a fixed clock — ties become reachable and the exposed order becomes non-deterministic. Fixing the timestamp without fixing the sort trades a wrong `created_at` for a scrambled `sequence`: strictly worse than doing nothing.

**Both backends.** The memory backend (the default, `internal/config/load.go:46-48`) never replays, so the invariant holds there vacuously for events — but §1c's run-level clobber is a *memory-backend* defect that the storage backend merely inherits, and corollary 1 is enforced in `memory.go` where every path passes through it. The invariant is not backend-specific.

## 4. Options

Each option is judged against the two things `mem.AppendEvent` currently owns — **dedup by event ID** (`memory.go:197-201`) and **per-run sequence assignment** (`memory.go:202-204`) — because disturbing either is the real cost.

### A. Stamp the event before marshalling; teach the projection not to re-stamp

`StorageLedgerRepository.AppendEvent` sets `event.CreatedAt` from its own injected clock *before* `marshalLifecycleEvent`. `mem.AppendEvent` becomes `if event.CreatedAt.IsZero() { … }`. `fromStorageEvent` copies the decoded `CreatedAt` through.

- **Dedup by ID:** untouched. **Sequence assignment:** untouched — §1a proves the derived values already round-trip exactly.
- **Schema:** none. The payload already has a `CreatedAt` slot; this fills it.
- **Legacy rows:** decode yields zero → the `IsZero` branch stamps replay-now → **today's behaviour, unchanged**. Graceful, no version check.
- **Cost:** three edits of ≤3 lines each, plus the §3-corollary-3 sort change.

### B. Add a durable field to `storage.Event` and have the projection trust it

- **Dedup / sequence:** untouched.
- **Schema:** none *structurally* — the column exists (§1b) — but the value semantics change from row-insert-default to writer-supplied, and rows written by every prior version keep second-granularity UTC text with no zone.
- **Cost:** widens the `Store` interface (an exported cross-package contract, two implementations, both `Events` and `EventsSince` queries, a new clock and clock injector on `storage.Memory`), for a value that is *worse* than A's: coarser, tie-prone (§1b), and describing row insertion rather than the event.
- **Only advantage:** it could partially backfill runs already on disk, to the nearest second. Real, and not worth the interface.

### C. Bypass `mem.AppendEvent` on the replay path only

- **Dedup by ID:** the replay path must reimplement it, or lose it. Losing it is not hypothetical: `applyStoreEventLocked` currently *relies* on `ErrDuplicate` and `ErrNotFound` being returned and swallowed (`storage_projection.go:154-156`).
- **Sequence assignment:** must also be reimplemented, and must reproduce §1a's numbering exactly or `sequence` starts diverging across a rebuild — converting a timestamp bug into an ordering bug.
- **Cost:** duplicates both invariants `mem` owns, in a second place, under `s.mem.mu` held by the caller. This is the change the `KNOWN LIMITATION` comment imagines and calls "larger than this one". It is right about C and wrong to conclude C is the only shape available.

### D. Accept and document (status quo)

Already done: `storage_schema.go:341-348`, `docs/product/agent.md:101-103`, INV-AG-11, and a passing test that asserts the wrong answer.

- **Against:** the documented reason ("means bypassing the projection's dedup and sequencing") describes option C and is not true of option A. The limitation is documented on the strength of an analysis that did not consider stamping earlier. Leaving it means knowingly shipping `created_at` on two agent-visible surfaces where it is fiction, having established the fix is ~10 production lines.

**DECIDED: A.** The only option that changes neither of `mem.AppendEvent`'s two responsibilities — it changes an *ordering of statements* in one function and adds one `IsZero` guard in another. B pays an exported-interface cost for a strictly worse value. C pays for reimplementing dedup and sequencing to fix neither. D is defensible only under the cost model A refutes. A also makes `RebuildProjection` correct for free, since it reads the payload directly.

**On the storage-row-ID wrinkle.** `newStoreEvent` mints its own row ID (`storage.go:149`, `"se-N"`) rather than reusing `event.ID`, and `fromStorageEvent` sets `l.ID = evt.ID` — so **`list_run_events` reports a different event `id` before and after a rebuild**, and `mem`'s dedup-by-ID cannot recognise a replayed event as one it already appended.

A **fixes** this in the same one-line idiom: the decoded payload holds the original `ID`. Two consequences, both good. Replayed `id` values match live ones. And dedup-by-ID becomes meaningful on the replay path — today a caller who appends two events with the same `ID` gets `ErrDuplicate` from `mem` while the store row is written anyway, so a rebuild resurrects an event the live run never showed; with the ID restored, `mem` rejects it again and the projection swallows it. Nothing depends on current behaviour: `advanceStorageEventIDCounter` parses `evt.ID` (the storage event), not `l.ID`.

Not fixed by A: the row ID and the event ID remain two different identifiers. Unifying them would put a caller-controlled string in a `PRIMARY KEY` the store dedups on, changing duplicate-append semantics for every kind. Out of scope; §11 keeps it out.

## 5. Design

Five production edits, all `internal/ledger`.

**5.1 — Stamp before marshalling** (`storage.go:242-261`). Insert before `:247`:

```go
if event.CreatedAt.IsZero() {
	s.mu.RLock()
	event.CreatedAt = s.nowLocked()
	s.mu.RUnlock()
}
```

`event` is a value parameter, so this mutates the local copy that both `marshalLifecycleEvent` and `s.mem.AppendEvent` receive — the stored payload and the live projection carry the *same* instant by construction, not by coincidence. The lock idiom matches `CompareAndSetTaskStatus` (`storage.go:284-287`). `s.now` is already injectable via `SetTimeSource` (`storage.go:70-75`), which also forwards to `s.mem` — §8's determinism requirement is satisfied by existing code.

**5.2 — Stop clobbering a supplied event timestamp** (`memory.go:203`). `event.CreatedAt = m.now()` becomes `if event.CreatedAt.IsZero() { … }`. Sequence assignment untouched.

**5.3 — Stop clobbering a supplied run timestamp** (`memory.go:89`). Same guard. This is §1c, and it makes `mivia diagnostics` report a real age. It also makes `internal/cli/diagnostics_test.go:23` — which stakes out three runs an hour apart and is currently flattened to three equal `time.Now()` values — actually test the sort it appears to test.

**5.4 — Recover `CreatedAt` and `ID` on replay** (`storage_schema.go:349-368`). In the `decodeMarshalledLifecycleEvent` branch, add `l.CreatedAt = decoded.CreatedAt` (guarded on non-zero, so legacy rows fall through to 5.2's stamp) and `l.ID = decoded.ID` (guarded on non-empty). Rewrite the `KNOWN LIMITATION` block to state what is now durable, what is derived (`Sequence`, per §1a), and that rows written before this change still replay to the read instant.

**5.5 — Order by `sequence`, not by clock** (`memory.go:181`). Replace the `CreatedAt.Before` comparison with `out[i].Sequence < out[j].Sequence`. §3 corollary 3. This keeps 5.1–5.4 from being a net regression, and is a correctness fix on its own terms: `repository.go:57` already documents `ListEvents` as "ordered by sequence", which the implementation does not do.

**Delete `toStorageEvent`** (`storage_schema.go:321-330`) — zero callers anywhere including tests, and its doc comment describes an event/row ID identity that `newStoreEvent` does not implement. It is the misleading artefact behind the §4 wrinkle.

## 6. Blast radius and migration

**No schema migration, and no mechanism to run one with.** Confirmed: the DDL is inline `CREATE TABLE IF NOT EXISTS` in `OpenSQLite` (`internal/storage/store.go:253-275`); there is no schema file, no version table, no migration runner, no `PRAGMA user_version`. `19` §2 recorded the same finding. If this plan needed a migration, the migration mechanism would be the plan.

It does not, and that is the decisive argument for A over B:

| Database written by | After this change |
|---|---|
| Current or earlier version | Payload `CreatedAt` is zero → 5.4's guard declines it → 5.2 stamps replay-now → **exactly today's behaviour**. No error, no crash, no version check |
| This version, read by an older binary | Older `fromStorageEvent` ignores the field it does not read. Forward-compatible |
| This version, read by this version | Original instant preserved |

No backfill is offered. `events.created_at` could supply one to the nearest second (§1b), but reading it requires B's interface change, and a coarse value silently mixed with nanosecond ones is a worse artefact than an honest "this run predates durable timestamps".

Blast radius by package: `internal/ledger` (5 edits), `internal/cli` (docs only), `.mivia/` (invariants + Makefile). No exported signature changes. `internal/storage` untouched.

## 7. Changes

| # | File | Change |
|---|---|---|
| 1 | `internal/ledger/storage.go:242-261` | 5.1 — stamp `event.CreatedAt` when zero, **before** `marshalLifecycleEvent` |
| 2 | `internal/ledger/memory.go:203` | 5.2 — stamp event `CreatedAt` only when zero |
| 3 | `internal/ledger/memory.go:89` | 5.3 — stamp run `CreatedAt` only when zero (§1c) |
| 4 | `internal/ledger/memory.go:181` | 5.5 — `ListEvents` orders by `Sequence` |
| 5 | `internal/ledger/storage_schema.go:349-368` | 5.4 — recover `CreatedAt` and `ID`; rewrite the limitation comment |
| 6 | `internal/ledger/storage_schema.go:321-330` | Delete dead, misleading `toStorageEvent` |
| 7 | `internal/ledger/storage_events_test.go:99-175` | **Invert** `TestListEventsTimestampsAreReplayRelative` → `TestListEventsPreserveOriginalTimestampAcrossRebuild` |
| 8 | `internal/ledger/storage_events_test.go` (append) | New tests per §9 |
| 9 | `internal/ledger/memory_test.go` | Memory-backend tests per §9 |
| 10 | `internal/ledger/storage_catchup_test.go:283-284` | `projectionState` currently excludes `CreatedAt` *because* of this defect. Include it |
| 11 | `.mivia/invariants.md` INV-AG-11 | Amended text per §12 |
| 12 | `Makefile:131` | Replace the pinning test name with the inverted one; add the new names |
| 13 | `docs/product/agent.md:101-103` | Replace the replay-instant caveat with the real contract |

No new exported symbols. The only signature-adjacent statement is negative and load-bearing:

```go
// Unchanged, deliberately. Sequence remains derived at replay; §1a proves the
// derived values are identical to the live ones.
ListEvents(ctx context.Context, runID string) ([]LifecycleEvent, error)
```

**Structure gate.** `memory.go` 465 → ~469 (soft 500). `storage.go` 458 → ~463. `storage_schema.go` 388 → ~380 after the §5 deletion — it shrinks. `storage_events_test.go` 238 → ~400 (testSoft 800). Nothing goes into `storage_test.go` (724). No function approaches 80 lines.

## 8. Constraints check

- **Memory backend is the default.** Changes #2, #3, #4 are *in* it; #1 and #5 are the storage backend's use of it. §9 tests both.
- **No wall-clock sleeps.** `SetTimeSource` exists on both repositories (`memory.go:57`, `storage.go:70`) and the storage one forwards to `mem` (`storage.go:74`). Every §9 test injects a fixed or stepping clock. The inverted test becomes *stronger* under injection: exact equality instead of `19`'s inequality-with-slop.
- **`--strict`:** budgets in §7. No `TODO`/`FIXME`/`HACK`/`XXX`; no `panic` added.
- **`storage_test.go`:** untouched.

## 9. Verification

```bash
go build ./... && go vet ./...
go test ./internal/ledger/ ./internal/cli/ ./internal/storage/ -race -count=1
go test ./internal/... ./cmd/... -race
make verify && make invariants
```

**Tests.**

- `TestListEventsPreserveOriginalTimestampAcrossRebuild` — **the load-bearing one**, #7's replacement. Append under an injected clock at T0, close, reopen the same file under a clock at T1, assert the replayed `CreatedAt` is **exactly** T0. Fails today by construction, since today's assertion is the negation.
- `TestListEventsPreserveEventIDAcrossRebuild` — the §4 wrinkle. Replayed `ID` equals the caller's, not `"se-N"`.
- `TestAppendEventStampsBeforeMarshalling` — reads the row payload back through the store and asserts the JSON `CreatedAt` is non-zero and equals what `ListEvents` reported live. Pins §3 corollary 2 structurally: reordering `storage.go:247` and `:260` fails it even if the rebuild test were satisfied another way.
- `TestListEventsOrderedBySequenceUnderTiedTimestamps` — fixed clock, several events, assert `sequence` is `1..N` in order. Guards §3 corollary 3 and the unstable-sort trap; fails today.
- `TestLegacyRowWithoutTimestampFallsBackToReadInstant` — a row whose payload has a zero `CreatedAt` still yields a non-zero timestamp and no error. §6's graceful-degradation claim, tested rather than asserted in prose.
- `TestMemoryCreateRunPreservesSuppliedCreatedAt` — §1c on the default backend.
- `TestMemoryAppendEventStampsOnlyUnstampedEvents` — §3 corollary 1 on the default backend.
- `TestProjectionStateIncludesTimestampsAcrossRebuild` — change #10.

**Mutation proofs.**

| # | Mutation | Test that MUST fail |
|---|---|---|
| 1 | Move the `CreatedAt` stamp back after `marshalLifecycleEvent` | `TestAppendEventStampsBeforeMarshalling` |
| 2 | Restore unconditional `event.CreatedAt = m.now()` | `TestListEventsPreserveOriginalTimestampAcrossRebuild` |
| 3 | Drop `l.CreatedAt = decoded.CreatedAt` | `TestListEventsPreserveOriginalTimestampAcrossRebuild` |
| 4 | Drop `l.ID = decoded.ID` | `TestListEventsPreserveEventIDAcrossRebuild` |
| 5 | Restore unconditional `rec.snapshot.CreatedAt = m.now()` | `TestMemoryCreateRunPreservesSuppliedCreatedAt` |
| 6 | Revert `ListEvents` to sorting by `CreatedAt` | `TestListEventsOrderedBySequenceUnderTiedTimestamps` |
| 7 | Make `fromStorageEvent` trust a zero decoded `CreatedAt` verbatim | `TestLegacyRowWithoutTimestampFallsBackToReadInstant` |
| 8 | Have the replay path reassign `Sequence` from a fresh counter | `TestProjectionStateIncludesTimestampsAcrossRebuild` |

Mutations 1–3 are the regression proofs for §1 and must be recorded with `Regression: INV-AG-11`.

**The pinning test must be inverted, not deleted.** `19` left `TestListEventsTimestampsAreReplayRelative` asserting `!got.CreatedAt.Equal(original)` with a comment saying it "is expected to fail and should be inverted" if the timestamp becomes durable. Honouring that: the assertion direction flips, the name becomes `TestListEventsPreserveOriginalTimestampAcrossRebuild`, the slop-tolerant pair collapses into one exact `Equal` under an injected clock, and `Makefile:131` is updated in the same commit. Deleting it would silently retire the only regression guard on the defect.

## 10. Implementation waves

Per `.mivia/rules/05` Step 1: one file per task, test before production, reviewer every 2–3 tasks.

**Wave 1 — the default backend** (no storage path yet; independently shippable)
1. `internal/ledger/memory_test.go` — `TestMemoryCreateRunPreservesSuppliedCreatedAt`, `TestMemoryAppendEventStampsOnlyUnstampedEvents`, `TestListEventsOrderedBySequenceUnderTiedTimestamps` (RED).
2. `internal/ledger/memory.go` — changes #2, #3, #4.
   *Reviewer checkpoint.*

**Wave 2 — make the durable copy real**
3. `internal/ledger/storage_events_test.go` — `TestAppendEventStampsBeforeMarshalling` (RED).
4. `internal/ledger/storage.go` — change #1.
5. `internal/ledger/storage_events_test.go` — invert #7; add `TestListEventsPreserveEventIDAcrossRebuild` and `TestLegacyRowWithoutTimestampFallsBackToReadInstant` (RED).
   *Reviewer checkpoint.*

**Wave 3 — the replay path**
6. `internal/ledger/storage_schema.go` — changes #5 and #6.
7. `internal/ledger/storage_catchup_test.go` — change #10.
   *Reviewer checkpoint.*

**Wave 4 — close the loop**
8. `.mivia/invariants.md` + `Makefile:131` — changes #11, #12.
9. `docs/product/agent.md` — change #13.

Wave 1 stands alone and is worth landing alone: it fixes `mivia diagnostics` (§1c) and the `ListEvents` ordering contract with no dependency on the storage path. Waves 2–3 are indivisible — landing #1 without #5 stores a timestamp nobody reads; landing #5 without #1 reads a zero.

## 11. What this does NOT solve

- **Runs already on disk stay replay-relative, permanently.** Their payloads hold a zero. `events.created_at` could backfill them to the nearest second; §6 declines to, and §14 is the escape hatch if that matters.
- **`sequence` remains derived, not durable.** §1a shows the derived values are identical today. If replay order ever stopped matching append order, `sequence` would silently drift and mutation #8 is the only guard — a projection-equality test, not a proof.
- **The event ID and the storage row ID stay two identifiers.** A fixes which one gets *reported*, not unifying them.
- **The timestamp is the writing process's wall clock.** Two processes sharing a workspace with skewed clocks can produce timestamps that disagree with `sequence`. §3 corollary 3 makes that harmless for ordering, and it means a history reader must not compute durations across writers.
- **An event whose `mem.AppendEvent` failed in-process still appears after replay** (§1a). Pre-existing; not touched.
- **`RebuildProjection` still has no production callers.** Change #5 makes it correct, which does not make it used. Two replay implementations remain.
- **Nothing about redaction, retention or size.** A durable timestamp is one more true fact about a run, on surfaces already gated by INV-AG-9. No new exposure.

## 12. Invariant registration

Current row (`.mivia/invariants.md`):

> | INV-AG-11 | Safety | Recorded execution history survives a projection rebuild: a replayed lifecycle event keeps its kind, task and attempt, so a kind filter cannot silently return zero rows for a run the process did not create. `created_at` is replay-relative — a known limitation, pinned by test rather than left to be rediscovered | `TestListEventsRestoresKindAfterProjectionRebuild`, `TestListEventsToleratesUndecodablePayload`, `TestListEventsTimestampsAreReplayRelative` | 2026-07-30 (plan 19 implemented) |

Amended text:

> | INV-AG-11 | Safety | Recorded execution history survives a projection rebuild: a replayed lifecycle event keeps its id, kind, task, attempt and original `created_at`, so a kind filter cannot silently return zero rows and a timestamp is when the event happened, not when it was read back. The projection stamps only what arrives unstamped, at run level as well as event level; `sequence` is derived at replay and is identical to the live value because replay order is append order; event order is taken from `sequence`, never from a timestamp, so tied clocks cannot scramble it. Events recorded before plan 21 hold no durable timestamp and still replay to the read instant | `TestListEventsRestoresKindAfterProjectionRebuild`, `TestListEventsToleratesUndecodablePayload`, `TestListEventsPreserveOriginalTimestampAcrossRebuild`, `TestListEventsPreserveEventIDAcrossRebuild`, `TestAppendEventStampsBeforeMarshalling`, `TestListEventsOrderedBySequenceUnderTiedTimestamps`, `TestLegacyRowWithoutTimestampFallsBackToReadInstant`, `TestMemoryCreateRunPreservesSuppliedCreatedAt`, `TestMemoryAppendEventStampsOnlyUnstampedEvents`, `TestProjectionStateIncludesTimestampsAcrossRebuild` | 2026-07-30 (plan 21) |

`make validate-invariants` requires every named test to exist, so changes #11 and #12 land with Wave 4's code, not before it.

## 13. Plan scorecard

| Criterion | Verdict |
|---|---|
| Compiles (no import cycles) | PASS — no new imports; all edits inside `internal/ledger` |
| No breaking API change | PASS — no exported signature changes. `ListEvents` **order** changes from clock-derived to `sequence`-derived, which is what `repository.go:57` already documents |
| Testable in isolation | PASS — both repositories expose `SetTimeSource`; no sleeps, no real clock |
| Backward-compatible config | PASS — no config surface touched |
| Backward-compatible data | PASS — §6. Zero-timestamp rows degrade to current behaviour; no migration, no version gate |
| Every function has a test | PASS — Waves 1–3 pair each production task with a preceding test task |
| Security tests present | N/A by construction — no new surface, no new exposure (§11) |
| Rule `60` satisfied | N/A — no tool name, description or schema changes |
| Structure gate | PASS — §7; `storage_schema.go` net shrinks |
| Cost proportionate to harm | PASS — ~10 production lines against two agent- and operator-visible fields that are currently fiction |

## 14. Rollback criterion

Kill or reduce this plan if:

- **Change #4 (`ListEvents` orders by `sequence`) cannot land.** Then #1–#3 and #5 must not land either. Making timestamps durable while ordering still derives from them makes ties reachable and the exposed order non-deterministic (§3 corollary 3) — a worse defect than the one being fixed. Revert to D and say so in `docs/product/agent.md`.
- **A durable `Sequence` turns out to be required.** That is option C's cost centre and its own plan. Do not smuggle it into #5 by trusting a payload `Sequence`; a caller-supplied sequence can open gaps and duplicates in the projection.
- **Backfilling pre-plan-21 runs becomes a real requirement.** Then B's interface change is justified for the legacy path only, as a separate plan — with the second-granularity mixing problem (§1b) solved explicitly.
- **§1a turns out to be wrong** — a case is found where replay order is not append order. Then this plan is the wrong plan: ordering would be the defect, and a wrong timestamp on a correctly ordered stream is the lesser problem to fix second.

In the first case, land nothing. In the others, land Wave 1 — the run-level fix (§1c) and the ordering contract (#4) are independent of the storage path and independently correct.
