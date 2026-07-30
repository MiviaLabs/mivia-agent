# 13 — Fence a run to one executor

**Status:** ✅ **IMPLEMENTED 2026-07-30.** §5 (incremental catch-up) and §6 (the fence) are both
done; §4's decision B (store-level claim) shipped. Verified at HEAD on 2026-07-30: all six §6
changes are present — the `run_claims` table (`internal/storage/store.go`), `ClaimRun`/`ReleaseRun`
on the repository interface, an in-process claim map on the memory backend, claim-before-mutation in
`ResumeInterruptedRun` (`internal/coordinator/recovery.go:94`), claim-on-spawn
(`internal/coordinator/spawn.go:62`), and `HeldByAnotherExecutor` on the run dashboard
(`internal/cli/tui_run_dashboard.go:48`) — and all five §7 tests pass, plus a sixth
(`TestResumeReleasesClaimOnError`) the plan did not name.
**It shipped with no invariant row**, which is a gate gap rather than a code gap: `make invariants`
did not cover the fence at all. Registered as **INV-AG-13** on 2026-07-30, with §7's mutation M1
re-run as proof the pin is not vacuous — skipping the claim in `ResumeInterruptedRun` fails
`TestResumeRefusesRunHeldByAnotherExecutor`.
**This unblocks `15`** (§2 was a hard blocker on §6).
**Date:** 2026-07-30
**Depends on:** `12` (implemented). **Blocks:** `15` (the resume user surface must not ship before the fence — `15` §2).
**Blast radius:** HIGH — the failure it prevents is duplicated external side
effects, and the fix adds a liveness dependency to a path that has none.

---

## 1. The defect

Nothing stops two `mivia` processes sharing one workspace from executing the
same run at the same time.

"Interrupted" is defined purely as *non-terminal status*, with no notion of
whether anyone is still working on it:

```go
// internal/ledger/storage_recovery.go:32
WasInterrupted: r.Status == RunStatusRunning || r.Status == RunStatusQueued || r.Status == RunStatusCreated
```

There is no lease, heartbeat, owner or process identity on a run —
`internal/ledger/types.go` records none, and `12` §3 deliberately keeps caller
identity *out* of the ledger. `ResumeInterruptedRun` gates only on
`isTerminalRunStatus` (`recovery.go:84-86`).

**Reachable path.** Two processes, one workspace, `[subagents].store_backend =
"sqlite"` (`internal/cli/orchestration_state.go` opens the shared file). Process
A is executing run R. Process B starts, `Recover` classifies R as
`WasInterrupted` because A is alive but invisible, the run dashboard lists it,
and a resume marks A's live tasks failed and dispatches them again.

**Impact.** Concurrent double execution of subagent tasks — duplicated external
side effects, duplicated model spend — plus two projections appending
conflicting status events into one `events` table.

**Why the optimistic CAS does not save you.** Each `StorageLedgerRepository`
builds its projection once and never refreshes:

```go
// internal/ledger/storage.go:80-84
if s.built { s.mu.RUnlock(); return nil }
```

Process B validates versions against its own stale snapshot and appends
unconditionally, so the per-task version CAS is not a cross-process guard.

**The hazard is already recognised elsewhere**, which is what makes its absence
here a defect rather than an unconsidered case: `cancelRecovered`
(`internal/coordinator/cancel.go:139-140`) refuses to act on `running` /
`cancel_requested` tasks without a live owner. `ResumeInterruptedRun` has no
equivalent.

## 2. Why `12` made this worth fixing now

Before `12`, resume dropped `Input` and every resumed task failed instantly, so
the double-execution window was theoretical — the second process could not
actually run anything. `12` fixed that. The window is now real.

`12` §3 also means the ledger cannot answer "who owns this run?" by design.
Fencing therefore needs its own mechanism; it must not be smuggled in by
persisting caller identity, which is the thing `12` deliberately refuses.

