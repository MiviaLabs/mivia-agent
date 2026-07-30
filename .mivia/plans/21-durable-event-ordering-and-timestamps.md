# 21 — Durable event timestamps, derived ordering

**Status:** VALIDATED 2026-07-30 — **BUILD, REDUCED SCOPE.** Two of the plan's
changes are withdrawn (see corrections C1 and C2 below); one of them would not
have compiled and the other is a reachable regression.
**Date:** 2026-07-30
**Depends on:** `19` (implemented — `list_run_events`, `fromStorageEvent`, INV-AG-11).
**Blocks:** nothing. **Composes with:** nothing in flight.
**Blast radius:** LOW. Three production edits after the withdrawals, all inside `internal/ledger`, none to a schema, none to an exported signature. The medium half of the original estimate was change #4, which is withdrawn.
**Recommendation:** BUILD. The `KNOWN LIMITATION` comment in `storage_schema.go:341-348` overstates the cost by a wide margin, and §4 establishes why.

---

## Corrections found during validation

Every empirical claim in this plan was independently re-verified against the
code. Most held. These did not. Where a citation drifted by a few lines it is
noted at the end rather than inline.

- **C1 — §3 corollary 3, §5.5 and change #4 rest on a misread. `ListEvents` does
  not sort at all, and change #4 as written would not compile.**
  `memory.go:181` is inside **`ListTasks`** (`memory.go:170-183`) and sorts
  `[]TaskSnapshot`. `ListEvents` is `memory.go:209-221` and contains **no sort**:
  it copies `rec.events` in append order. Append order *is* sequence order —
  `AppendEvent` assigns `seq` then appends (`memory.go:201-205`) — so the
  implementation already satisfies `repository.go:57` ("ordered by sequence").
  Measured under a fixed clock, six events with byte-identical timestamps came
  back `seq=1..6` in order. Consequences: there is no unstable-sort trap; change
  #4's replacement expression `out[i].Sequence < out[j].Sequence` would fail to
  compile because `TaskSnapshot` has no `Sequence` field; §5.5's "which the
  implementation does not do" is false; and the coupling argument that gave this
  plan its urgency — that durable timestamps without #4 are a *net regression* —
  dissolves. **Change #4 is withdrawn.** So is §14's first rollback criterion,
  which rests on the same false premise.
  *The real unstable `CreatedAt` sorts are over tasks, not events:*
  `memory.go:181`, `memory.go:357`, `storage_schema.go:253`. Task ties are
  genuinely reachable — `createTasks` receives one `now` for the whole batch
  (`spawn.go:78`) — so task order really is non-deterministic today. That is a
  real defect, at a different level, not created by this plan, and out of scope.

