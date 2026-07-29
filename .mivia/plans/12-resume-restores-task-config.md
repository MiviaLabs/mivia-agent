# 12 — Resume actually resumes, and the ledger never grants privilege

**Status:** ✅ Implemented 2026-07-30.
**Date:** 2026-07-30
**Depends on:** `02` (completed). **Blocks:** corrects `07` and `09` §-claims.
**Blast radius:** MEDIUM — changes what is written to the ledger, and makes a
currently-unreachable code path reachable. §3 is the load-bearing section.

---

## 1. The defect

`ResumeInterruptedRun` cannot work. It rebuilds each task as:

```go
// internal/coordinator/recovery.go:97-101
originalTasks = append(originalTasks, subagents.Task{
    ID:        task.TaskID,
    Name:      task.HandlerName,
    DependsOn: task.DependsOn,
})
```

Three fields out of fifteen. `subagents.Task` also carries `Input`, `Depth`,
`Budget`, `Timeout`, `Scope`, `Permission`, `SessionID`, `TurnID`, `Role`,
`Owner` and `InvocationKey` — all dropped. `MultiStepHandler.Invoke`
(`multi_step.go:54-59`) rejects an empty `Input` immediately, so every resumed
task fails with `invalid task input`.

**The root cause is one layer down:** `ledger.TaskSnapshot`
(`internal/ledger/types.go:86-102`) never stored those fields. `HandlerName` was
added with the comment *"Stored so ResumeInterruptedRun can rebuild the task
config"* — the intent was right, the field set was incomplete, and nothing
caught it because the path has no production caller.

`ResumeInterruptedRun` has zero production callers (`coordinator/types.go:50`
interface decl, `recovery.go:77` definition, `ledger/types.go:100` comment). It
is dead, which is why a completely broken path has survived.

## 2. Correcting plans 07 and 09

Both describe resume as a **privilege-escalation risk**. Today that is wrong on
the facts: the path is unreachable and non-functional, so it escalates nothing.

But it is wrong in a way that becomes right the moment this plan lands. Once
resume works, the restored task's `Permission`, `Scope` and `Role` decide what
the resumed work may do — and if those come from the ledger, the ledger becomes
a privilege source. The ledger is a file in the workspace
(`[subagents].store_path`), and the agent has file tools. **A floor the agent
can lower is not a floor** (`04` §5).

So 07 and 09 should not simply be corrected to "harmless". They should say: the
path is currently unreachable, and §3 below is what keeps it harmless after it
is fixed.

## 3. Decision: the ledger restores *work*, never *authority*

Split `subagents.Task` fields into two classes.

**Restored from the ledger — describes the work to redo:**

| Field | Why it is safe |
|---|---|
| `Input` | The task's own payload. Re-executing with different input is not resuming |
| `DependsOn`, `Name` (`HandlerName`) | Already persisted; DAG shape |
| `Timeout`, `Budget`, `Depth` | Resource *limits*. Restoring a smaller-or-equal value is safe; see clamp below |

**Never restored — re-derived from the current caller and config:**

| Field | Why |
|---|---|
| `Permission`, `Scope` | Authority. A tampered ledger row would otherwise grant it |
| `Role` | Same, once `05` lands. Resume must use the role the *resuming* caller holds |
| `SessionID`, `TurnID`, `Owner` | Identity of the caller doing the resuming, not the original. `02` scopes handles by principal; inheriting a persisted principal would let a resumed run be owned by whoever the file says |
| `InvocationKey`, `IdempotencyKey` | Dispatcher idempotency scope. Reusing a persisted key across processes would make a resumed attempt silently dedupe against the original |

**Clamp, do not trust, the restored limits.** `Timeout`, `Budget` and `Depth`
are read from the ledger but capped at the resuming caller's configured maxima
(`config.SubagentConfig`), so a ledger claiming `Depth: 99` or `Budget: 1e9`
cannot exceed what the live config permits. Restoring a *smaller* value is
honoured; a larger one is clamped, not rejected, because the run legitimately
predates a config change.

> **Why not persist everything and validate on read?** Because validation is a
> thing you can forget to do at one call site. Not persisting authority at all
> means there is nothing to forget: the ledger physically cannot say who you
> are. That is the same reasoning that made redaction configuration-only.

## 4. Changes

