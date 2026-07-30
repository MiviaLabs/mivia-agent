# 24 — Make run deletion durable

**Status:** ✅ IMPLEMENTED 2026-07-30 — Wave 0 landed in `f372c75`; durable tombstone deletion, both replay paths, invariant registration, all verification gates, and the §7 mutation table were executed before archival.
**Date:** 2026-07-30
**Depends on:** nothing. Discovered while validating `20`; see `20`'s §9 note that content retention "needs the schema-versioning work §3 B priced" — this plan needs none of it, and §3 explains why.
**Blocks:** nothing. **Composes with:** `23` (content retention — this plan deliberately deletes no content, §1c) and `13` (two-process fencing — §2 is a hazard to that story, not to this one).
**Blast radius:** MEDIUM. Widens one exported cross-package interface (`storage.Store`, two implementations plus **two** test doubles), adds one durable event kind handled in two replay paths, and **forces a file split**: `internal/storage/store.go` is at 468 of a 500-line `--strict` ceiling (§4).
**Requirement from the requester:** hard deletion must remain possible. §3 is decided under that constraint, so "tombstone instead of deleting" is rejected as a *substitute* and used only as the mechanism that keeps the cursor honest.
**Proposed commits:** `refactor(agent): split the durable store by concern`, `fix(agent): delete a run's durable record, not just its projection`

---

## Two premises corrected up front

Both were claims I made when recommending this work. Both are wrong, and the plan is written against the corrected versions.

