# 13 — Fence a run to one executor

**Status:** Design-ready; one open decision (§4).
**Date:** 2026-07-30
**Depends on:** `12` (implemented). **Blocks:** nothing.
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

**Recommendation: B, with A's expiry as a later addition if the store ever goes
remote.** Correctness here comes from the OS, not from a timeout guess, and the
corollary-2 failure (a live process losing its run) is impossible. The
hung-holder case is a worse-looking but safer failure than double execution: a
user can kill the process; they cannot un-send a duplicated side effect.

Do not choose A without deciding the expiry, the renewal interval, and what the
executor does when renewal fails mid-run (it must stop, not continue unfenced).

## 5. Changes (assuming B)

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

## 6. Verification

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

> Write the two-process test against **two repository instances over one store**,
> not one instance. `StorageLedgerRepository` serves reads from an in-process
> projection built once (`storage.go:80-84`), so a single-instance test cannot
> observe cross-process behaviour at all — the same trap that made
> `TestTaskSnapshotRoundTripsNewFields` pass while persisting nothing (`12`).

**Docs:** `docs/product/config.md` — running two mivia instances against one
`store_path`, and what the second one now refuses to do.

## 7. Rollback criterion

If claiming proves to fence runs that are not actually held — the hung-holder
case biting real users — **do not** shorten a timeout into correctness. Move to
option C and remove resume, or accept A with an explicitly chosen and documented
expiry. A fence that sometimes lets two executors through is worse than no
fence, because it invites the assumption that double execution cannot happen.