- **C2 — §4's "storage-row-ID wrinkle" is a regression, not a fix. `l.ID =
  decoded.ID` is withdrawn.**
  `newEventID()` is a **process-local** `atomic.Uint64` — `evt-1`, `evt-2`, …,
  reset on every process start (`internal/coordinator/types.go:167-169`), unlike
  `newRunID()` which is crypto-random (`types.go:161-165`). Restoring the caller
  ID on replay therefore makes a resumed run's own events collide with the ones
  just replayed. Verified end-to-end over a real SQLite file with the change
  applied:
  ```
  PROBE replayed IDs = [evt-1 evt-2]
  PROBE resume append of evt-1 => duplicate record
  PROBE events after resume = 2 (want 3)
  ```
  `markInterruptedTasks` (`recovery.go:193`) is exactly that call — it mints
  `newEventID()` against a run replayed from disk. The store row is still written
  (store dedup keys on `se-N`), so the event becomes permanently invisible to
  `ListEvents`; and `transitionTaskToStatus` (`dag.go:252-254`) *returns* the
  error, so `startReady` (`dag.go:142-150`) marks the task failed. There is no
  `advanceEventIDCounter` analogous to `advanceStorageEventIDCounter`
  (`storage.go:443-455`), and the counter lives in `internal/coordinator`, which
  `internal/ledger` cannot import. Making `newEventID()` unguessable is the
  prerequisite for ever restoring the ID; that is its own change.
  **Withdrawn with it:** `TestListEventsPreserveEventIDAcrossRebuild` and
  mutation proof #4.
  This also means **§4's headline criterion is not satisfied as §4 scopes option
  A.** A-minus-the-ID-half touches neither dedup-by-ID nor sequence assignment;
  A as written changes dedup-by-ID behaviour, and for the worse.

- **C3 — §1a is right for a serial writer and wrong as an unconditional claim,
  but the error points the safe way.**
  Measured serial round-trip: live `[evt-1/seq=1 … evt-5/seq=5]` → replay
  `[se-3/seq=1 … se-11/seq=5]`. Sequences identical, interleaved with four other
  store-event kinds. So the plan's conclusion holds.
  The mechanism it omits: `nextSequence` (`storage_projection.go:290-304`) and
  `s.mem.AppendEvent` (`storage.go:260`) are **two separate critical sections**.
  Concurrent appends to one run can therefore give the live stream a different
  event→sequence mapping than a rebuild produces. Measured divergence over 60
  attempts per width: **0/60 at 2 concurrent appenders, 4/60 at 4, 8/60 at 8,
  24/60 at 16.** Production has exactly one two-appender path —
  `reconcileCancellation` runs on its own goroutine (`cancel.go:37`) and appends
  `task_cancel_requested` (`cancel.go:76`) while the DAG goroutine appends — and
  it did not diverge in 300 memory-store or 120 SQLite `-race` attempts.
  **§14's fourth rollback criterion is NOT triggered.** It fires only if *replay*
  order stops being append order; replay order is store allocation order, always.
  It is the *live* order that can drift, so a rebuild canonicalises rather than
  corrupts. Plan 21 remains the right plan. Recorded in §11 instead.

- **C4 — mutation proof #8 is vacuous against the test named for it.**
  `projectionState` (`storage_catchup_test.go:285-313`) renders run
  id/status/task-count and per-task status/version/refs/attempts. It never calls
  `ListEvents` and never renders a sequence, so no `projectionState`-based test
  can detect "the replay path reassigns `Sequence` from a fresh counter." The
  replacement test renders event id, sequence and `created_at` explicitly.

- **C5 — change #10 would fail as specified.**
  `TestProjectionCatchUpPreservesOrdering` creates its run with a **zero**
  `CreatedAt` (`storage_catchup_test.go:235`). After change #3, repo A stamps its
  own `now()` and the fresh replay repo C stamps its own, so merely adding
  `CreatedAt` to `projectionState` makes that test fail. The run must be created
  with a non-zero `CreatedAt` in the same edit.

- **C6 — change #2 is dormant on its own.** No production caller sets
  `LifecycleEvent.CreatedAt`; all nine construction sites leave it zero
  (`coordinator.go:56`, `record_results.go:49`, `dag.go:78,251`,
  `cancel.go:17,72`, `spawn.go:72,146`, `recovery.go:193`). So `memory.go:203`'s
  guard changes nothing observable for any current in-process caller — it matters
  only for §5.1's stamp and the replay path. §10's "Wave 1 stands alone and is
  worth landing alone" is still true, but **only because of #3**, not #2.

- **C7 — "A also makes `RebuildProjection` correct for free" is overstated.**
  `RebuildProjection` appends `fromStorageEvent` output directly
  (`storage_schema.go:259-265`) and `fromStorageEvent` never sets `Sequence`. So
  every lifecycle event it returns keeps `Sequence == 0` *after* this plan too. A
  fixes its timestamps, not it. (Still no production callers — confirmed, one
  test at `storage_test.go:406`.)

- **C8 — §7 change #9 lists `internal/ledger/memory_test.go` without "(new)".**
  The file does not exist.

**Confirmed as written**, with own evidence:

- **§1's four-step mechanism.** `marshalLifecycleEvent` at `storage.go:247`,
  `newStoreEvent` at `:252`, `s.mem.AppendEvent` at `:260`, unconditional
  `event.Sequence = seq; event.CreatedAt = m.now()` at `memory.go:202-203`.
  Replay re-enters at `storage_projection.go:149-157`. `LifecycleEvent` carries
  no JSON tags (`types.go:194-204`), so both fields are in the stored JSON,
  present and zero. `fromStorageEvent` recovers four fields and not those two
  (`storage_schema.go:361-366`).
- **§1b, empirically.** Three rows written in one call all read back
  `created_at = "2026-07-30 03:54:50"` — second granularity, no fractional part,
  no zone, and tied inside one second exactly as claimed. `Append` writes five
  columns (`store.go:295`); no `SELECT` reads the sixth (`store.go:307,324,351`);
  `storage.Event` has no field for it (`store.go:32-38`); `storage.Memory` has no
  clock (`store.go:78-91`).
- **§1c.** `spawn.go:44-46` sets a real `CreatedAt`, `storage.go:169` persists
  it, `memory.go:89` overwrites it unconditionally, `storage_projection.go:130`
  re-enters on replay, and `diagnostics.go:52-54,64` sorts by it and computes
  `time.Since`. `TaskSnapshot.CreatedAt` survives (`memory.go:152`) — the
  asymmetry is real. *Minor overstatement:* `diagnostics_test.go:23` does stake
  out three runs an hour apart, but the test asserts only the count and a
  non-empty `RunID`; it never asserts the sort, so it does not "appear to test"
  it.
- **§6, empirically.** `PRAGMA user_version` reads **0**; the tables are exactly
  `content events run_claims`; the only pragmas are the four at `store.go:247`;
  DDL inline at `store.go:253-275`; no version table, no migration runner.
- **`SetTimeSource` on both repositories, storage forwarding to memory.**
  `memory.go:57-61`, `storage.go:70-75`, forward at `:74`. §5.1's lock idiom does
  match `CompareAndSetTaskStatus` (`storage.go:283-288`), and the `s.mu → mem.mu`
  order agrees with `applyTail`, so there is no inversion.
- **The "who is harmed" table.** `inspect_agents` really has no timestamp in its
  payload (`orchestrate.go:372-377`) — though its `Description()` advertises
  "timestamps" (`orchestrate.go:305`), a pre-existing inaccuracy. The TUI
  dashboard really is fed `time.Now()` from live events
  (`tui_run_dashboard.go:329,350`), never the repository.

**Two surfaces the table omits, both checked:**

- `StorageLedgerRepository.Recover` (`storage_recovery.go:20-45`) reads
  `mem.ListRuns` but takes only RunID, DisplayName and Status. **Not harmed.**
- `fullSnapshot` (`memory.go:364-367`) synthesises `CompletedAt = now()` on
  **every read** for any terminal run whose `CompletedAt` is nil. Same class of
  defect; no current reader consumes run `CompletedAt`, so nothing is harmed
  today; **not fixed by this plan.**

**Citation drift** (correct claim, wrong line): `store.go:31-37` → `32-38`;
`store.go:305,319,344` → `307,324,351`; `store.go:76-110` → `78-91`;
`storage_events_test.go:110-175` → `110-179`; `docs/product/agent.md:101-103` →
`101-105`; `diagnostics.go:51-66` → `52-66`.

**Net effect on the decision.** Option A stands, minus its ID half. The plan is
smaller than it thought: three production edits, not five. The headline defect
(event `created_at` is fiction on a model-visible surface) and §1c (run
`created_at` and `Elapsed` on `mivia diagnostics`) are both real and both worth
fixing. §14 no longer permits "land nothing", because the criterion that said so
was based on C1's misread.

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

**Corrected (C1).** The third corollary already holds for events, and needs no code change to make it hold. `mem.ListEvents` (`memory.go:209-221`) performs no sort: it returns `rec.events` in append order, and append order is sequence order by construction (`memory.go:201-205`). Measured under a fixed clock, six events with byte-identical timestamps came back `seq=1..6` in order. The corollary stays in this list as a property to *guard*, not to establish — hence `TestListEventsOrderedBySequenceUnderTiedTimestamps` survives as a regression test while change #4 is withdrawn. The `CreatedAt`-ordered unstable sorts that do exist are over tasks (`memory.go:181`, `memory.go:357`, `storage_schema.go:253`) and are out of scope.

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

**DECIDED: A, minus its ID half (C2).** The only option that changes neither of `mem.AppendEvent`'s two responsibilities — it changes an *ordering of statements* in one function and adds one `IsZero` guard in another. B pays an exported-interface cost for a strictly worse value. C pays for reimplementing dedup and sequencing to fix neither. D is defensible only under the cost model A refutes. A also makes `RebuildProjection`'s **timestamps** correct for free, since it reads the payload directly — but not its `Sequence`, which stays zero (C7).

**On the storage-row-ID wrinkle.** `newStoreEvent` mints its own row ID (`storage.go:149`, `"se-N"`) rather than reusing `event.ID`, and `fromStorageEvent` sets `l.ID = evt.ID` — so **`list_run_events` reports a different event `id` before and after a rebuild**, and `mem`'s dedup-by-ID cannot recognise a replayed event as one it already appended. Confirmed empirically: live `[evt-1 … evt-5]` replays as `[se-3 … se-11]`.

~~A **fixes** this in the same one-line idiom~~ — **REFUTED, see C2.** Restoring the decoded `ID` makes dedup-by-ID collide with the process-local `evt-N` counter, so a resumed run's own events are rejected as duplicates and vanish from `ListEvents` while their store rows persist. Verified end-to-end. The paragraph's claim that "dedup-by-ID becomes meaningful on the replay path" is the exact mechanism of the regression. It is correct that nothing else depends on current behaviour (`advanceStorageEventIDCounter` parses `evt.ID`, the storage event, not `l.ID`) — but the dedup path does. **The ID half of option A is withdrawn**, which is also what keeps §4's own criterion ("changes neither of `mem.AppendEvent`'s two responsibilities") true.

Not fixed by A: the row ID and the event ID remain two different identifiers. Unifying them would put a caller-controlled string in a `PRIMARY KEY` the store dedups on, changing duplicate-append semantics for every kind. Out of scope; §11 keeps it out.

## 5. Design

~~Five~~ **Three** production edits after C1 and C2, all `internal/ledger`.

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

**5.4 — Recover `CreatedAt` on replay** (`storage_schema.go:349-368`). In the `decodeMarshalledLifecycleEvent` branch, add `l.CreatedAt = decoded.CreatedAt`, guarded on non-zero so legacy rows fall through to 5.2's stamp. Rewrite the `KNOWN LIMITATION` block to state what is now durable, what is derived (`Sequence`, per §1a as corrected by C3), and that rows written before this change still replay to the read instant.

~~`l.ID = decoded.ID`~~ — **withdrawn, see C2.** It collides with the process-local `evt-N` counter and breaks resume.

**~~5.5 — Order by `sequence`, not by clock~~ — withdrawn, see C1.** `ListEvents` performs no sort; `memory.go:181` is `ListTasks`, and the proposed expression would not compile against `TaskSnapshot`. The property is already true and is guarded by test instead.

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
| 2 | `internal/ledger/memory.go:203` | 5.2 — stamp event `CreatedAt` only when zero (dormant alone, see C6) |
| 3 | `internal/ledger/memory.go:89` | 5.3 — stamp run `CreatedAt` only when zero (§1c) |
| ~~4~~ | ~~`internal/ledger/memory.go:181`~~ | **WITHDRAWN (C1)** — `ListEvents` does not sort; the expression would not compile |
| 5 | `internal/ledger/storage_schema.go:349-368` | 5.4 — recover `CreatedAt` only; rewrite the limitation comment. ~~`ID`~~ withdrawn (C2) |
| 6 | `internal/ledger/storage_schema.go:321-330` | Delete dead, misleading `toStorageEvent` (zero callers confirmed) |
| 7 | `internal/ledger/storage_events_test.go:99-179` | **Invert** `TestListEventsTimestampsAreReplayRelative` → `TestListEventsPreserveOriginalTimestampAcrossRebuild` |
| 8 | `internal/ledger/storage_events_test.go` (append) | New tests per §9 |
| 9 | `internal/ledger/memory_events_test.go` (**new**, C8) | Memory-backend tests per §9 |
| 10 | `internal/ledger/storage_catchup_test.go:235,283-284` | Include run `CreatedAt` in `projectionState` **and** create the run with a non-zero one (C5) |
| 11 | `.mivia/invariants.md` INV-AG-11 | Amended text per §12 |
| 12 | `Makefile:131` | Replace the pinning test name with the inverted one; add the new names |
| 13 | `docs/product/agent.md:101-105` | Replace the replay-instant caveat with the real contract |

No new exported symbols. The only signature-adjacent statement is negative and load-bearing:

```go
// Unchanged, deliberately. Sequence remains derived at replay; §1a proves the
// derived values are identical to the live ones.
ListEvents(ctx context.Context, runID string) ([]LifecycleEvent, error)
```

**Structure gate.** Baselines re-measured and exact: `memory.go` 465 → ~469 (soft 500). `storage.go` 458 → ~463. `storage_schema.go` 388 → ~380 after the §5 deletion — it shrinks. `storage_events_test.go` 238 → ~400 (testSoft 800). Nothing goes into `storage_test.go` (724). No function approaches 80 lines.

## 8. Constraints check

- **Memory backend is the default.** Changes #2 and #3 are *in* it (#4 withdrawn, C1); #1 and #5 are the storage backend's use of it. §9 tests both.
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
- ~~`TestListEventsPreserveEventIDAcrossRebuild`~~ — **withdrawn with C2.**
- `TestAppendEventStampsBeforeMarshalling` — reads the row payload back through the store and asserts the JSON `CreatedAt` is non-zero and equals what `ListEvents` reported live. Pins §3 corollary 2 structurally: reordering `storage.go:247` and `:260` fails it even if the rebuild test were satisfied another way.
- `TestListEventsOrderedBySequenceUnderTiedTimestamps` — fixed clock, several events, assert `sequence` is `1..N` in order and that the exposed order matches append order. **Passes today** (C1: there is no sort to trap), so it is a regression guard rather than a RED test. Its mutation proof is "make `ListEvents` sort by `CreatedAt`" — i.e. it proves the property cannot be *lost*.
- `TestLegacyRowWithoutTimestampFallsBackToReadInstant` — a row whose payload has a zero `CreatedAt` still yields a non-zero timestamp and no error. §6's graceful-degradation claim, tested rather than asserted in prose.
- `TestMemoryCreateRunPreservesSuppliedCreatedAt` — §1c on the default backend.
- `TestMemoryAppendEventStampsOnlyUnstampedEvents` — §3 corollary 1 on the default backend. Note C6: no production caller exercises this path today; the test covers the contract §5.1 and the replay path depend on.
- `TestProjectionStateIncludesTimestampsAcrossRebuild` — change #10. Per C4 it must render **event id, sequence and `created_at`** as well as run `CreatedAt`; `projectionState` alone reads none of those, so a test built only on it cannot catch mutation #8.

**Mutation proofs.**

| # | Mutation | Test that MUST fail |
|---|---|---|
| 1 | Move the `CreatedAt` stamp back after `marshalLifecycleEvent` | `TestAppendEventStampsBeforeMarshalling` |
| 2 | Restore unconditional `event.CreatedAt = m.now()` | `TestListEventsPreserveOriginalTimestampAcrossRebuild` |
| 3 | Drop `l.CreatedAt = decoded.CreatedAt` | `TestListEventsPreserveOriginalTimestampAcrossRebuild` |
| ~~4~~ | ~~Drop `l.ID = decoded.ID`~~ | **WITHDRAWN (C2)** |
| 5 | Restore unconditional `rec.snapshot.CreatedAt = m.now()` | `TestMemoryCreateRunPreservesSuppliedCreatedAt` |
| 6 | Make `ListEvents` sort by `CreatedAt` (the shape C1 shows it never had) | `TestListEventsOrderedBySequenceUnderTiedTimestamps` |
| 7 | Make `fromStorageEvent` trust a zero decoded `CreatedAt` verbatim | `TestLegacyRowWithoutTimestampFallsBackToReadInstant` |
| 8 | Have the replay path reassign `Sequence` from a fresh counter | `TestProjectionStateIncludesTimestampsAcrossRebuild` (only in its C4-corrected form) |

Mutations 1–3 are the regression proofs for §1 and must be recorded with `Regression: INV-AG-11`.

**The pinning test must be inverted, not deleted.** `19` left `TestListEventsTimestampsAreReplayRelative` asserting `!got.CreatedAt.Equal(original)` with a comment saying it "is expected to fail and should be inverted" if the timestamp becomes durable. Honouring that: the assertion direction flips, the name becomes `TestListEventsPreserveOriginalTimestampAcrossRebuild`, the slop-tolerant pair collapses into one exact `Equal` under an injected clock, and `Makefile:131` is updated in the same commit. Deleting it would silently retire the only regression guard on the defect.

## 10. Implementation waves

Per `.mivia/rules/05` Step 1: one file per task, test before production, reviewer every 2–3 tasks.

**Wave 1 — the default backend** (no storage path yet; independently shippable)
1. `internal/ledger/memory_events_test.go` (new) — `TestMemoryCreateRunPreservesSuppliedCreatedAt`, `TestMemoryAppendEventStampsOnlyUnstampedEvents` (RED), plus `TestListEventsOrderedBySequenceUnderTiedTimestamps` (green guard, per C1).
2. `internal/ledger/memory.go` — changes #2 and #3. ~~#4~~ withdrawn (C1).
   *Reviewer checkpoint.*

**Wave 2 — make the durable copy real**
3. `internal/ledger/storage_events_test.go` — `TestAppendEventStampsBeforeMarshalling` (RED).
4. `internal/ledger/storage.go` — change #1.
5. `internal/ledger/storage_events_test.go` — invert #7; add `TestLegacyRowWithoutTimestampFallsBackToReadInstant` (RED).
   *Reviewer checkpoint.*

**Wave 3 — the replay path**
6. `internal/ledger/storage_schema.go` — changes #5 and #6.
7. `internal/ledger/storage_catchup_test.go` — change #10.
   *Reviewer checkpoint.*

**Wave 4 — close the loop**
8. `.mivia/invariants.md` + `Makefile:131` — changes #11, #12.
9. `docs/product/agent.md` — change #13.

Wave 1 stands alone and is worth landing alone: it fixes `mivia diagnostics` (§1c) with no dependency on the storage path. Per C6 that is the *whole* of Wave 1's observable value — #2 is dormant until Wave 2 supplies a stamped event, and the ordering contract (#4) is withdrawn because it was already satisfied. Waves 2–3 are indivisible — landing #1 without #5 stores a timestamp nobody reads; landing #5 without #1 reads a zero.

## 11. What this does NOT solve

- **Runs already on disk stay replay-relative, permanently.** Their payloads hold a zero. `events.created_at` could backfill them to the nearest second; §6 declines to, and §14 is the escape hatch if that matters.
- **`sequence` remains derived, not durable.** §1a shows the derived values are identical today for a serial writer. If replay order ever stopped matching append order, `sequence` would silently drift and mutation #8 is the only guard — a projection-equality test, not a proof.
- **The live event→`sequence` mapping can drift from the durable one under concurrent appends to one run** (C3). `nextSequence` and `s.mem.AppendEvent` are separate critical sections, so two goroutines appending to the same run can be numbered by `mem` in the opposite order to the one the store recorded. Measured 0/60 at two concurrent appenders, 24/60 at sixteen; production has one two-appender path (`reconcileCancellation` vs the DAG goroutine). Not fixed here, and the direction is benign: a rebuild returns the canonical store order. It does mean a *live* reader can see a `created_at` sequence that disagrees with `sequence` — which §3 corollary 3 already declares harmless, since order comes from `sequence`.
- **Nothing about task ordering.** The genuinely unstable `CreatedAt` sorts are over tasks (`memory.go:181,357`, `storage_schema.go:253`), and task ties are reachable because a whole batch shares one `now` (`spawn.go:78`). Real, adjacent, untouched.
- **Run `CompletedAt` stays read-relative.** `fullSnapshot` (`memory.go:364-367`) synthesises it on every read for a terminal run that has none. No reader consumes it today.
- **The event ID and the storage row ID stay two identifiers.** A fixes which one gets *reported*, not unifying them.
- **The timestamp is the writing process's wall clock.** Two processes sharing a workspace with skewed clocks can produce timestamps that disagree with `sequence`. §3 corollary 3 makes that harmless for ordering, and it means a history reader must not compute durations across writers.
- **An event whose `mem.AppendEvent` failed in-process still appears after replay** (§1a). Pre-existing; not touched.
- **`RebuildProjection` still has no production callers.** Change #5 makes it correct, which does not make it used. Two replay implementations remain.
- **Nothing about redaction, retention or size.** A durable timestamp is one more true fact about a run, on surfaces already gated by INV-AG-9. No new exposure.

## 12. Invariant registration

Current row (`.mivia/invariants.md`):

> | INV-AG-11 | Safety | Recorded execution history survives a projection rebuild: a replayed lifecycle event keeps its kind, task and attempt, so a kind filter cannot silently return zero rows for a run the process did not create. `created_at` is replay-relative — a known limitation, pinned by test rather than left to be rediscovered | `TestListEventsRestoresKindAfterProjectionRebuild`, `TestListEventsToleratesUndecodablePayload`, `TestListEventsTimestampsAreReplayRelative` | 2026-07-30 (plan 19 implemented) |

Amended text (corrected: no `id` claim, per C2):

> | INV-AG-11 | Safety | Recorded execution history survives a projection rebuild: a replayed lifecycle event keeps its kind, task, attempt and original `created_at`, so a kind filter cannot silently return zero rows and a timestamp is when the event happened, not when it was read back. The projection stamps only what arrives unstamped, at run level as well as event level; `sequence` is derived at replay from store order, and event order is taken from `sequence`, never from a timestamp, so tied clocks cannot scramble it. Events recorded before plan 21 hold no durable timestamp and still replay to the read instant, and a replayed event still reports the storage row id rather than the caller's | `TestListEventsRestoresKindAfterProjectionRebuild`, `TestListEventsToleratesUndecodablePayload`, `TestListEventsPreserveOriginalTimestampAcrossRebuild`, `TestAppendEventStampsBeforeMarshalling`, `TestListEventsOrderedBySequenceUnderTiedTimestamps`, `TestLegacyRowWithoutTimestampFallsBackToReadInstant`, `TestMemoryCreateRunPreservesSuppliedCreatedAt`, `TestMemoryAppendEventStampsOnlyUnstampedEvents`, `TestProjectionStateIncludesTimestampsAcrossRebuild` | 2026-07-30 (plan 21) |

`make validate-invariants` requires every named test to exist, so changes #11 and #12 land with Wave 4's code, not before it.

## 13. Plan scorecard

| Criterion | Verdict |
|---|---|
| Compiles (no import cycles) | PASS — no new imports; all edits inside `internal/ledger` |
| No breaking API change | PASS — no exported signature changes. `ListEvents` order is **unchanged**: it was already append-order, which is `sequence` order, which is what `repository.go:57` documents (C1 corrects the original claim that this changed) |
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

- ~~**Change #4 (`ListEvents` orders by `sequence`) cannot land.**~~ **WITHDRAWN with C1.** The criterion rested on the false premise that `ListEvents` sorts by `CreatedAt`. It does not sort at all, so durable timestamps cannot make its order non-deterministic and there is no coupling to honour. This criterion no longer permits "land nothing".
- **A durable `Sequence` turns out to be required.** That is option C's cost centre and its own plan. Do not smuggle it into #5 by trusting a payload `Sequence`; a caller-supplied sequence can open gaps and duplicates in the projection.
- **Backfilling pre-plan-21 runs becomes a real requirement.** Then B's interface change is justified for the legacy path only, as a separate plan — with the second-granularity mixing problem (§1b) solved explicitly.
- **§1a turns out to be wrong** — a case is found where **replay** order is not append order. Per C3 this did **not** happen: replay order is store allocation order unconditionally. What C3 found is that the *live* order can drift from it under concurrent appends to one run, which means a rebuild canonicalises rather than corrupts. That is the safe direction and does not fire this criterion.
- **Restoring the event ID becomes a requirement.** Then `newEventID()` must first become unguessable (C2), as its own change in `internal/coordinator`. Do not reinstate `l.ID = decoded.ID` before that.

In every surviving case, land Wave 1 — the run-level fix (§1c) is independent of the storage path and independently correct.