> **Distinguish the two.** `12` §3 keeps *authority* out of the ledger — what a
> resumed run may do. This plan adds *liveness* — whether anyone is already
> doing it. A lease says "a process is working on R", not "R may run as root".
> Keep them separate or the lease becomes the privilege field `12` rejected.

## 3. Invariant to establish

> **At most one executor may hold a run in a non-terminal state at any time.**
> A second executor must refuse the run rather than resume it, and must say why.

Corollaries worth stating, because they are where this usually goes wrong:

- A crashed holder must not fence the run forever — the lease has to expire.
- A live holder must not lose its run to an impatient peer — expiry has to be
  longer than the worst legitimate pause between heartbeats.
- Refusal must be distinguishable from "already terminal" so the dashboard can
  say *someone else is running this*, not *this is finished*.

## 4. Options — DECISION REQUIRED

### A. Lease column on the run, renewed by heartbeat

`RunSnapshot` gains `LeaseOwner string` (a random per-process ID, **not** a
principal — see §2) and `LeaseExpiresAt time.Time`. The executor renews while
running; resume refuses a run whose lease is unexpired.

*For:* survives crashes by expiry; works across machines if the store is ever
shared over a network filesystem. *Against:* introduces clock dependence and a
renewal goroutine; a paused process (laptop suspend, long GC) can lose its lease
while still alive, which is exactly the corollary-2 failure.

### B. Store-level exclusive claim

Take an advisory lock on the SQLite file (or a dedicated `run_claims` row with a
unique constraint) held for the run's duration.

*For:* no clocks, no renewal, no expiry tuning; the OS releases it when the
process dies, so crash recovery is automatic and exact. *Against:* tied to a
single machine and a single store backend; the memory backend needs a parallel
implementation; a hung-but-alive process holds the claim indefinitely.

### C. Refuse to resume, full stop

Drop `ResumeInterruptedRun` entirely and require the user to start a new run.

*For:* removes the hazard and a large amount of machinery; `12` §7 already names
this as its own rollback. *Against:* discards the recovery capability just made
to work, and interrupted runs stay visible as interrupted with no action
available.

**DECIDED: B**, with A's expiry as a later addition if the store ever goes
remote. Correctness comes from the OS rather than a timeout guess, and the
corollary-2 failure (a live process losing its run) is impossible. The
hung-holder case is a worse-looking but safer failure than double execution: a
user can kill a process; they cannot un-send a duplicated side effect.

If A is ever revisited, it is not adoptable without also deciding the expiry,
the renewal interval, and what the executor does when renewal fails mid-run — it
must stop, not continue unfenced.

## 5. Prerequisite: the projection is built once and never refreshes

**This must be fixed before §6, or the fence silently does nothing.**

`StorageLedgerRepository` builds its in-memory projection on first use and never
rebuilds it:

```go
// internal/ledger/storage.go:80-84
s.mu.RLock()
if s.built {
    s.mu.RUnlock()
    return nil
}
```

Every read is then served from that frozen snapshot — `GetRun`, `ListRuns`,
`GetTask`, `ListTasks` all return `s.mem.…` (`storage.go:221-278`). Writes go to
the store *and* the local projection, so a process sees its own writes and
nobody else's.

**Measured**, two repositories over one store, B building its projection before
A writes:

```
STALE: process B cannot see A's run: not found
B sees 0 runs; A wrote 1
```

### Why this blocks the fence

§4 chose a claim recorded in the store. If `ClaimRun` is read back through the
projection, process B checks a snapshot taken before A's claim existed, sees no
claim, and proceeds. **The fence would report success to both processes** — the
precise failure §8 calls worse than having no fence at all, because it invites
the assumption that double execution cannot happen.

### Independent impact, beyond fencing

Two long-lived `mivia` instances in one workspace each see the other's work
frozen at their own startup: new runs never appear in `ListRuns`, the dashboard
never updates, and `Recover` classifies from stale status. Every cross-process
feature is built on sand until this is fixed, so it is worth fixing on its own
merits rather than only as fencing scaffolding.

### Approach