- **There is no "poisoned idempotency key" scenario. The idempotency key is not persisted at all.**
  I claimed a failed-to-start run leaves its idempotency key on disk, permanently poisoned. It does not. `StorageLedgerRepository.CreateRun` (`internal/ledger/storage.go:161-183`) marshals only the `RunSnapshot` — `marshalRunSnapshot` is `json.Marshal(snap)` (`storage_schema.go:28-30`) — and passes `key` **only** to `s.mem.CreateRun`. The replay path confirms it: `storage_projection.go:130` re-creates every run with `s.mem.CreateRun(ctx, "", snap)`, an empty key. So the idempotency index is process-local on both backends, and a key never survives a restart. That is a separate finding about idempotency (plan `22`'s territory), not a consequence of this defect.
  *What IS persisted* is `RunSnapshot.RequestFingerprint`, because it is a field on the marshalled struct.

- **This is not a small self-contained change.** I said it needed no design decision. It needs one, and §2 is it: hard deletion breaks a documented precondition of the incremental catch-up cursor, measured. The `--strict` file-size ceiling forces a split on top of that.

**Three further corrections, found after drafting** (two raised by plan `23`'s author against this plan, both verified here):

- **`INV-AG-13` was not free — plan `22` claims it too** (`22:18,395,656,662`), and this plan claimed it at the same time. Neither `scripts/validate_invariants.py` nor `scripts/invariant_coverage.py` parses invariant *ids* at all — both only extract backticked test names — so a duplicate id would have passed every gate and landed silently. §8 now takes **`INV-AG-15`** and states the allocation rule instead of trusting a number. Allocation as of 2026-07-30, after plan `13` §6's run fence was registered retroactively as `INV-AG-13`: `23` → 14, this plan → 15, `22` → 16. Re-read the manifest and take the lowest free id above 12 at the moment of landing.
- **§3 D's "consumed only by `Recover` and `mivia diagnostics`" was wrong: there is no `mivia diagnostics` command.** `cmd/mivia` contains only `main.go`, and `NewDiagnostics` (`internal/cli/diagnostics.go:34`) has **zero production callers** — `grep -rn 'NewDiagnostics' internal/ cmd/ | grep -v _test.go` returns only its own definition. So the resurrected run reaches exactly one surface, `Recover` → the startup stderr line in §1, and there is no operator command on which a future prune or inspection could hang. Corrected in §3 D. (This also means plan `21` §1c's claim that replay-relative run timestamps "reach `mivia diagnostics`" is about an uninvokable surface.)
- **§4 change #7 under-counted the `storage.Store` test doubles.** `countingStore` is not the only one: `flushSQLite` (`internal/storage/store_agent_integration_test.go:171-176`) also implements every `Store` method explicitly — `Append` at `:178`, `Events` at `:186`, and so on — and is passed to `NewQueuedWriter`. Both must gain `DeleteRun` or their packages stop compiling. Change #7 now names both.
- **Memory also needs to retain the tombstone.** The draft said its delete could remove every event while leaving its append cursor intact. That prevents an already-caught-up second reader from observing the deletion: its cursor sees the append but has no event to apply. The implementation retains the one `run_deleted` row on Memory too, while deleting the preceding rows and claims; this preserves reader convergence and hard-deletes the original payloads.
- **Mutation #6 named the wrong observer.** A fresh repository sees only the tombstone after hard deletion, so ignoring `applyStoreEventLocked` still leaves no run to resurrect. `TestDeleteRunConvergesInASecondReader`, which starts with an already-applied projection, is the test that fails when the incremental replay handler ignores `run_deleted`.

---

## 1. The defect

`DeleteRun` on the durable repository deletes only the in-memory projection. The next process replays the store and the deleted run comes back.

`internal/ledger/storage.go:437-442` is the whole implementation:

```go
func (s *StorageLedgerRepository) DeleteRun(ctx context.Context, runID string) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	return s.mem.DeleteRun(ctx, runID)
}
```

`MemoryLedgerRepository.DeleteRun` (`internal/ledger/memory.go:349-362`) drops the idempotency index entries and the run record from its maps. No `DELETE` is issued against `events` or `run_claims`, and no tombstone is appended. Measured over a real SQLite file — create a run, append one event, delete it, close, reopen the same file with a fresh repository:

```
PROBE same process after DeleteRun: GetRun err=not found (notfound=true)
PROBE next process: GetRun err=<nil> id="r-del" name="doomed" status=created
PROBE next process: ListEvents n=1 err=<nil>
```

The run is back, with its events and its original status.

**The visible consequence accumulates without bound.** `StorageLedgerRepository.Recover` marks a run as interrupted when its status is `running`, `queued` **or `created`** (`internal/ledger/storage_recovery.go:35`), and `initCoordinator` prints one line per interrupted run to stderr at startup (`internal/cli/orchestration_state.go:139-145`):

```go
fmt.Fprintf(os.Stderr, "info: recovered interrupted run %s (%s)\n", r.RunID, r.DisplayName)
```

So every run that failed to start produces a false "recovered interrupted run" line on **every subsequent launch, forever**. N failed starts means N spurious lines at every startup, permanently, describing runs that never ran and cannot be resumed.

### 1a. What `DeleteRun` is actually for — the create-failure unwind, and nothing else

Every production caller is error cleanup inside `createAndStartRun`:

| Site | Trigger |
|---|---|
| `internal/coordinator/spawn.go:65` | `ClaimRun` returned `ErrClaimHeld` — another executor holds the run |
| `internal/coordinator/spawn.go:68` | `ClaimRun` failed for any other reason |
| `internal/coordinator/spawn.go:74` (via `releaseAndDeleteRun`) | the `run_created` event append failed |
| `internal/coordinator/spawn.go:80` (via `releaseAndDeleteRun`) | `createTasks` failed |

`grep -rn '\.DeleteRun(' --include=*.go internal/ cmd/` finds no other production caller; `releaseAndDeleteRun` reaches `DeleteRun` at `spawn.go:173`. So the semantics are "this run never really existed; unwind it" — which is why hard deletion is the right shape and an audit trail of it would be noise. There is no user-facing delete command today; this plan does not add one.

**All four sites precede `go c.executeRun` at `spawn.go:91`**, which is the sharp form of §1b: a run that can be deleted has not begun executing, so no task has produced output and no content has been stored. Credit to plan `23`'s author for the precise enumeration; this table originally collapsed `:74` and `:80` into the helper.

**The claim is already released durably**, so the resurrected run is not also stuck: `releaseAndDeleteRun` calls `ReleaseRun` first (`spawn.go:172`), and `SQLite.ReleaseClaim` really deletes the row. Only the run record and its events survive.

### 1b. Content is not involved on this path, and must not be

Content is written by `persistResultContent` (`internal/coordinator/record_results.go:70-84`), reached from `recordRunResults` long after a run has started successfully. A run that failed during `createAndStartRun` has stored no content, so there is nothing for `DeleteRun` to collect.

**This is a deliberate scope boundary, not an omission.** References are content-addressed, so the reference→run relation is many-to-many: two runs producing byte-identical output share one `content` row. Deleting content by run would therefore delete bytes that another run's live reference points at, breaking INV-AG-10 ("a content reference handed to the model resolves, or it is not handed to the model"). `20` §3 A rejected an option on this same ground — "its owning run" is not well defined for a content-addressed key. Content retention is plan `23`, and it needs refcounting or an age sweep, neither of which belongs here.

`Store.DeleteRun` in §6 therefore documents in its contract that it removes events and claims and **never** content.

## 2. The hazard hard deletion introduces — measured

`SQLite.Changes` uses the `events` table's `rowid` as an append cursor, and its doc comment states the precondition outright (`internal/storage/store.go:340-343`):

> Rows are only ever inserted (**never deleted or rewritten**), and SQLite serialises writers, so rowid is a monotonic append position: everything appended after cursor N has a rowid greater than N.

The table is declared `id TEXT PRIMARY KEY` (`store.go:253-257`), so `rowid` is implicit and has no `AUTOINCREMENT`. SQLite therefore allocates `max(rowid)+1`, which **falls back down when the highest rows are deleted**. A reader that had already caught up past those rowids then never sees the rows that reuse them.

This is not theoretical. Measured: two appends, a reader catches up, a hard `DELETE` of that run, then a brand-new run appends.

```
PROBE after 2 appends to rA: e1@rA=rowid1 e2@rA=rowid2
PROBE reader has caught up, cursor = 2
PROBE after hard DELETE of rA:
PROBE after brand-new run rB appended: e3@rB=rowid1
PROBE Changes(afterCursor=2) => changed=map[] newCursor=2
PROBE *** rB IS INVISIBLE to a reader that caught up before the delete ***
```

`Changes` returns an empty map, so `catchUp` never learns that run rB exists and `applyTail` never reads it. A whole run becomes invisible to the second process — silently, with no error. That is the failure mode `13`'s fencing work exists to prevent, and a naive `DELETE FROM events` introduces it.

**The default backend does not have this hazard.** `Memory.Changes` uses `len(m.order)` as its cursor and `order` is a pure append log of run IDs (`store.go:85`, `108`, `138-153`). As long as `Memory.DeleteRun` leaves `order` untouched, the cursor stays monotonic and a deleted run merely reports `maxSeq` 0, which every consumer already treats as "nothing new". So the memory backend needs a plain delete and nothing more; only SQLite needs §3's mechanism. §7 tests both.

## 3. Options — how to hard-delete without breaking the cursor

Judged against what `SQLite.Append` and `SQLite.Changes` currently own — **SQLite-assigned rowids** (which is what makes concurrent appends from two processes collision-free) and a **constant-time freshness probe** (`catchUp` runs it at the start of every repository operation). Disturbing either is the real cost.

### A. Persist a rowid high-water in a new `meta` table; `Append` allocates rowids explicitly

`CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT NOT NULL)`. `DeleteRun` records the deleted maximum; `Append` inserts with an explicit `rowid = max(high_water, MAX(rowid)) + 1`.

*For:* Keeps the probe O(rows since cursor). A new **table** is genuinely safe without a migration runner, unlike a new **column** — `CREATE TABLE IF NOT EXISTS` creates a missing table on an existing file, whereas it never alters an existing one, which is why `20` §3 B and `21` §6 both rejected added columns. It would also give the repo the `schema_version` slot that `23` will want.

*Against — it breaks multi-process append, which is the one thing `Append` must not lose.* Rowid assignment moves from SQLite to the caller. Two processes appending to one file would each have to read the high-water inside their own transaction and would still race unless the read and the insert are serialised across processes; an in-memory counter per process collides outright. Making it safe means a read-plus-update of `meta` inside every `Append` transaction — three statements where there is now one, on the hot write path, to defend against a condition that arises only after a delete.

### B. Hard-delete the rows, then pin the high-water with one tombstone event

Append a small `run_deleted` event for the run **first**, so it takes a fresh sequence and a fresh `rowid` above everything before it; then `DELETE` that run's earlier rows. `max(rowid)` only ever rises, so `Changes`'s precondition holds by construction and neither `Append` nor `Changes` changes at all. The tombstone doubles as the durable deletion marker: both replay paths remove the run from the projection when they see it.

*For:*
- **`Append` and `Changes` are untouched.** Rowid assignment stays SQLite's, so multi-process append is unaffected. The probe stays constant-time.
- **Crash-safe in the right direction.** If the process dies between the tombstone and the `DELETE`, replay still sees the tombstone and still removes the run. The leftover rows are garbage, not a resurrection. The reverse order has no such property, which is why §5 fixes it.
- **A second process converges.** The tombstone's sequence is above that process's watermark, so its next `applyTail` reads it and drops the run. A bare `DELETE` gives a stale reader nothing to observe, so it would keep the run forever.
- **Payloads and space really do go.** All but one row per deleted run is gone, satisfying the hard-deletion requirement. One row per failed start is a rounding error, and `23` can sweep tombstones with the same care it applies to content.

*Against:*
- A new `storageKind*` constant handled in **two** replay implementations — `applyStoreEventLocked` (`storage_projection.go`) and `RebuildProjection` (`storage_schema.go`). `21` C7 confirmed both exist and that `RebuildProjection` still has no production callers, so the second is written for correctness rather than for a caller.
- `Append` rejects an empty payload (`store.go:286-288`, and `Memory` at `:103-105`), so the tombstone carries a minimal JSON body rather than nothing.
- An unrecognised kind must stay non-fatal for forward compatibility — an older binary reading a newer file sees `run_deleted` and must ignore it, which means the older binary still resurrects the run. State it in §9; do not pretend otherwise.

### C. Stop using rowid as the cursor

Replace the probe with `SELECT run_id, MAX(sequence) FROM events GROUP BY run_id` and drop the cursor.

*For:* Smallest diff — one function, no new kind, no replay changes, no new table.

*Against — it regresses the hot path, by the existing code's own analysis.* `store.go:345-349` documents that plain `GROUP BY run_id` makes SQLite scan the whole `UNIQUE(run_id, sequence)` covering index, "turning the probe into O(history)", and that the `GROUP BY +run_id` unary-plus trick exists specifically to avoid it. `catchUp` probes on every repository operation, so this trades a bounded per-operation cost for a smaller diff. It would also need the same change to `Memory.Changes` for the two backends to keep the same semantics.

### D. Accept and document — leave `DeleteRun` projection-only

*For:* Zero code. The resurrected run is inert: no claim, and exactly one surface consumes it — `Recover`, which produces §1's startup line. (There is no `mivia diagnostics` command; see the corrections above.)

*Against:* It contradicts the stated requirement that hard deletion be possible, and the false "recovered interrupted run" line accumulates permanently, one per failed start (§1). `20` chose D on its own merits and this plan considered it seriously; here the requirement settles it.

**DECISION: B.** It is the only option that hard-deletes the data while changing neither of the two things the store currently owns — SQLite-assigned rowids and a constant-time probe. A pays for a per-append read to defend a post-delete condition and breaks concurrent append in the process; C regresses a probe that runs on every ledger operation; D is excluded by the requirement. B's cost is one new durable kind in two replay paths, which is bounded and testable.

## 4. Blast radius and changes

**The file-size ceiling is load-bearing and forces a split.** `internal/storage/store.go` is **468** lines against the `--strict` soft ceiling of 500 (`.mivia/policy/go-structure.json`: `fileLines.soft` 500, `testSoft` 800; `--strict` promotes warnings to failures). The interface method plus its two implementations cannot fit in 32 lines. Splitting is therefore change #1 and must land first, as its own commit, so the behavioural diff is reviewable.

Measured current line counts of every file below: `store.go` 468, `ledger/storage.go` 470, `storage_projection.go` 347, `storage_schema.go` 402, `memory.go` 480, `memory_claims.go` 54, `storage_test.go` 724, `storage_catchup_test.go` 569, `store_test.go` 89.

| # | File | Change |
|---|---|---|
| 1 | `internal/storage/sqlite.go` (new) | Move the `SQLite` type and its methods out of `store.go`. Pure move, no behaviour change. Leaves `store.go` holding the errors, `Event`, `Claim`, the `Store` interface and `Memory` — comfortably under 500 with room for #2. |
| 2 | `internal/storage/store.go` | Add `DeleteRun` to the `Store` interface with the contract in §6, and implement it on `Memory`: delete from `events`, `ids`, `maxSeq`, `claims`; **leave `order` untouched** (§2). |
| 3 | `internal/storage/sqlite.go` | Implement `DeleteRun` on `SQLite`: one transaction under `writeMu`, `DELETE FROM events WHERE run_id=? AND sequence<=?` plus `DELETE FROM run_claims WHERE run_id=?`. Takes the tombstone's sequence as the bound so the tombstone row survives. |
| 4 | `internal/ledger/storage_schema.go` | Add `storageKindRunDeleted = "run_deleted"` to the constant block (`:13-22`), a minimal marshal helper, and handling in `RebuildProjection`. |
| 5 | `internal/ledger/storage_projection.go` | Handle `storageKindRunDeleted` in `applyStoreEventLocked`: remove the run from the projection, and clear its `applied`/`allocated`/`inflight` entries. |
| 6 | `internal/ledger/storage.go` | `DeleteRun` becomes: append the tombstone (allocating a sequence via `nextSequence` **before** anything is deleted), then `s.store.DeleteRun` bounded by that sequence, then `s.mem.DeleteRun`. ~12 lines; 470 → ~482, inside the ceiling but tight. |
| 7 | `internal/ledger/storage_catchup_test.go` **and** `internal/storage/store_agent_integration_test.go` | **Two** test doubles implement `storage.Store` by explicit delegation rather than embedding, so both must gain `DeleteRun` or their packages stop compiling: `countingStore` (`storage_catchup_test.go:16-60`) and `flushSQLite` (`store_agent_integration_test.go:171-176`, passed to `NewQueuedWriter`). |
| 8 | `internal/storage/store_delete_test.go` (new) | Per-backend unit tests for #2 and #3, including the `order`/cursor-monotonicity assertions. |
| 9 | `internal/ledger/storage_delete_test.go` (new) | The end-to-end tests in §7. **New file, not `storage_test.go`**, which is at 724 of the 800 test ceiling. |
| 10 | `.mivia/invariants.md` + `Makefile:131` | INV-AG-15 per §8, plus the new test names in the `-run` regex. |

**No schema migration, and none needed.** No column is added and no table is altered; `run_deleted` is a new value in the existing `kind` column, which is `TEXT` with no constraint. A database written by an older binary simply has no such rows. Compatibility:

| Database / binary | Result |
|---|---|
| Written by an earlier version, read by this one | No `run_deleted` rows exist. Identical to today's behaviour. |
| Written by this version, read by an earlier one | The older `applyStoreEventLocked` hits its default branch for an unknown kind and ignores it, so the older binary **still resurrects the run**. Forward-compatible in the sense that nothing errors; not in the sense that the delete is honoured. §9. |
| Written and read by this version | The run stays deleted. |

**No config surface. No model-facing text.** Rule `60` is not engaged: `run_deleted` is a *storage* kind, and the `list_run_events` tool's enum is over `LifecycleEvent.Kind` (`internal/cli/ledger_tools.go:242-247`), a different vocabulary — `19` §2 and §7 establish that these two sets are distinct. No tool description changes.

## 5. Implementation waves

Per `.mivia/rules/05` Step 1: one file per task, a test task before each production task, reviewer every 2–3 tasks.

**Wave 0 — the split** (no behaviour change; its own commit)
1. `internal/storage/sqlite.go` — change #1. Gate: `go build ./... && go test ./internal/storage/ ./internal/ledger/ -count=1` unchanged, and `python3 scripts/check_go_structure.py --strict --all` passes.

**Wave 1 — the store method**
2. `internal/storage/store_delete_test.go` — change #8 (RED): both backends delete events and claims, neither loses cursor monotonicity, and content is left alone.
3. `internal/storage/store.go` — change #2 (`Memory`).
4. `internal/storage/sqlite.go` — change #3 (`SQLite`).
5. `internal/ledger/storage_catchup_test.go` — change #7, or nothing after this point compiles.
   *Reviewer checkpoint.*

**Wave 2 — the tombstone kind and the replay paths**
6. `internal/ledger/storage_delete_test.go` — change #9 (RED), including the two-reader invisibility test.
7. `internal/ledger/storage_schema.go` — change #4.
8. `internal/ledger/storage_projection.go` — change #5.
   *Reviewer checkpoint.*

**Wave 3 — wire it up**
9. `internal/ledger/storage.go` — change #6.
10. `.mivia/invariants.md` + `Makefile:131` — change #10.

**Two ordering constraints, both correctness rather than preference.**

- **Wave 2 before Wave 3.** Landing #6 without #4/#5 appends a tombstone that no replay path understands, so a deleted run comes back *and* carries an unrecognised event.
- **Inside `DeleteRun`: allocate and append the tombstone BEFORE deleting anything.** `nextSequence` returns `max(applied[runID], allocated[runID]) + 1` (`storage_projection.go:290-304`), both of which this operation is about to clear. Deleting first would let the tombstone take a *lower* sequence than events a second process has already applied, so that process's `EventsSince(runID, applied)` tail read would skip it and the run would live on there forever. Appending first also gives the crash-safety property in §3 B: a crash between the two steps leaves a tombstone and some garbage rows, which replays to the correct outcome.

## 6. API surface

`internal/storage` — the one exported addition:

```go
// DeleteRun removes a run's durable record: every event row for the run whose
// sequence is at or below throughSequence, and any execution claim it holds.
//
// throughSequence exists so a caller can append a tombstone event first and
// then delete everything beneath it. On SQLite that ordering is what keeps
// Changes' cursor honest: rowid is allocated as max(rowid)+1 with no
// AUTOINCREMENT, so deleting the highest rows lets the next insert reuse their
// rowid and a reader that had already caught up past it never sees the reused
// row. Pass 0 to delete every event for the run.
//
// Recorded content is NEVER removed. References are content-addressed, so one
// content row can be the target of references held by several runs; deleting
// bytes by run would break a live reference another run still holds
// (INV-AG-10). Content retention is a separate concern.
DeleteRun(ctx context.Context, runID string, throughSequence int) error
```

`internal/ledger/storage_schema.go`:

```go
storageKindRunDeleted = "run_deleted"
```

`internal/ledger/storage.go` — the replacement body, in the order §5 fixes:

```go
func (s *StorageLedgerRepository) DeleteRun(ctx context.Context, runID string) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	// Tombstone FIRST: it must take a sequence above every event a second
	// process may already have applied, and the sequence allocator reads the
	// very watermarks this operation is about to clear.
	tombstone := s.newStoreEvent(runID, storageKindRunDeleted, runDeletedPayload(runID))
	if err := s.appendStoreEvent(ctx, tombstone); err != nil {
		return fmt.Errorf("store append run_deleted: %w", err)
	}
	if err := s.store.DeleteRun(ctx, runID, tombstone.Sequence-1); err != nil {
		return fmt.Errorf("store delete run %q: %w", runID, err)
	}
	return s.mem.DeleteRun(ctx, runID)
}
```

**Every signature above was checked against the real types, and the snippet compiles as written.** Verified: `newStoreEvent(runID, kind string, payload []byte) storage.Event` (`internal/ledger/storage.go:147-155`) already sets `Sequence: int(s.nextSequence(runID))` at `:151`, so `tombstone.Sequence` is an `int` and `tombstone.Sequence-1` matches `throughSequence int`; and `appendStoreEvent(ctx context.Context, evt storage.Event) error` is `storage_projection.go:334`. Note that `storage.Event.Sequence` is `int` (`internal/storage/store.go:32-38`) while `nextSequence` returns `uint64` (`storage_projection.go:290`) and `applied`/`allocated` are `map[string]uint64` — the conversion exists at exactly one boundary, `:151`, and the implementer must not introduce a second, differently-typed one. `21`'s correction C1 was a proposed change that would not have compiled because it referenced a field the struct did not have, and `RunSnapshot.Status` is `RunStatus` rather than `string` — compile every expression before writing it into a task.

## 7. Verification

```bash
go build ./... && go vet ./...
go test ./internal/storage/ ./internal/ledger/ ./internal/coordinator/ -race -count=1
go test ./internal/... ./cmd/... -race -count=1      # test-only import cycles are invisible to build and vet
export PATH="$HOME/.local/bin:$PATH" && make verify && make invariants
python3 scripts/check_go_structure.py --strict --all
```

**Tests.**

- `TestDeletedRunDoesNotResurrectInNextProcess` — **the load-bearing one.** Create a run with events over a real SQLite file, `DeleteRun`, close, reopen the same path with a fresh repository, assert `GetRun` returns `ErrNotFound` and `ListEvents` returns nothing. Fails today by construction; §1's probe is this test in prose.
- `TestDeleteRunKeepsChangesCursorMonotonic` — the §2 regression, and the reason option B exists. Two repositories over one store: reader A catches up, run X is deleted, a brand-new run Y is created, and A's next catch-up **must** see Y. Fails against a naive `DELETE FROM events`. This is the test that would have caught the bug my first design had.
- `TestDeleteRunConvergesInASecondReader` — reader B has already applied run X's events; after A deletes X, B's next catch-up must drop X. Proves the tombstone's sequence is above B's watermark, which is what §5's ordering constraint buys.
- `TestDeleteRunLeavesContentUntouched` — store content, delete the run, assert `LoadContent` still resolves. Pins §1b so a later reader does not "finish the job" and break INV-AG-10.
- `TestDeleteRunOnMemoryBackend` — the **default** backend (`internal/config/load.go:46-48`). Deletes events and claim, and `Changes` stays monotonic across the delete.
- `TestDeleteRunClearsProjectionWatermarks` — `applied`/`allocated`/`inflight` no longer carry the run, so the maps do not grow without bound across repeated create-failure unwinds.
- `TestRecoverDoesNotReportDeletedRunAsInterrupted` — the user-visible symptom from §1: no false "recovered interrupted run" line. Asserts against `Recover`'s output rather than against stderr.
- `TestUnknownStorageKindIsIgnored` — a forward-compatibility guard: an unrecognised `kind` must not error either replay path. Green today; it protects the §4 compatibility table from a future change that makes unknown kinds fatal.
- `TestStoreSplitIsBehaviourPreserving` — not a new test. Wave 0's gate is that the **existing** `./internal/storage/` and `./internal/ledger/` suites pass unchanged after the move.

**Mutation proofs.** Each mutation must fail exactly the named test; each named test must be able to observe its mutation.

| # | Mutation | Test that MUST fail |
|---|---|---|
| 1 | Restore `DeleteRun` to `return s.mem.DeleteRun(ctx, runID)` | `TestDeletedRunDoesNotResurrectInNextProcess` |
| 2 | Delete the rows first, append the tombstone second (§5's ordering) | `TestDeleteRunConvergesInASecondReader` |
| 3 | Drop the tombstone entirely; issue a bare `DELETE FROM events` | `TestDeleteRunKeepsChangesCursorMonotonic` |
| 4 | Have `Memory.DeleteRun` also delete from `order` | `TestDeleteRunOnMemoryBackend` |
| 5 | Make `Store.DeleteRun` also `DELETE FROM content` | `TestDeleteRunLeavesContentUntouched` |
| 6 | Ignore `storageKindRunDeleted` in `applyStoreEventLocked` | `TestDeleteRunConvergesInASecondReader` |
| 7 | Ignore `storageKindRunDeleted` in `RebuildProjection` | needs its own test — `RebuildProjection` has **no production callers** (`21` C7), so no test that goes through the repository can observe this. The test must call `RebuildProjection` directly, as `storage_test.go:406` already does. |
| 8 | Leave `applied[runID]` in place on delete | `TestDeleteRunClearsProjectionWatermarks` |
| 9 | Make an unrecognised `kind` return an error | `TestUnknownStorageKindIsIgnored` |

Mutation #1 is the regression proof for §1 and must be recorded with `Regression: INV-AG-15`. Mutation #3 is the regression proof for §2.

**Row 7 is the trap in this table** and is called out deliberately: `21`'s correction C4 killed a mutation proof whose named test could not see what it claimed to prove, and `19` found a security test whose regex matched a bisected secret so it passed either way. Any mutation whose only named test routes through the repository cannot detect a `RebuildProjection` bug, because nothing production calls it.

## 8. Invariant registration

`.mivia/invariants.md` holds `INV-AG-1`…`INV-AG-7`, `INV-AG-9`…`INV-AG-12`. **`INV-AG-8` is absent** — a gap, not a free slot; do not reuse it.

**`INV-AG-13` is NOT free, and no gate would have caught it.** Plan `22` claims it (`22:18,395,656,662`) and this plan originally claimed it too. Neither `scripts/validate_invariants.py` nor `scripts/invariant_coverage.py` parses invariant ids — both extract only backticked test names — so two plans registering one id passes every check. Allocation across the three plans in flight: `22` → 13, `23` → 14, **this plan → `INV-AG-15`**. Do not trust that number either: re-read the manifest at the moment of landing and take the lowest free id above 12.

```
| INV-AG-15 | Safety | Deleting a run deletes its durable record, not just this process's view of it: a deleted run does not reappear when the store is replayed. Deletion is hard — the event rows and their payloads are removed — and it is made durable by a tombstone event appended BEFORE the rows are deleted, so a second reader converges on the deletion and a crash between the two steps still replays to the run being absent. The tombstone also pins the store's rowid high-water, because the incremental catch-up cursor is a rowid and SQLite reuses a rowid freed by deleting the highest row, which would make a subsequent run invisible to a reader that had already caught up. Recorded content is deliberately never deleted here: references are content-addressed and shared between runs, so collecting bytes by run would break a live reference (INV-AG-10) | `TestDeletedRunDoesNotResurrectInNextProcess`, `TestDeleteRunKeepsChangesCursorMonotonic`, `TestDeleteRunConvergesInASecondReader`, `TestDeleteRunLeavesContentUntouched`, `TestDeleteRunOnMemoryBackend`, `TestRecoverDoesNotReportDeletedRunAsInterrupted` | 2026-07-30 (plan 24) |
```

Amend `INV-AG-12` — it currently states that "`DeleteRun` removes no content on either backend, on SQLite it deletes nothing from disk at all". The second clause becomes false with this plan and the first stays true. Replace with: "`DeleteRun` removes the run's events but deliberately removes no content (INV-AG-15), so content still outlives its run and there is still no retention." Note plan `23` amends the same row; whichever lands second must merge rather than overwrite.

`INV-AG-10` — unchanged. Nothing here removes a reference or the bytes behind one; `TestDeleteRunLeavesContentUntouched` is the guard that keeps that true.

`Makefile:131` — add `TestDeletedRunDoesNotResurrectInNextProcess`, `TestDeleteRunKeepsChangesCursorMonotonic`, `TestDeleteRunConvergesInASecondReader`, `TestDeleteRunLeavesContentUntouched`, `TestDeleteRunOnMemoryBackend`, `TestRecoverDoesNotReportDeletedRunAsInterrupted`. `scripts/validate_invariants.py` fails if a manifest test is not selected by that regex, so #10 lands with Wave 3's code and not before.

## 9. What this does NOT solve

1. **An older binary still resurrects the run.** It ignores the unrecognised `run_deleted` kind (§4). Only a schema version gate could make that loud, and there is no version table and no migration runner — verified: `OpenSQLite` issues four pragmas and three `CREATE TABLE IF NOT EXISTS`, with no `PRAGMA user_version` (`internal/storage/store.go:247-275`).
2. **Content is never deleted.** By design (§1b). Plan `23`.
3. **One tombstone row per deleted run stays on disk forever.** Bounded by the number of failed starts, and a candidate for `23`'s sweep — which must reproduce §5's ordering reasoning or it will reintroduce §2.
4. **No user-facing delete.** `DeleteRun` remains reachable only from the create-failure unwind (§1a). Exposing deletion to an operator or to the model is a different plan with its own authorization question — note INV-AG-12 records that content reads are not principal-scoped, so a delete surface would need the scoping analysis `20` declined to build.
5. **`Changes` still depends on rowid monotonicity.** This plan preserves that precondition rather than removing the dependency. Any future code that deletes event rows must pin the high-water the same way; §8's invariant text is where that is written down.
6. **The three `DeleteRun` call sites still discard the error** (`spawn.go:65,68,173` are all `_ =`). A delete that fails is silent. Making it loud means deciding what `createAndStartRun` should do when its unwind fails, which is a coordinator-level question this plan does not open.
7. **`RebuildProjection` still has no production callers.** Change #4 makes it correct, not used. Two replay implementations remain (`21` C7).

## 10. Plan scorecard

| Criterion | Verdict |
|---|---|
| Compiles (no import cycles) | PASS — `internal/ledger` already imports `internal/storage`; no new edge. Wave 0 is a same-package move. Note `go build`/`go vet` cannot see test-only cycles, so §7 runs the full `go test ./internal/... ./cmd/...` |
| No breaking API change | **FAIL, deliberately and scoped** — `storage.Store` gains a method. Two implementations plus `countingStore` (change #7); the interface is not exported beyond this module |
| Testable in isolation | PASS — both backends are directly constructible; the two-reader tests need only two repositories over one store, which `storage_catchup_test.go` and `coordinator/run_fence_test.go` already do |
| Backward-compatible config | PASS — no config surface |
| Backward-compatible data | PASS with one stated gap — §4's table; the gap is row 2 and it is in §9 |
| Every function has a test | PASS — §5 pairs each production task with a preceding test task; §7 covers both backends and both replay paths |
| Security tests present | PASS — `TestDeleteRunLeavesContentUntouched` is the negative test that keeps INV-AG-10 intact |
| `--strict` structure gate | PASS **only via change #1's split** — `store.go` is 468/500 and the addition does not fit. New tests go in new files because `storage_test.go` is 724/800 |
| Rule `60` satisfied | N/A — `run_deleted` is a storage kind, not a `LifecycleEvent.Kind`; no tool name, description or schema changes |
| Cost proportionate to harm | PASS — the symptom is unbounded false startup output plus a store that cannot forget a run it was told to forget |

## 11. Rollback criterion

Kill or reduce this plan if:

- **Wave 0's split cannot be made behaviour-preserving.** If moving `SQLite` out of `store.go` needs anything beyond a move, stop and re-scope: a refactor that changes behaviour while a delete path is being added makes both unreviewable. The split is a prerequisite, not part of the fix.
- **`TestDeleteRunKeepsChangesCursorMonotonic` cannot be made to fail against a naive delete.** Then §2's hazard is not reachable the way the probe suggests, option B's entire justification collapses, and the right answer is the smallest thing that fixes §1 — probably a plain `DELETE` with §9 item 5 deleted from the list. Re-derive from §3 rather than keeping B's machinery for a hazard that is not there.
- **Two-process append regresses.** `internal/coordinator/run_fence_test.go` is the existing guard. If option B somehow disturbs it, the tombstone is interacting with sequence allocation in a way §5 did not anticipate — return to §3, and note that option A was rejected precisely for touching this.
- **A user-facing delete becomes a requirement.** Then the authorization question in §9 item 4 has to be answered first, and INV-AG-12's severity analysis is the starting point, not this plan.