| # | File | Change |
|---|---|---|
| 1 | `internal/ledger/types.go` | add `Input json.RawMessage`, `Timeout`, `Budget`, `Depth` to `TaskSnapshot`, all `omitempty`. **No** permission/scope/role/session fields |
| 2 | `internal/ledger/types.go` | extend `TaskSnapshot.Clone` to copy them (deep-copy `Input`) |
| 3 | `internal/coordinator/spawn.go:127` | populate the new fields from `task` |
| 4 | `internal/coordinator/recovery.go:97-101` | restore work fields; leave authority fields zero; clamp limits against `c.policy`/config |
| 5 | `internal/ledger/memory.go` | ensure the in-memory repo round-trips the new fields (it stores `TaskSnapshot` values, so this should be Clone-only) |

**No schema migration.** `StorageLedgerRepository` marshals `TaskSnapshot` whole
as JSON into `events.payload` (`storage_schema.go:37-44`); the table is
`(id, run_id, sequence, kind, payload)` with no per-field columns. New
`omitempty` fields are additive — an event written by an older build unmarshals
with them zero-valued, which is exactly the pre-fix behaviour.

**Old runs stay unresumable, honestly.** A task persisted before this change has
no `Input`, so resume must fail with a clear message naming that cause rather
than the generic `invalid task input` — the same treatment `HandlerName`
already gets at `recovery.go:95`.

### 4a. Storage-size consequence, stated

`Input` is the full task payload and is now written to the ledger on every task
creation. For a SQLite store this is a real growth change, and — with plan 10 —
it is written **unredacted unless the workspace configures a policy**. Anyone
enabling `[subagents].store_backend = "sqlite"` should know their task inputs
land on disk. Document in `docs/product/config.md` alongside `store_path`.

## 5. The other gaps this closes

- ~~**`TestRunIDCollisionAcrossRestart`**~~ (`02` §7). **Not a gap** — checked
  before writing it. `TestRunIDDoesNotCollideWithPersistedLegacyID`
  (`coordinator/coordinator_test.go:74`) already asserts both halves: a new
  random ID does not collide with a persisted `run-N`, and the legacy ID still
  resolves. Only the name differs from the one `02` §7 chose.
- **`TestExecuteToolTaskRejectsToolMissingFromRegistry`** (`01` M3, never
  written) — the guard exists at `loop_tools.go` (the `reg.Get` check before
  dispatch) but has no mutation proof. `01` is marked complete, so this is a
  gap in a *shipped* invariant; write the test.

## 6. Verification

```bash
go build ./... && go vet ./...
go test ./internal/... ./cmd/... -race
make verify && make invariants
```

**New tests:**

- `TestResumeRestoresTaskInput` — the load-bearing one: a resumed task carries
  its original `Input` and executes, rather than failing `invalid task input`.
- `TestResumeDoesNotRestoreAuthorityFields` — `Permission`, `Scope`, `Role`,
  `SessionID` and `Owner` are **zero** on the rebuilt task even when the ledger
  row has them set by hand. This is §3 asserted, and it must fail if anyone
  "helpfully" widens the restore.
- `TestResumeClampsLimitsToCurrentConfig` — a ledger claiming `Depth`/`Budget`
  above the live config is clamped, and a smaller value is honoured.
- `TestResumeOldTaskWithoutInputFailsClearly` — pre-change rows fail naming the
  cause.
- `TestTaskSnapshotRoundTripsNewFields` — through both repositories.
- `TestRunIDCollisionAcrossRestart`, `TestExecuteToolTaskRejectsToolMissingFromRegistry` — §5.

**Mutation proofs:**

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Drop `Input` from the restore | `TestResumeRestoresTaskInput` |
| M2 | Also restore `Permission`/`Scope`/`Role` from the snapshot | `TestResumeDoesNotRestoreAuthorityFields` |
| M3 | Trust the persisted `Depth`/`Budget` without clamping | `TestResumeClampsLimitsToCurrentConfig` |
| M4 | Drop the `Input`-missing guard | `TestResumeOldTaskWithoutInputFailsClearly` |
| M5 | Restore the persisted `InvocationKey` | `TestResumeRestoresTaskInput` (a deduped attempt produces no new execution) |

**Docs:** `docs/product/config.md` — §4a's disk-content note.

## 7. Rollback criterion

If restoring work fields proves insufficient to make resume genuinely usable
(e.g. handlers turn out to need state this plan does not persist), **delete
`ResumeInterruptedRun` and its interface method** rather than persisting more.
A dead path that lies about what it does is worse than no path: it is what
produced this defect and the two mischaracterisations in `07` and `09`.
