# 02 - Run-handle ownership and caller identity propagation

**Status:** ✅ Completed (2026-07-29, `402ca3f`) - with two documented test gaps below.

> **Verified 2026-07-30 by checking the mutation proofs against the tree**, not by
> trusting the commit message. Four of six named tests exist and the guards they
> cover are in place: `TestRunHandleNotAccessibleToOtherOwner`,
> `TestRunHandleAccessibleToAncestor`, `TestRunIDIsNotSequential`,
> `TestUnauthorizedAndUnknownAreIndistinguishable`, plus `TestTaskDepthPropagates`.
> `Makefile:131`'s `-run` alternation was extended to include them, closing the
> §7 concern that a manifest test could pass `validate-invariants` while never
> actually running.
>
> Two named tests do not exist:
>
> - **`TestResumePreservesRunOwner` (M5) - obsolete, not a gap.** §3d decides
>   *not* to persist the owner because resume is dead code. M5 ("skip persisting
>   the owner") and the §7 row asserting "a resumed run is still owned"
>   contradict that decision; they are relics of an earlier draft. The
>   implementation correctly followed §3d. **Do not implement M5** - it would
>   reintroduce the thing §3d rejects.
> - **`TestRunIDCollisionAcrossRestart` (§7) - covered under another name.**
>   Corrected 2026-07-30: an earlier note here called this a real gap. It is not.
>   `TestRunIDDoesNotCollideWithPersistedLegacyID`
>   (`coordinator/coordinator_test.go:74`) asserts both halves §7 asked for - a
>   new random ID does not collide with a persisted `run-N`, and the legacy ID
>   still resolves. Only the name differs. Nothing to write.
>
> The separate defect §3d files - resume rebuilds `Task{ID, Name, DependsOn}` and
> drops `Input`, so `ResumeInterruptedRun` cannot work - remains open and unowned.
**Date:** 2026-07-29
**Commits:** `security(agent): scope orchestration run handles to their owner`, `fix(agent): propagate caller identity and depth through the dispatcher`
**Depends on:** nothing (touches code disjoint from `01`). **Blocks:** `05`, `07`, `09`. **Ship before `01`** so the RED gate is natural - see `00` §4.
**Blast radius:** HIGH (cross-run confidentiality and integrity).

---

## 1. The vulnerability

Orchestration run handles are addressable by a **guessable** ID and authorized by an identity **every caller shares**.

**Guessable IDs.** `internal/coordinator/types.go:141-143`:

```go
var runIDCounter atomic.Uint64
func newRunID() string { return fmt.Sprintf("run-%d", runIDCounter.Add(1)) }
```

A process-global counter. Run IDs are `run-1`, `run-2`, … - trivially enumerable.

**The authorization check does not distinguish callers.** `internal/cli/orchestration_state.go:71-73`:

```go
func orchestrationHandleAccessible(record *orchestrationHandle, dispatcher *runtime.Dispatcher, repo ledger.LedgerRepository) bool {
	return record != nil && record.dispatcher == dispatcher && repositoriesMatch(record.repo, repo)
}
```

`orchestrationHandle` (`:35-40`) carries `coord`, `handle`, `repo`, `dispatcher` - **no owner**. `runHandles` is a package-level global `sync.Map` (`:26`). Within one session every agent shares the dispatcher and the repo, so the check returns true for every caller and every run.

### What an attacker gets

| Tool | Effect on someone else's run |
|---|---|
| `inspect_agents` (`cli/orchestrate.go:265-318`) | run `display_name`, `status`, and per-task `task_id`, `display_name`, `status`, `depends_on`, `output_ref`, `error_ref` |
| `join_run` (`cli/orchestrate_lifecycle.go:38-44`) | blocks on and returns "the final run result including per-task status, output references, and any errors" |
| `cancel_run` (`cli/orchestrate_lifecycle.go:148-155`) | **mutates** - marks queued/running tasks canceled |

`cancel_run` makes this an integrity and availability issue, not only confidentiality. Enumerating `run-1..N` and cancelling is a denial-of-service against concurrent work.

### Is it live today?

**Yes, in combination with `01`.** These three tools are in the subagent denylist, but that denylist is advertisement-only (`01` §1), so a subagent reached by prompt injection can call `inspect_agents` on a guessed `run-N` right now.

After `01` lands the exposure narrows to the root agent, which owns all runs - latent rather than live. **It becomes live again the moment roles exist**, because then multiple distinct principals share one session. That is why this ships before `05`.

## 2. Root cause: there is no caller identity at the tool boundary

`tools.Tool.Execute(ctx context.Context, args json.RawMessage)` receives no `runtime.Request`. So an orchestration tool cannot know *who* is calling, and cannot stamp an owner on a handle it creates.

This is the same missing mechanism as the depth-propagation gap deferred from `01` §5: `buildTasks` (`cli/dispatch.go:231-236`) and `spawnAgentTool.Execute` (`cli/orchestrate.go:172-180`) construct `subagents.Task` with `Depth` unset because they cannot see the invoking request either.

**One mechanism fixes both.** That is the reason to do this as a single plan.

## 3. Design

### 3a. Caller identity in context

In `internal/runtime`, add a context carrier and wrap the call in `Dispatcher.execute` (`dispatcher.go:290-294`, where `callCtx` is already constructed):

```go
// Caller identifies the agent invoking a handler. It travels in the context
// so tools, which receive no Request, can attribute and authorize their work.
type Caller struct {
	TurnID   string
	ParentID string
	Depth    int
	Role     string // populated by plan 05; empty until then
}

func ContextWithCaller(ctx context.Context, c Caller) context.Context
func CallerFrom(ctx context.Context) (Caller, bool)
```

`Role` is declared now and populated in `05`. Declaring it here avoids a second signature change later; it is inert until then, which is acceptable because it is a *field on an internal struct*, not a config field promising behavior.

### 3b. Owner identity for a run

**A session principal must be minted. It cannot be derived from existing fields.** Every candidate already in the tree fails:

| Candidate | Why it fails |
|---|---|
| `Request.ParentID` at root | `chat/session.go:270` sets the **literal constant** `"session"` - identical in every session and every process. Vacuous. |
| `Request.TurnID` | `chat/session.go:271` sets `fmt.Sprintf("turn:%d", myTurn)` - a per-session counter, so `turn:1` collides across sessions. Also turn-scoped: spawn in turn 3, `join_run` in turn 5 fails. |
| `TurnID` to separate a subagent from its root | `multi_step.go:85-87` sets `ParentID: req.ID` but leaves `TurnID: req.TurnID` **unchanged** - root and all its subagents share one `TurnID`. No separation. |

**Design:** mint a random session ID at session construction (`chat/session.go:270`, into `agent.Options`), carry it on `Caller.SessionID`, and stamp it as the run's principal. Add `Caller.Role`, populated by `05`, to separate principals *within* a session.

**Lineage requires a stored chain, not a single link.** `Caller.ParentID` gives one hop; `runtime.Request` is not retained after `Invoke` (`dispatcher.go:328-336` stores only `Result`), so there is no ID→parent map to walk. Store the creating caller's **ancestor chain on `orchestrationHandle` at creation time**, or - simpler and sufficient for v1 - scope ownership to `(SessionID, Role)` and let the parent/child case hold by construction, since a subagent and its parent share a session and, until `05`, a role.

> Simple `record.owner == caller.TurnID` looks correct in a unit test and breaks the real `spawn_agent` → `join_run` flow. Verify against `subagent_integration_test.go` before assuming otherwise.

> **Do not replace the existing dispatcher-identity check - AND with it.** `runHandles` is a package-global `sync.Map` (`orchestration_state.go:26`) shared by every dispatcher in the process, and `record.dispatcher == dispatcher` is currently the *only* thing isolating concurrent sessions. Swapping it for an owner derived from `"session"`/`"turn:N"` would make two concurrent sessions mutually accessible - a **regression**, not a fix. Final predicate: `record.dispatcher == dispatcher && repositoriesMatch(...) && principalMatches(...)`.

### 3c. Unguessable run IDs

Replace the counter with a random token:

```go
func newRunID() string  // "run-" + 128 bits from crypto/rand, base32 lowercase, no padding
```

This is **defence in depth, not the fix.** Capability-by-obscurity alone is not authorization; 3b is the control. Both ship together.

**Care required - the counter is load-bearing for restart safety.** `initCoordinator` (`cli/orchestration_state.go:83-90`) calls `sr.MaxRunIDNumber()` and `coordinator.AdvanceRunIDCounter(maxRun)` to avoid ID collisions after a process restart with a SQLite ledger. Random IDs make collision-avoidance unnecessary, so:

- `AdvanceRunIDCounter` and `MaxRunIDNumber` become dead and should be **deleted**, not left as no-ops.
- **There are three call sites, not one:** `cli/orchestration_state.go:88-91`, `cli/orchestration_state.go:113-114`, and `cli/dispatcher.go:52-53`. Deleting the function while missing two is a compile break; removing one site and leaving the others is a silent no-op. Grep before editing.
- Persisted `run-N` IDs from existing ledgers must still resolve. The new generator only affects *new* IDs; nothing parses the format except `MaxRunIDNumber`. Confirm with a grep for `run-` parsing before deleting.

> **`Owner` is already a taken name - do not reuse it.** `subagents.Task.Owner` exists (`internal/subagents/subagents.go:15`), is hardcoded to `"mivia"` at all three construction sites (`cli/dispatch.go:233`, `cli/orchestrate.go:175`, `cli/delegate.go:110`), flows into `runtime.Request.ParentID` (`subagents.go:206`), and is persisted as `TaskSnapshot.ParentTaskID` via `parentTaskID(task.Owner)` (`coordinator/spawn.go:127`). It means **parent-task ID**, which is a different concept from run-handle ownership. Name the new field `principal` (or `ownerPrincipal`) so the two never collide.
>
> Note also `subagents.Task.Scope` (`subagents.go:20`), a second dormant field plumbed to `Request.Scope` and unused. Do not repurpose it without deciding what it was for.

### 3d. Do NOT persist the owner - resume is dead code

The earlier draft required an owner column on `ledger.TaskSnapshot`. **Dropped, because it buys nothing today.**

`ResumeInterruptedRun` has **zero production callers** - the complete non-test grep is `coordinator/types.go:50` (interface decl), `coordinator/recovery.go:77` (definition), `ledger/types.go:100` (a comment). And `runHandles` (`orchestration_state.go:26`) is in-memory only and never repopulated on restart, so after a restart no handle is reachable by any tool, owner column or not.

**Consequence for `07` §5:** take option **B** (role name ≡ handler name) unconditionally. `02` performs no ledger migration, so `07`'s A/B decision is no longer contingent on it.

**Separate defect, file it, do not fix here:** resume is broken independently of ownership. `recovery.go:99-103` rebuilds `Task{ID, Name, DependsOn}` and drops `Input`, so `MultiStepHandler.Invoke` (`multi_step.go:54-59`) fails immediately with `invalid task input`. `Depth`, `Budget`, `Timeout`, `Scope`, `Permission` are dropped too. Either `fix(coordinator): restore task fields on resume` or delete `ResumeInterruptedRun` as dead code. Plans `07` and `09` describe this path as a *privilege-escalation* risk; it is actually an *unreachable and non-functional* path, and both should say so.

## 4. Changes

| Site | File | Change |
|---|---|---|
| Caller carrier | `internal/runtime/context.go` (new, ~50) | `Caller`, `ContextWithCaller`, `CallerFrom` |
| Wrap the call | `internal/runtime/dispatcher.go:290-294` | stamp `Caller` onto `callCtx` |
| Owner field | `internal/cli/orchestration_state.go:35-40` | `owner` on `orchestrationHandle` |
| Authorization | `internal/cli/orchestration_state.go:71-73` | lineage check in `orchestrationHandleAccessible` |
| Stamp at spawn | `internal/cli/orchestrate.go:131-209` | read `CallerFrom(ctx)`, set owner |
| ID generation | `internal/coordinator/types.go:141-143` | crypto/rand token; delete `AdvanceRunIDCounter` |
| Counter cleanup | `cli/orchestration_state.go:88-91` **and** `:113-114` **and** `cli/dispatcher.go:52-53`, `internal/ledger` | delete `MaxRunIDNumber`/`AdvanceRunIDCounter` - all three sites |
| Depth (free win) | `cli/dispatch.go:231-236`, `cli/orchestrate.go:172-180`, `cli/delegate.go` | `Task.Depth = CallerFrom(ctx).Depth + 1` |
| Retention race | `cli/orchestration_state.go:128-130` (write), `:62` (read) | `handleRetentionDuration` is an **unsynchronised package global** written from `initCoordinator` - reached from `spawnAgentTool.Execute` (`orchestrate.go:132`), i.e. from concurrent tool workers (`loop_tools.go:301-326`) - and read from the per-handle goroutine. Data race; rule 50 hard rule 5. Capture retention as a field on `orchestrationHandle` at store time. |
| Handle leak | `cli/orchestration_state.go:60-68` | The retention goroutine blocks on `<-record.handle.Done()` with no ctx and no `OnClose` unregistration, so a run that never terminates leaks a goroutine **and** pins its handle in the process-global `runHandles` for the process lifetime - outliving the session whose isolation this plan establishes (rule 50 forbidden pattern: background work outliving its parent). Select on a dispatcher-`OnClose` channel. |

Files touched are well under limits (`orchestrate.go` 393, `orchestration_state.go` 143, soft 500).

## 5. Error semantics

Keep returning `{"error":"unknown run_id"}` for an unauthorized run. **Do not** distinguish "exists but not yours" from "does not exist" - that difference is itself an enumeration oracle. The existing code already returns the same string for both (`orchestrate.go:277-282`); preserve it deliberately and note why in a comment, so nobody "improves" the message later.

## 6. Verification

```bash
go build ./... && go vet ./...
go test ./internal/runtime/... ./internal/cli/... ./internal/coordinator/... ./internal/ledger/... -race
make invariants && make validate-invariants
make structure-check && make verify
```

**Tests:**

| Test | Pins |
|---|---|
| `TestRunHandleNotAccessibleToOtherOwner` | **Reproduction test.** Owner A spawns; caller B calls `inspect_agents`/`join_run`/`cancel_run` on A's run ID; all three return `unknown run_id` and A's run is unaffected. |
| `TestRunHandleAccessibleToAncestor` | A parent may inspect/join a run created by its own child (3b) - the regression this design most easily breaks. |
| `TestCancelRunCannotCancelForeignRun` | Integrity: the foreign run still completes. |
| `TestRunIDIsNotSequential` | Two consecutive IDs are not adjacent integers; format is `run-` + high-entropy token. |
| `TestRunIDCollisionAcrossRestart` | Random IDs do not collide with persisted `run-N` IDs; old IDs still resolve. |
| `TestResumePreservesRunOwner` | 3d - a resumed run is still owned. |
| `TestUnauthorizedAndUnknownAreIndistinguishable` | §5 - identical error for both cases. |
| `TestTaskDepthPropagates` | Depth reaches `Task.Depth` so `Policy.MaxDepth` can trip across hops. |

### Mutation proofs (rule 20)

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Drop the owner comparison from `orchestrationHandleAccessible` | `TestRunHandleNotAccessibleToOtherOwner` |
| M2 | Use equality instead of lineage | `TestRunHandleAccessibleToAncestor` |
| M3 | Restore `fmt.Sprintf("run-%d", …)` | `TestRunIDIsNotSequential` |
| M4 | Return a distinct error for unauthorized | `TestUnauthorizedAndUnknownAreIndistinguishable` |
| M5 | Skip persisting the owner | `TestResumePreservesRunOwner` |
| M6 | Drop the `Task.Depth` assignment | `TestTaskDepthPropagates` |

### Invariant

```
| INV-AG-9 | Safety | Orchestration run handles are accessible only to their owner or an ancestor of their owner; unauthorized and unknown run IDs are indistinguishable | `TestRunHandleNotAccessibleToOtherOwner`, `TestRunHandleAccessibleToAncestor`, `TestUnauthorizedAndUnknownAreIndistinguishable` | |
```

Commit body: `Regression: INV-AG-9 (TestRunHandleNotAccessibleToOtherOwner)`.

**Add `Makefile` to §4's change table.** None of `TestRunHandle*`, `TestResume*`, `TestRunID*`, `TestUnauthorized*`, `TestTaskDepth*`, or `TestCancelRunCannotCancelForeignRun` match the hardcoded `-run` alternation at `Makefile:129-133` (the only `TestCancel*` alternatives are `TestCancelKeeps|TestCancelBefore|TestCancelThenTurnEnd`). `scripts/validate_invariants.py` only checks that a named test *exists*, so a manifest entry whose test never runs still passes `validate-invariants` cleanly - extend that script to assert each manifest test is matched by the regex (a rule-20 Critical Drift Guard candidate).

Deleting `AdvanceRunIDCounter`/`MaxRunIDNumber` also requires deleting two ledger tests: `internal/ledger/storage_test.go:682,738`.

Skills: `secure-change`, `concurrency-review` (`runHandles` is a global `sync.Map` with a retention goroutine per handle, `orchestration_state.go:58-69`), `bug-audit`, `verify-code-change` (blast radius HIGH).

**ADLC RED gate:** `TestRunHandleNotAccessibleToOtherOwner` must be observed failing against unmodified code before any production edit.

## 7. Relationship to the rest of the program

- **Independent of `01`.** Different boundary: `01` controls *which tools* an agent may execute; `02` controls *which runs* a tool may act on. Either order works.
- **Blocks `05`.** Roles create multiple principals in one session, which is what turns this from latent to live.
- **Blocks `07`.** The `Caller` carrier is where `Role` lands, and `07`'s resume/idempotency decision depends on whether `02` performs a ledger migration.
- **Removes an item from `09` §3**, which currently documents this as an unfixed limitation. Update that row when this ships.

**Rollback criterion:** if lineage-based ownership proves too restrictive for a legitimate orchestration pattern, widen to "same session principal" - but never back to dispatcher identity, and never revert the unguessable IDs.