| | Option | Assessment |
|---|---|---|
| **i** | Rebuild the whole projection on every read | Correct and trivial; O(all events) per read makes it unusable at any real ledger size |
| **ii** | Incremental catch-up: read events after the highest sequence already applied, per run | The `events` table already carries `sequence` with `UNIQUE(run_id, sequence)` (`storage/store.go:113-117`), so this is a bounded tail read |
| **iii** | Bypass the projection for claims only — read/write claims straight from the store | Fixes the fence without fixing staleness. Leaves every other cross-process read wrong |

**Recommendation: ii, with iii as a deliberate narrowing if ii proves too
invasive.** Do not ship iii alone and call the staleness fixed — write down
which was chosen.

`ListRunIDs` (already used by `rebuildLocked`) gives the run set for a catch-up
sweep; the per-run `sequence` gives the watermark. A read that needs freshness
should catch up first; a `built` flag becomes a per-run `appliedSequence`.

**Cost, measured (2026-07-30).** Catch-up puts a store probe on the path of
every ledger read that previously hit memory only.

| Benchmark | Before | After | Δ |
|---|---|---|---|
| `StorageLedger_GetRun` (memory) | 134–159 ns | 202–229 ns | **+27% to +71%** |
| `StorageLedger_CreateRun` | 2068 ns | 2523 ns | +22% |
| `StorageLedger_TaskLifecycle` | 4927 ns | 6639 ns | +35% |
| `StorageLedger_ListRuns` | 22813 ns | 23653 ns | +4% (noise; dominated by snapshot copying) |
| `SQLiteChangesProbe` (up-to-date, 2000 events) | — | **5962 ns** | the real headline |

**The honest headline is the SQLite number, not the percentages.** Every read on
a SQLite-backed ledger now costs a ~6 µs query where it previously cost a ~150 ns
memory lookup — roughly **40× in absolute terms**, though flat in history size
and still single-digit microseconds. That is inherent to option ii; it does not
optimise away.

> An earlier revision of this section reported "+6.5% on `GetRun`". That was
> measured at `-benchtime=200x` on a single run and was under-sampled — it
> understated the cost by an order of magnitude and did not measure SQLite at
> all. The numbers above are an 8-run mean at 8000x, independently re-measured,
> with a dedicated SQLite probe benchmark added because the cost lands on
> **reads**, which no existing benchmark covered.

Two performance traps were found and fixed during implementation, without which
the cost would have been far worse: the memory store's `Changes` was O(history)
until it kept an append-order slice with a per-run max, and SQLite's `Changes`
needed `GROUP BY +run_id` — plain `GROUP BY run_id` makes the planner scan the
whole `UNIQUE(run_id, sequence)` covering index (55 µs) instead of doing a rowid
range search (5.9 µs).

### Tests for §5

- `TestProjectionSeesWritesFromAnotherRepository` — the measured failure above,
  as a regression: B must see a run A wrote after B built its projection. Must
  fail against today's code.
- `TestProjectionCatchUpIsIncremental` — a second read after one new event does
  not re-read the whole history (assert via store call count or event-read
  counter), or option ii silently degrades into option i.
- `TestProjectionCatchUpPreservesOrdering` — interleaved writes from two
  repositories converge to the same task/run state on both.

| # | Mutation | Test that MUST fail |
|---|---|---|
| M0 | Restore the `if s.built { return nil }` early exit | `TestProjectionSeesWritesFromAnotherRepository` |
| M0b | Catch up by rebuilding everything | `TestProjectionCatchUpIsIncremental` |

### 5a. Sequence allocation collides across processes — confirmed reachable

Found while implementing §5, verified reachable at HEAD, and **not fixed** —
it belongs to §6.

`nextSequence` allocates a run's next sequence from *this instance's own*
`applied`/`allocated` state. Two processes over one SQLite store writing the
same run therefore allocate the same number: B probes and sees max 7, allocates
8; A appends 8 first; B's insert violates `UNIQUE(run_id, sequence)`.

Two things make this worse than a lost write:

- **It surfaces inconsistently.** `CreateRun`/`CreateTask`/`AppendEvent` map the
  conflict to `ErrDuplicate`; `CompareAndSetTaskStatus`, `SetTaskOutput`,
  `SetTaskAttempt` and `CloseRun` wrap it as a generic `store append` error, so
  the same root cause reads as four different failures.
- **`CompareAndSetTaskStatus` mutates the projection *before* appending.** A
  lost append leaves that instance's projection ahead of the store, and the
  divergence **survives catch-up**, because `applied` was never advanced past a
  sequence that does not exist. The process then reads a task status the ledger
  never recorded, indefinitely.

§5 does not fix this and cannot: catch-up makes reads fresh, it does not make
allocation exclusive. The fix is either a store-side sequence allocator (let
SQLite assign the sequence inside the insert) or §6's claim making concurrent
writers to one run impossible in the first place. **Decide this as part of §6**
— a claim alone leaves the window open for any writer that does not take one.

## 6. Changes (assuming B)

| # | File | Change |
|---|---|---|
| 1 | `internal/storage/store.go` | acquire an exclusive claim for a run; release on close. A `run_claims(run_id PRIMARY KEY, holder TEXT, acquired_at TEXT)` row is preferable to a file-level lock, which would serialise unrelated runs |
| 2 | `internal/ledger/repository.go` | `ClaimRun(ctx, runID, holder string) (bool, error)` / `ReleaseRun` on the repository interface, so the coordinator is not backend-aware |
| 3 | `internal/ledger/memory.go` | in-process map; trivially exclusive within one process, which is the whole scope of a memory backend |
| 4 | `internal/coordinator/recovery.go` | `ResumeInterruptedRun` claims before `markInterruptedTasks` and returns a distinct `ErrRunHeldByAnotherExecutor` when it cannot |
| 5 | `internal/coordinator/spawn.go` | claim on `Spawn` too — the hazard is not specific to resume; two processes can also be handed the same run ID |
| 6 | `internal/cli/tui_run_dashboard.go` | show *held by another process* separately from *interrupted*, or users will keep trying to resume a run that is running fine |

**Stale claims from a crash.** With B the OS drops the connection and the row
must be reaped: either a `PRAGMA`-level connection scope, or a reaper that
clears claims whose holder no longer holds the SQLite connection. State the
chosen mechanism before implementing — an unreleased claim is corollary 1.

## 7. Verification

**Tests:**

- `TestResumeRefusesRunHeldByAnotherExecutor` — two repositories over one store,
  one holding a claim; resume returns the sentinel error and dispatches nothing.
  The load-bearing test; it must fail if the claim check is removed.
- `TestClaimReleasedOnRunCompletion` — a completed run is claimable again.
- `TestClaimReleasedAfterHolderClose` — corollary 1: a crashed holder does not
  fence the run forever.
- `TestSpawnRefusesConcurrentRunID` — §5 item 5.
- `TestMemoryBackendClaimIsExclusive` — the in-process case.

**Mutation proofs:**

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Skip the claim in `ResumeInterruptedRun` | `TestResumeRefusesRunHeldByAnotherExecutor` |
| M2 | Never release on completion | `TestClaimReleasedOnRunCompletion` |
| M3 | Treat a stale claim as live | `TestClaimReleasedAfterHolderClose` |

> Write every two-process test against **two repository instances over one
> store**, never one instance. A single-instance test cannot observe
> cross-process behaviour at all — the same trap that made
> `TestTaskSnapshotRoundTripsNewFields` pass while persisting nothing (`12`).
> §5 is the underlying defect; until it lands, these tests cannot pass.

**Docs:** `docs/product/config.md` — running two mivia instances against one
`store_path`, and what the second one now refuses to do.

## 8. Rollback criterion

If claiming proves to fence runs that are not actually held — the hung-holder
case biting real users — **do not** shorten a timeout into correctness. Move to
option C and remove resume, or accept A with an explicitly chosen and documented
expiry. A fence that sometimes lets two executors through is worse than no
fence, because it invites the assumption that double execution cannot happen.
