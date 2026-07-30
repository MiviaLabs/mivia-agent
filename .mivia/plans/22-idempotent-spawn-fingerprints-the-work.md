# 22 — Fingerprint the work, scope the key to its principal

**Status:** PROPOSED 2026-07-30. Decisions open (§3 reaches a recommendation; it has
not been hostile-challenged).
**Date:** 2026-07-30
**Depends on:** `19` (IMPLEMENTED — content references, INV-AG-10), `20` (VALIDATED →
D, INV-AG-12 — its correction **C6** is this plan's origin), `12` (IMPLEMENTED — the
"only fields describing the WORK" principle this plan generalises).
**Blocks:** nothing. **Composes with:** nothing in flight. Touches no file that
`21` touched.
**Blast radius:** LOW–MEDIUM. Three production edits, two of them in one file
(`internal/coordinator/spawn.go`), plus one schema-description string and one doc
section. No exported signature changes. No schema change. **§1a is the section that
sets the severity, and it sets it lower than the defect's shape suggests.**
**Proposed commit subjects:**
- `fix(agent): fingerprint the spawn request's work, not the caller`
- `security(agent): scope idempotency keys to the principal that created them`
- `docs(ai): register idempotency scope as INV-AG-16`

---

## 1. The defect

`coordinator.requestFingerprint` marshals the entire `[]subagents.Task` — including
the caller-identity fields the host stamps onto every task — and uses the digest as
the identity of an idempotent request. Two coupled defects fall out of that one
mechanism.

### The mechanism

| Step | Site | What happens |
|---|---|---|
| 1 | `internal/cli/orchestrate_spawn_tasks.go:46-59` | `buildSpawnTasks` stamps `SessionID: caller.SessionID`, `TurnID: caller.TurnID`, `Role: caller.Role`, `Depth: caller.Depth + 1` onto every task |
| 2 | `internal/subagents/subagents.go:14-30` | `Task` carries **no JSON tags**, so every exported field is marshalled |
| 3 | `internal/coordinator/spawn.go:97-104` | `requestFingerprint` does `json.Marshal(tasks)` → `sha256` → `sha256:<64 hex>` |
| 4 | `internal/coordinator/spawn.go:23-28` | `lookupHandle` hit + fingerprint mismatch ⇒ `ErrIdempotencyConflict` |
| 5 | `internal/coordinator/recovery.go:34-36` | `recoverByIdempotencyKey`: persisted `RequestFingerprint` mismatch ⇒ `ErrIdempotencyConflict` |
| 6 | `internal/cli/orchestrate.go:206-209` | `spawnAgentTool.Execute` returns that as a Go `error`, not a `{"error":…}` envelope |
| 7 | `internal/agent/loop_tools.go:418-421` | An empty result with a non-nil error becomes the literal tool body `error: spawn_agent: idempotency key already used for a different request` |

The turn identifier changes on every user turn: `sendAgent` does `s.turnID++` then
`TurnID: fmt.Sprintf("turn:%d", myTurn)` (`internal/chat/session.go:263-264`, `:304`).
`resetSystem` bumps it too (`:131`), so `/clear` and `Load` also advance it.

> **Correction to the task brief.** The brief cites `internal/chat/session.go:213-214`
> for the increment. Those lines are in **`sendPlain`** (`:210-…`), the no-tools path,
> which never builds an `agent.Options` and never reaches a dispatcher. The
> agent-path increment is `:263-264`. The claim is right; the citation is not.

### Problem A — availability, live

**Measured**, with a probe that mirrors `buildSpawnTasks` field-for-field. Identical
work, one session, consecutive turns:

```
PROBE turn:1 S1        = sha256:92703dad82e187754ffb137745014778af59f3cc604e490c22816ec7999509eb
PROBE turn:2 S1        = sha256:57d9f3f43aa6ceb63056f2222f62a6025ed546940457b8bd81bc8f59d5e9b3d4
PROBE turn:1 S2        = sha256:2f0dfeefe45a443f89e0381eea3c98b46559e206f6ba867b2554410824035782
PROBE turn:1 S1 again  = sha256:92703dad82e187754ffb137745014778af59f3cc604e490c22816ec7999509eb
PROBE digest input = [{"ID":"t1","Name":"multi_step","Owner":"mivia","SessionID":"sess-AAAA",
  "TurnID":"turn:1","Role":"","InvocationKey":"","DependsOn":null,"Scope":"","Permission":"",
  "Input":"do the identical work","Depth":1,"Timeout":30000000000,"Budget":0,"IdempotencyKey":""}]
PROBE work-only turn:1 S1  = sha256:517223bb5dcbc77e1a8c6f99ec4758763d308f305e83fb461a8ee857a427d331
PROBE work-only turn:2 S1  = sha256:517223bb5dcbc77e1a8c6f99ec4758763d308f305e83fb461a8ee857a427d331
PROBE work-only turn:1 S2  = sha256:517223bb5dcbc77e1a8c6f99ec4758763d308f305e83fb461a8ee857a427d331
```

(The brief's digests differ from mine because its probe used different task bytes.
The *property* — identity fields inside the digest — reproduces exactly.)

End to end through a real coordinator, memory repository, same key `key-1`:

```
PROBE spawn turn:1 same-session   err=<nil> handle=true
PROBE spawn turn:2 same-session   err=idempotency key already used for a different request isConflict=true handle=false
PROBE spawn turn:1 replay         err=<nil> sameHandle=true
PROBE spawn foreign-session       err=idempotency key already used for a different request isConflict=true handle=false
```

So the key **works within one turn and only within one turn**. `TurnID` is stable
across every *step* of one `SendUser`, so a same-turn retry after a tool failure does
dedupe; the "same key next turn" case — which is what an idempotency key is for —
deterministically neither dedupes nor starts the run. The model gets an error string
and no run.

`docs/product/agent.md:144-149` promises the behaviour the code does not deliver:

> "If the key matches a completed run, the existing results are returned … A
> different task set with the same key returns `ErrIdempotencyConflict`."

Both bullets are false across a turn boundary: the *same* task set returns
`ErrIdempotencyConflict`.

### Problem B — authz, latent

The only thing stopping a cross-principal read through the idempotency path is that
same accidental digest.

- `lookupHandle` (`spawn.go:106-113`) keys on the idempotency key alone. No principal.
- `recoverByIdempotencyKey` (`recovery.go:26-57`) keys on the idempotency key alone,
  compares only `snap.RequestFingerprint`, and returns a handle over another
  principal's run.
- `spawnResultPayload` (`orchestrate.go:226-241`) has **no** `orchestrationHandleAccessible`
  call, because spawn is the creation path. On the replay path
  `allResultsRecovered` is true (`resultsFromSnapshots` stamps
  `Provenance.Kind = "recovered"` on every result, `recovery.go:276-292`), so
  `runTaskResults` takes `persistedTaskResults` (`orchestrate_lifecycle.go:58-67`),
  which returns each task's `OutputRef` and `ErrorRef` verbatim.
- `storeOrchestrationHandle` (`orchestration_state.go:71-75`) does refuse to
  overwrite the original owner's record, which protects `inspect_agents` / `join_run`
  / `cancel_run` — and not this return value.
- INV-AG-12: `ledger_read` resolves any well-formed reference any caller presents. So
  the chain completes: replay ⇒ `output_ref` ⇒ `ledger_read` ⇒ bytes.

**Measured**, by removing identity from the tasks (i.e. simulating the naive fix for
A) and spawning twice with one key:

```
PROBE owner run=run-ZMH6OVYUWIZ54SW2X6NQZYK7XQ status=completed tasks=1
PROBE owner task=t1 outputRef="ref:output:4062edaf750fb8074e7e83e0c9028c94e32468a8b6f1614774328ef045150f93"
PROBE second caller err=<nil> sameHandle=true
PROBE second caller inspect run=run-ZMH6OVYUWIZ54SW2X6NQZYK7XQ err=<nil>
PROBE second caller sees task=t1 outputRef="ref:output:4062edaf750fb8074e7e83e0c9028c94e32468a8b6f1614774328ef045150f93"
```

**This is the trap.** The obvious fix for A is "stop putting identity in the
fingerprint", and done alone it converts a latent gate into an open door. Any option
in §3 that fixes A without simultaneously converting B's accident into a stated,
tested gate is wrong.

### One more thing today's accident does, in the wrong direction

Today a foreign principal presenting another principal's key receives
`ErrIdempotencyConflict` — a *distinguishing signal*. It says "this key is in use for
different work" to a caller who is entitled to know nothing. Under INV-AG-9's
indistinguishability rule that is itself a (one-bit) leak. The accidental gate is not
merely accidental; it is also the wrong shape.

## 1a. Reachability — Problem B is **not reachable today**

Say it plainly: **A is the whole live defect. B is a landmine, not a leak.**

Three independent facts close it, each verified here rather than taken from `20`:

1. **One principal per process.** `chat.NewSession` mints `SessionID` once
   (`internal/chat/session.go:119`); `Clear` → `resetSystem` (`:143-145`, `:125-133`)
   and `Load` (`internal/chat/persistence.go:279-285`) both replace history and leave
   `SessionID` untouched. One dispatcher is built per process — `attachSessionDispatcher`
   has exactly one caller, `internal/cli/chat_command.go:77` — so one coordinator
   (`initCoordinator` keys them by dispatcher, `orchestration_state.go:121-124`).
2. **Sub-agents share the parent principal by design.** `MultiStepHandler` copies
   `SessionID`/`Role` into the child loop options (`internal/subagents/multi_step.go:92-93`).
   And `spawn_agent` is unreachable from a sub-agent anyway: it is on
   `restrictedRegistry`'s denylist and is `Privileged()`
   (`multi_step.go:238-242`, `orchestrate.go:77`).
3. **`spawn_agent` is the only production caller that supplies a non-empty
   idempotency key**, and it refuses to run without a principal:
   `grep -n runThroughCoordinator` shows `dispatch.go:181` and `delegate.go:117` both
   pass `""`, and `spawnAgentTool.Execute` returns `spawn_agent: missing caller
   identity` when `principalFromContext` fails (`orchestrate.go:179-182`).

`orchestrate.go:30` synthesises a second, ephemeral principal for direct callers of
`runThroughCoordinator` — but those callers pass `key == ""`, so they never reach the
idempotency path. `internal/agent/loop.go:167-169` also mints a `SessionID` when one
is absent, which cannot happen on the spawn path for the same reason.

So the justification for this plan is Problem A — a deterministic, model-visible
failure of a documented parameter — and the justification for doing B *in the same
change* is that fixing A without it is the reachable version of B. That is a
different argument from "close a live hole", and the plan must not be sold as the
latter. `20` §1c forced its own severity down and then chose to build nothing; this
plan's live half survives the same correction, which is why the answer differs.

### 1b. The hardest question, and it dissolves

Is the owning principal available at the idempotency lookup for a run recovered from
disk in a later process? The brief expected this to be the hard part. **It is not,
because cross-process idempotency replay does not exist.**

`CreateRun`'s `key` parameter is never persisted. `StorageLedgerRepository.CreateRun`
marshals only the snapshot (`internal/ledger/storage.go:169-179`) and passes the key
solely to `s.mem.CreateRun` (`:181`). `RunSnapshot` has no key field
(`internal/ledger/types.go:48-57`). On replay, `applyStoreEventLocked` re-enters with
an **empty** key: `s.mem.CreateRun(ctx, "", snap)`
(`internal/ledger/storage_projection.go:130`). `RebuildProjection` never sets one
either (`storage_schema.go:174-180`).

Measured over a real SQLite file, one repository closed and a fresh one opened on the
same path:

```
PROBE same-process  GetRunByIdempotencyKey: runID="run-plan22" fingerprint="sha256:aaaa" err=<nil>
PROBE cross-process GetRunByIdempotencyKey: runID=""          err=not found
PROBE cross-process GetRun(run-plan22):     runID="run-plan22" fingerprint="sha256:aaaa" status=completed err=<nil>
```

The run and its persisted `RequestFingerprint` survive; the key that would reach them
does not. Consequences, all of which simplify this plan:

- **`RequestFingerprint` is not doing double duty as an authority token across
  processes.** It is write-only across a process boundary: `recovery.go:34` is its
  only reader, and that line is reachable only *after* `GetRunByIdempotencyKey`
  succeeds.
- **Fixing A creates no new cross-process leak.** `20` C3 measured that content
  survives a restart on SQLite, and I re-measured it independently —
  `PROBE cross-process LoadContent(ref:output:0340a1d9…): data="plan22 payload" err=<nil>`
  — but no key match can occur to hand a later process a reference in the first place.
- **`recoverByIdempotencyKey` is reachable only in-process.** Two routes: the
  coordinator's own handle map was evicted (`evictHandleAfterTerminal`, 10 min after
  terminal — `handle_lifecycle.go:5-15`, `types.go:81`) while the memory projection
  still holds the index; or a *second coordinator sharing one repository*, which
  `TestIntegration_SpawnIdempotencyAcrossCoordinators`
  (`internal/coordinator/integration_test.go:396-426`) establishes as supported,
  tested behaviour.

## 2. Invariant to establish

> A run's request fingerprint describes only the work that was requested, and an
> idempotency key resolves only within the principal that created it.

Corollaries:

- **Identity is a scope, not a digest input.** A digest that varies with the caller
  cannot deduplicate; a scope that varies with the caller cannot leak. Putting
  identity in the digest gets both wrong at once, which is §1.
- **A foreign key is a fresh key.** A principal presenting another principal's key
  receives a new run — not that run, and not an error. "In use by someone else" stops
  being observable, which is INV-AG-9's indistinguishability property applied to the
  creation path.
- **The scope never reaches the ledger file.** The key index is process-local and
  demonstrably non-durable (§1b), so no authority is persisted and
  `internal/ledger/types.go:105-124`'s decision stands untouched.
- **The digest's field list is written down at the digest site.** Adding a field to
  `subagents.Task` must not silently change what a fingerprint covers. That implicit
  coupling is the whole mechanism of §1.

## 3. Options

Each option is judged against the four things `Spawn`'s idempotency path currently
owns, because disturbing any of them is the real cost:

1. **In-process handle reuse** — `lookupHandle` returns the *same* `*RunHandle`, so a
   second `Join` awaits one execution (`spawn.go:23-28`).
2. **Ledger-backed run resolution** — `recoverByIdempotencyKey` returns a recovered
   handle whose results are rebuilt from snapshots (`recovery.go:26-57`).
3. **Conflict detection** — fingerprint mismatch ⇒ `ErrIdempotencyConflict`, whose
   accidental side effect is the only principal gate on the creation path.
4. **A persisted `RequestFingerprint`** on the run snapshot (`types.go:52`), which
   §1b shows is never compared across processes.

### A. Tag the identity fields `json:"-"` on `subagents.Task`

*For:* Three lines. Fixes A completely and immediately.

*Against — it opens B, measured.* The probe in §1 is exactly this option's behaviour:
a second principal gets `sameHandle=true` and the owner's `output_ref`. Responsibility
3's accidental gate is removed and nothing replaces it.

*Against — it inverts the safe default.* After tagging, every *new* field on
`subagents.Task` is included in the digest by default, and the field set is still
implicit. That is the mechanism that produced §1, preserved.

*Against — spooky action.* It changes the marshalling of a widely-shared struct for
the benefit of one digest function in another package. Nothing at the tag site says
so. **Rejected.**

### B. Work-only fingerprint + an owner map compared at both resolution points

Keep the raw key. `requestFingerprint` covers work only. `coordinator` records
`key → runtime.Caller{SessionID, Role}` in a new process-local map that outlives the
handle, and both `lookupHandle` and `recoverByIdempotencyKey` compare it.

*For:* Explicit. The gate is a named comparison with an obvious negative test.
Responsibilities 1 and 2 keep their shapes.

*Against — a new lifetime to get wrong.* The owner must outlive `c.handles`
(evicted 10 min after terminal) or the gate silently disappears exactly when
responsibility 2 takes over. So a second map with *no* eviction, which is `20` A″'s
unbounded-growth argument reappearing — and `20` C3 is the record of what happens when
such a set's lifetime and the thing it guards disagree.

*Against — it must answer "no recorded owner" and both answers are bad.* Deny breaks
`TestIntegration_SpawnIdempotencyAcrossCoordinators` and every library caller using
`context.Background()`; allow means the gate is silently absent for a second
coordinator on the same repository. **Rejected** in favour of D, which needs no such
answer because the scope travels with the key.

### C. Accept and document

Correct the `idempotency_key` schema description and `docs/product/agent.md` to say
the key deduplicates only within one turn; add a comment at `spawn.go:97` recording
that `SessionID` in the digest is load-bearing for §1's Problem B; register the pair
as an invariant so the next reader does not "fix" A and open B.

*For:* Zero production risk. Honest. This is what `20` chose, and §1a's severity
finding is the same finding that pushed `20` there. Responsibilities 1–4 untouched.

*Against — the two cases are not alike, and the difference is decisive.* `20`'s
proposal cost a *measured availability regression* on a shipped guarantee (C3) to
close a gap defended against no principal that exists. This plan's A half **is** the
availability regression, already shipped: `idempotency_key` is a documented parameter
that fails deterministically on the second turn, and the failure is a hard tool error
rather than a degraded result. Accepting means shipping a parameter whose only
correct use is one the model cannot discover, and rewriting the docs to describe
per-turn deduplication — a feature nobody asked for.

*Against — it leaves the gate accidental.* A comment is not a test. `20` could accept
its limitation because five existing tests pinned it and one was mutation-proved
(`20` §8); here **no test observes B at all**, and the property is enforced by a
digest computed for a different purpose in another file. **Rejected**, but it is the
correct destination if §11 fires.

### D. Work-only fingerprint + a principal-namespaced idempotency key

`requestFingerprint` is replaced by an explicit projection over the fields that
describe the requested work. Separately, `Spawn` composes the key it uses internally
as `scope(ctx) + ":" + idempotencyKey` for a non-empty key, where `scope` is a
fixed-length digest of `runtime.Caller.SessionID` and `.Role` — the exact two fields
`cli.orchestrationPrincipal` is made of (`orchestration_state.go:42-53`). That one
composed string is then used for `lookupHandle`, `recoverByIdempotencyKey`,
`CreateRun` and `newRunHandle`.

*For — no new state and no new lifetime.* The namespace travels *inside* the key, so
`c.handles` and the repository's `idemLookup` become principal-scoped for free. There
is nothing to evict, nothing to grow, and no window in which the gate is absent. This
is the property B could not get.

*For — the gate is structural, and silent.* A foreign principal cannot name another's
key, so `GetRunByIdempotencyKey` returns `not found` and a new run is created.
Corollary 2 is satisfied by construction: no error, no signal, and today's one-bit
"key is taken" leak (§1) disappears too.

*For — responsibilities 1–4 keep their shapes.* Handle reuse, ledger resolution and
conflict detection all still work; only the string they key on changes. `CreateRun`'s
signature, `GetRunByIdempotencyKey`'s signature and both repository implementations
are untouched.

*For — no persisted authority.* §1b proves the key never reaches disk. `types.go:105-124`
is upheld, and hashing the session id means even the process-local index holds a
derived value rather than the unguessable principal itself (rule 10).

*For — no import question at all.* Everything lives in `internal/coordinator`, which
already imports `internal/runtime` (`recovery.go:11`). No new package edge, in
production or in test.

*Against — the coupling to `orchestrationPrincipal` is by convention.* If
`cli.orchestrationPrincipal` gains a third field, the two definitions of "principal"
drift silently. A cross-package test cannot close this: `internal/cli` imports
`internal/coordinator`, so a test in `coordinator` that referenced
`orchestrationPrincipal` would close an import cycle. Mitigated by a doc comment at
both sites and a behavioural test per field; recorded in §9.

*Against — it changes what a *second coordinator on one repository* sees.* Today two
coordinators sharing a repository dedupe on the raw key. After D they dedupe only if
their callers present the same principal. With no caller in context both compose the
same empty-principal scope, so `TestIntegration_SpawnIdempotencyAcrossCoordinators`
and `…RejectsDifferentRequest` keep passing unchanged. With *different* principals
they no longer share, which is the intent.

*Against — key ambiguity is a real hazard.* Naive `sessionID + ":" + role + ":" + key`
collides (role `x`, key `y:z` vs role `x:y`, key `z`) because `Role` and the
model-supplied key are arbitrary text. A fixed-length digest namespace removes it, and
§7 has a test for exactly this.

### E. Remove only `TurnID` from the digest

*For:* The minimal live fix — one field, and A is gone within a session.

*Against:* It leaves `SessionID`, `Role`, `Depth`, `Owner`, `InvocationKey` and
`IdempotencyKey` in the digest, so the field set stays implicit and the gate stays
accidental — enforced by a side effect, with no test naming it and no mutation proof.
It also preserves the one-bit "key is taken" signal (§1) and preserves the trap for
the next reader. **Rejected as a destination**, kept as §11's fallback: it is what D
degrades to if the namespace proves risky.

**DECISION (recommended, open to challenge): D.** It is the only option that fixes A
and converts B's accident into a stated gate *without adding state whose lifetime can
disagree with the thing it guards* — which is precisely the failure `20` C3 measured
and rejected A″ for. A is the trap. B pays a new-lifetime cost to reach a weaker
place than D. C is defensible on severity alone and is refused because A is a live,
documented-behaviour defect rather than a hypothetical one; it stays as §11's
destination. E is D minus the discipline.

## 4. Blast radius and changes

| # | File | Lines now | Change |
|---|---|---|---|
| 1 | `internal/coordinator/spawn.go:97-104` | 174 → ~215 | Replace `requestFingerprint`'s whole-struct marshal with an explicit work projection (§6). Add `idempotencyScope`. |
| 2 | `internal/coordinator/spawn.go:15-42` | (same file) | `Spawn` composes the scoped key once and passes it to `lookupHandle`, `recoverByIdempotencyKey` and `createAndStartRun`. Function is 28 lines → ~34. |
| 3 | `internal/subagents/subagents.go:14-30` | 310 → ~318 | Doc comment only: this struct is **not** the fingerprint's field list; the list lives at change #1 and adding a field here does not change a digest. |
| 4 | `internal/coordinator/spawn_idempotency_test.go` (**new**) | — | §7's coordinator-level tests. |
| 5 | `internal/cli/orchestration_test.go` or the nearest existing spawn-tool test file | see below | §7's end-to-end tool test. **One file, not a new one, if an existing file fits under the cap.** |
| 6 | `internal/cli/orchestrate.go:143-146` | 454 → 455 | `idempotency_key`'s schema `description`: state the scope honestly. Rule `60` applies. |
| 7 | `docs/product/agent.md:144-149` | 173 → ~176 | Correct the Idempotency section; it currently describes behaviour the code does not have. |
| 8 | `.mivia/invariants.md` | — | New INV-AG-16 row; amend INV-AG-9 (§8). |
| 9 | `Makefile:130` | 148 | Add the new test names to the `invariants:` `-run` regex (§8). |

**Structure gate.** `python3 scripts/check_go_structure.py --strict --all` currently
exits 0. Budgets: soft file 500 / hard 800, soft func 80 / hard 120
(`.mivia/policy/go-structure.json`). Measured now: `spawn.go` **174**,
`subagents.go` **310**, `orchestrate.go` **454**, `recovery.go` **415**. Every
proposed file stays far under 500 and no function approaches 80 lines. Separately,
`internal/cli/structure_test.go:18` caps **every** file in `internal/cli` at 800 lines
including tests; the largest is `delegation_test.go` at **691**, so change #5 must go
into a file with ≥100 lines of headroom or into a new one.

**No schema change, no migration, and no mechanism to run one with** — re-verified:
`OpenSQLite` issues four pragmas (`internal/storage/store.go:247`) and three
`CREATE TABLE IF NOT EXISTS` (`:253-275`); `PRAGMA user_version` reads nothing because
it is never issued, and the only hit for `user_version|schema_version|migrat` under
`internal/storage` and `internal/ledger` is `storage_schema.go:353`, a comment
documenting the absence.

### Data compatibility

| Database written by | After this change |
|---|---|
| Current or earlier version, read by this version | Persisted `RequestFingerprint` holds the **old** format (identity included). It is **never compared**, because the idempotency key index does not survive a restart (§1b, measured), so `recovery.go:34` is unreachable for it. No error, no version check, no migration. |
| This version, read by an older binary | `RequestFingerprint` is still a string in the same field of the same payload. The older binary compares it only against its own (old-format) digest for keys created in *its* process. Forward-compatible. |
| This version, read by this version | Work-only digests compare as intended. |
| A pre-change run's key, presented after a restart | Not found today and not found after — a **new** run is created, exactly as now. The composed key changes nothing here because there is no index entry to miss. |

**Consumers of the format, swept.** `grep -rn 'requestFingerprint\|RequestFingerprint'`
finds: `spawn.go:19,24,46,97,156`, `recovery.go:34`, `coordinator/types.go:27`,
`ledger/types.go:52,65`. **No test asserts the format** and no model-visible or
operator-visible surface exposes it — `spawnResultPayload`, `taskSummaries`,
`inspect_agents`' `taskInfo` and `internal/cli/diagnostics.go` all omit it. The format
change is therefore invisible outside `internal/coordinator`.

## 5. Implementation waves

Per `.mivia/rules/05-adlc-agentic-development-lifecycle.md` Step 1: one file per task,
a test task before each production task, reviewer every 2–3 tasks.

**Wave 1 — fingerprint the work** (fixes A; independently shippable)
1. `internal/coordinator/spawn_idempotency_test.go` (new) —
   `TestSpawnFingerprintIgnoresCallerIdentity`,
   `TestSpawnFingerprintCoversRequestedWork`,
   `TestSpawnIdempotencyKeyDedupesAcrossTurns` (all RED),
   plus `TestSpawnConflictStillReportedForDifferentWork` (green guard).
2. `internal/coordinator/spawn.go` — change #1's projection and change #2's use of it.
   *Reviewer checkpoint.*

**Wave 2 — scope the key** (depends on Wave 1)
3. `internal/coordinator/spawn_idempotency_test.go` (append) —
   `TestSpawnIdempotencyScopeIsThePrincipal`,
   `TestSpawnForeignPrincipalGetsANewRun`,
   `TestSpawnForeignPrincipalIsIndistinguishableFromFirstUse`,
   `TestSpawnKeyNamespaceIsUnambiguous`,
   `TestSpawnWithoutCallerIdentityKeepsSharedScope`.
4. `internal/coordinator/spawn.go` — `idempotencyScope` and its use.
5. `internal/subagents/subagents.go` — change #3's comment.
   *Reviewer checkpoint.*

**Wave 3 — the model-facing surface** (depends on Wave 2)
6. `internal/cli/…_test.go` — `TestSpawnAgentIdempotencyKeyDedupesAcrossTurns`, the
   load-bearing test.
7. `internal/cli/orchestrate.go` — change #6.
   *Reviewer checkpoint.*

**Wave 4 — close the loop**
8. `.mivia/invariants.md` + `Makefile:130` — changes #8 and #9.
9. `docs/product/agent.md` — change #7.

**Wave order IS a correctness constraint, in one direction only, and it is the
opposite of the intuitive one.** Wave 2 before Wave 1 is harmless: namespacing the key
while identity is still in the digest merely adds a second reason two principals do
not collide, and every existing test still passes. **Wave 1 before Wave 2 opens
Problem B for the duration** — the §1 probe is a measurement of exactly that
intermediate state. So if the waves are split across commits, Wave 2 must land first
or the two must land together. Wave 1 is described first because it is the RED-test
wave for the live defect; it is not shippable alone despite fixing A alone, and §11
records that. Waves 3–4 are cosmetic and documentary and may land later.

## 6. API surface

No exported symbol changes. `Coordinator.Spawn`'s signature is deliberately untouched:

```go
// Unchanged. The owning principal is read from ctx (runtime.CallerFrom), not
// passed as a parameter, so no caller and no interface implementation changes.
Spawn(context.Context, []subagents.Task, string, ...bool) (*RunHandle, error)
```

`internal/coordinator/spawn.go` — replacing `requestFingerprint`:

```go
// fingerprintTask is the explicit list of fields that describe the WORK a spawn
// request asked for. It is a projection of subagents.Task, not the struct itself:
// marshalling the struct put the caller's SessionID, TurnID, Role and Depth into
// the digest, so a byte-identical request on the next turn produced a different
// fingerprint and an idempotency key could never deduplicate (plan 22 §1).
//
// Adding a field to subagents.Task does NOT change a fingerprint. If a new field
// describes the requested work, add it here deliberately.
type fingerprintTask struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	DependsOn  []string        `json:"depends_on,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Timeout    time.Duration   `json:"timeout,omitempty"`
	Budget     int             `json:"budget,omitempty"`
	Scope      string          `json:"scope,omitempty"`
	Permission string          `json:"permission,omitempty"`
}

// requestFingerprint returns the canonical identity of the WORK in tasks.
func requestFingerprint(tasks []subagents.Task) (string, error)

// idempotencyScope returns a fixed-length namespace for the principal in ctx, or
// the empty-principal namespace when ctx carries no caller. It digests the two
// fields cli.orchestrationPrincipal is made of, so the coordinator's notion of
// "who owns this key" and the cli's notion of "who may control this run" agree.
// The digest is fixed length, so scopedKey cannot be made ambiguous by a Role or
// a caller-supplied key containing the separator.
func idempotencyScope(ctx context.Context) string

// scopedKey namespaces a non-empty idempotency key to its principal. An empty
// key stays empty: it means "no idempotency", and namespacing it would turn every
// unkeyed spawn into a keyed one.
func scopedKey(ctx context.Context, key string) string
```

Compile-relevant facts verified by reading, not assumed:

- `subagents.Task`'s field types are `ID, Name, Owner, SessionID, TurnID, Role,
  InvocationKey, Scope, Permission string`, `DependsOn []string`,
  `Input json.RawMessage`, `Depth int`, `Timeout time.Duration`, `Budget int`,
  `IdempotencyKey string` (`subagents.go:14-30`). `spawn.go` already imports
  `encoding/json` and `time`; it needs `internal/runtime` added, which introduces
  **no new package edge** — `recovery.go:11` already imports it and
  `go list -deps ./internal/coordinator` already lists it.
- `runtime.Caller` is `{SessionID, TurnID, ParentID string; Depth int; Role string}`
  (`internal/runtime/context.go:11-20`); `CallerFrom(ctx) (Caller, bool)` at `:44`.
- `RunSnapshot.Status` is `ledger.RunStatus`, **not** `string` (`ledger/types.go:51`),
  and `RunSnapshot` has no session, principal or key field (`:48-57`). Any test
  constructing one must write `ledger.RunStatusCompleted`, not `"completed"`.
- `RunHandle` already has a field named `owner *coordinator` (`coordinator/types.go:31`).
  Nothing in this plan adds a field to `RunHandle`; if a later revision does, it must
  not reuse that name.
- `ErrIdempotencyConflict` stays exactly where it is (`spawn.go:95`) and keeps its
  meaning: same key, different **work**.

### Field-by-field verdict on `subagents.Task`

| Field | Who sets it on the spawn path | Caller-dependent? | Verdict |
|---|---|---|---|
| `ID` | model (`spawnTaskParams.ID`) | no | **WORK** — in the digest. Task identity within the DAG. |
| `Name` | model | no | **WORK** — which handler does the work. |
| `DependsOn` | model | no | **WORK** — DAG shape. Two different graphs are two different requests. |
| `Input` | model's `prompt`, marshalled (`orchestrate_spawn_tasks.go:32`) | no | **WORK** — the payload. `12` §3 restores it for the same reason. |
| `Timeout` | model's `timeout_seconds`, else config default (`:36-39`) | no | **WORK** — a resource limit, and `12` §3 already classes limits as work. It also shapes execution, so two requests with different deadlines are not the same request. |
| `Budget` | model | no | **WORK** — same reasoning as `Timeout`. |
| `Scope` | never set on either production path (always `""`) | no | **WORK** — it selects a dispatcher resource pool (`internal/runtime/dispatcher.go:297-302`), so it shapes execution rather than naming a caller. Zero today, so including it is observationally neutral and future-correct. |
| `Permission` | derived from the task's own `Name` via the skill registry (`:41-45`) | **no** — the registry is per-process, not per-caller | **WORK.** This is the one judgement call. It *is* authority, so the "exclude authority" reading says drop it. But it is derived from the task's own name, not from who is asking, and it is an input to execution: a key first used for work granted permission `P1` must not silently return a `P1` result to a later request that would have been granted `P2`. Include, and note in §9 that this makes a workspace skill-config change invalidate keys minted before it. |
| `Depth` | `caller.Depth + 1` (`:54`) | **YES** | **EXCLUDE.** Derived from the caller's position, not from the request; it appears in no tool schema. Always `1` today because `spawn_agent` is unreachable from a sub-agent (§1a), so exclusion is observationally neutral. `12` §3 restores it as a clamped limit *on resume*, which is a different question from whether it identifies a request. |
| `SessionID` | `caller.SessionID` (`:55`) | **YES** | **EXCLUDE.** Problem B's accidental gate; becomes an explicit scope instead. |
| `TurnID` | `caller.TurnID` (`:56`) | **YES** | **EXCLUDE.** Problem A, entirely. |
| `Role` | `caller.Role` (`:57`) | **YES** | **EXCLUDE from the digest, INCLUDE in the scope.** It is half of `orchestrationPrincipal` (`orchestration_state.go:42-45`). |
| `Owner` | constant `"mivia"` on both paths (`:50`, `dispatch.go:268`) | no | **EXCLUDE.** It becomes the dispatcher `ParentID` (`subagents.go:214`) and, when it looks like `task-*`, the persisted `ParentTaskID` (`spawn.go:142`, `coordinator.go:71-76`). It names who parents the task, not what was asked. `12` §3 puts it in the never-restored group. Constant today, so neutral. |
| `InvocationKey` | empty on the spawn path; `"dispatch:<N>:<id>"` on `dispatch_tasks` (`dispatch.go:267`), where `<N>` is a per-process counter | **YES** | **EXCLUDE.** Dispatcher idempotency scope; `12` §3 groups it with the authority fields for the same reason. It is a process-local counter, so including it makes a digest unreproducible by construction. `dispatch_tasks` passes `key == ""`, so nothing compares its fingerprint anyway — the value is still persisted, and after this change it becomes stable rather than counter-dependent. |
| `IdempotencyKey` | never set on either path | n/a | **EXCLUDE.** This is the *task-level* dispatcher key (`subagents.go:208`), distinct from `Spawn`'s run-level argument. Putting a key inside the digest that key indexes is circular; `12` §3 also declines to restore it. |

## 7. Verification

```bash
go build ./... && go vet ./...
go test ./internal/coordinator/ ./internal/cli/ ./internal/subagents/ -race -count=1
go test ./internal/... ./cmd/... -race
python3 scripts/check_go_structure.py --strict --all
make verify && make validate-invariants && make invariants
```

`go vet` and `go build` are **not** sufficient to prove no import cycle: an in-package
`_test.go` can close one invisibly, which is exactly what `19`'s correction records
(`internal/storage`'s in-package test imports `internal/agent`, so `internal/runtime`
could not import `internal/ledger`). Checked directly for this plan:
`go list -f '{{.Name}} TEST:{{.TestImports}} XTEST:{{.XTestImports}}' ./internal/coordinator`
imports only `ledger`, `runtime`, `storage`, `subagents` — none of which reach
`internal/coordinator`. `internal/runtime`'s own tests import only `internal/redact`.
So the `coordinator → runtime` edge is safe in test as well as in production. The
constraint this *does* impose: `internal/cli` imports `internal/coordinator`, so no
test in `internal/coordinator` may reference `orchestrationPrincipal`. Change #5's
test therefore lives in `internal/cli`.

### Tests

- `TestSpawnAgentIdempotencyKeyDedupesAcrossTurns` (`internal/cli`) — **the
  load-bearing one.** Drive `spawnAgentTool.Execute` twice with the same
  `idempotency_key`, through contexts carrying one `runtime.Caller.SessionID` and
  `TurnID` `turn:1` then `turn:2`. Assert the second call returns the **same
  `run_id`** and no Go error. It is the only test that covers `buildSpawnTasks`'
  stamping, the tool's error path and the coordinator together. **Fails today** —
  measured: the second call is `ErrIdempotencyConflict`.
- `TestSpawnIdempotencyKeyDedupesAcrossTurns` (`internal/coordinator`) — the same
  property one layer down, so a failure localises. **Fails today.**
- `TestSpawnFingerprintIgnoresCallerIdentity` — one task list, then copies differing
  *only* in `SessionID`, `TurnID`, `Role`, `Depth`, `Owner`, `InvocationKey`,
  `IdempotencyKey`, one field at a time; every digest must be equal. **Fails today**
  for all seven.
- `TestSpawnFingerprintCoversRequestedWork` — copies differing only in `ID`, `Name`,
  `DependsOn`, `Input`, `Timeout`, `Budget`, `Scope`, `Permission`, one at a time;
  every digest must differ. **Passes today**; it is the guard against a projection
  narrowed to nothing, and against a future field added to `subagents.Task` being
  assumed covered.
- `TestSpawnConflictStillReportedForDifferentWork` — same caller, same key, different
  `Input` ⇒ `ErrIdempotencyConflict`. **Passes today**; must survive, or responsibility
  3 has been deleted rather than repaired.
- `TestSpawnIdempotencyScopeIsThePrincipal` — same `SessionID` + same `Role` dedupes;
  same `SessionID` + **different `Role`** produces a **new run, not an error**.
  **Fails today** on the `Role` half (today it is a conflict), which makes it RED for
  the scope's second field rather than only its first.
- `TestSpawnForeignPrincipalGetsANewRun` — P1 spawns with key `K` and joins; P2 (a
  different `SessionID`) spawns with key `K` and byte-identical work. Assert P2's
  `runID != P1`'s, that P2's snapshot contains none of P1's task IDs, and that no
  `OutputRef`/`ErrorRef` from P1's tasks appears in P2's result. **This is the trap as
  a test.** Today it passes for the wrong reason (P2 gets an error rather than a new
  run) — stated plainly here because a green test whose greenness comes from a
  different mechanism is exactly the vacuity `21` C4 and `19`'s bisected-secret regex
  were caught on. Its non-vacuity is established by mutation #2, which makes it fail.
- `TestSpawnForeignPrincipalIsIndistinguishableFromFirstUse` — P2's outcome for P1's
  key must be *identical in shape* to P2's outcome for a key never used: a new run, no
  error. **Fails today**, because today P2 receives `ErrIdempotencyConflict`, which is
  itself the one-bit signal §1 identifies. This is the INV-AG-9 half.
- `TestSpawnKeyNamespaceIsUnambiguous` — two (principal, key) pairs whose naive
  concatenation collides — fixed `SessionID`, (`Role: "x"`, key `"y:z"`) versus
  (`Role: "x:y"`, key `"z"`) — must not share a run. Observable regardless of the
  digest, so it survives every other mutation.
- `TestSpawnWithoutCallerIdentityKeepsSharedScope` — `context.Background()`, two
  spawns, same key, same work ⇒ dedupe, no error. This is the `20` C3 tripwire: it
  fails the moment anyone makes a missing principal a denial, and it is the same
  property `TestIntegration_SpawnIdempotencyAcrossCoordinators` depends on.

### Mutation-proof table

| # | Mutation | Test that MUST fail |
|---|---|---|
| 1 | Put `SessionID` or `TurnID` back into `fingerprintTask` | `TestSpawnAgentIdempotencyKeyDedupesAcrossTurns` (and `TestSpawnFingerprintIgnoresCallerIdentity`) |
| 2 | Drop the scope from `scopedKey` (use the raw key) — **the naive fix for A** | `TestSpawnForeignPrincipalGetsANewRun` |
| 3 | Make `requestFingerprint` a constant, or drop `Input` from the projection | `TestSpawnFingerprintCoversRequestedWork`, `TestSpawnConflictStillReportedForDifferentWork` |
| 4 | Namespace on `SessionID` only, ignoring `Role` | `TestSpawnIdempotencyScopeIsThePrincipal` |
| 5 | Deny when ctx carries no caller | `TestSpawnWithoutCallerIdentityKeepsSharedScope` |
| 6 | Return `ErrIdempotencyConflict` to a foreign principal instead of a new run | `TestSpawnForeignPrincipalIsIndistinguishableFromFirstUse` |
| 7 | Compose the scope as `sessionID + ":" + role + ":" + key` | `TestSpawnKeyNamespaceIsUnambiguous` |
| 8 | Namespace an **empty** key as well | `TestSpawnWithoutCallerIdentityKeepsSharedScope` would not see it; `TestIntegration_SpawnIdempotencyAcrossCoordinators` would not either. **Needs its own assertion:** `TestSpawnWithoutCallerIdentityKeepsSharedScope` gains a case asserting that two unkeyed spawns produce **two** runs and two distinct handles. |
| 9 | Put a storage or host-language term in the `idempotency_key` description | `TestSessionToolSurfaceIsProjectAndLanguageGeneric` |

Every mutation above is observable by the test named against it, checked case by case
— #2 and #6 in particular are the ones whose named tests must be able to tell "new
run" from "error" and from "owner's run", so all three assert on the returned `runID`
and the task set, not merely on `err == nil`. Mutation #8 is listed because writing
the table exposed that no existing or proposed test could see it; that is the reason
the row names the assertion to add rather than an existing test.

Mutations #1 and #2 are the regression proofs and must be recorded in the commit body
as `Regression: INV-AG-16` (rule 20, "Regression Tests").

## 8. Invariant registration

**`INV-AG-16`, not 13.** When this plan was written the manifest's lowest free id was 13, but plan `13` §6's run fence was registered retroactively as `INV-AG-13` on 2026-07-30 (it had shipped with no manifest row), and plans `23` and `24` hold 14 and 15. Neither `scripts/validate_invariants.py` nor `scripts/invariant_coverage.py` parses invariant ids, so a collision passes every gate silently. Re-read `.mivia/invariants.md` and take the lowest free id above 12 at the moment of landing rather than trusting this number.


`.mivia/invariants.md` holds `INV-AG-1`…`INV-AG-7`, `INV-AG-9`, `INV-AG-10`,
`INV-AG-11`, `INV-AG-12`. **`INV-AG-8` is absent — a gap, not a free slot; do not
reuse it.** The next free id is **`INV-AG-16`**. Re-verified by reading the file.

New row, Agent Loop table:

```
| INV-AG-16 | Safety | A run's request fingerprint describes only the work that was requested, and an idempotency key resolves only within the principal that created it. The digest covers an explicit projection of the requested work (task id, handler, dependencies, input, timeout, budget, scope, permission) and never the caller's session, turn, role, depth, owner or dispatcher keys — a digest that varies with the caller cannot deduplicate, which is why `spawn_agent`'s key silently failed on every turn after the first. Identity is a scope instead: the key is namespaced by a fixed-length digest of the caller's session and role, so a principal presenting another principal's key receives a NEW run rather than that run or an error, keeping unauthorized and unknown indistinguishable on the creation path too. The scope is process-local by construction and never persisted — `CreateRun`'s key is not marshalled and replay re-enters with an empty key, so the idempotency index does not survive a restart and no authority reaches the ledger file (see `internal/ledger/types.go`). A caller with no identity keeps the shared scope, so direct and cross-coordinator idempotency still work | `TestSpawnAgentIdempotencyKeyDedupesAcrossTurns`, `TestSpawnIdempotencyKeyDedupesAcrossTurns`, `TestSpawnFingerprintIgnoresCallerIdentity`, `TestSpawnFingerprintCoversRequestedWork`, `TestSpawnConflictStillReportedForDifferentWork`, `TestSpawnIdempotencyScopeIsThePrincipal`, `TestSpawnForeignPrincipalGetsANewRun`, `TestSpawnForeignPrincipalIsIndistinguishableFromFirstUse`, `TestSpawnKeyNamespaceIsUnambiguous`, `TestSpawnWithoutCallerIdentityKeepsSharedScope` | 2026-07-30 (plan 22) |
```

**Amend `INV-AG-9`** — it is the run-ownership invariant and currently scopes itself to
*handle access*: "Orchestration run handles are accessible only to the creator's
session principal … Read-only run tools are in scope". The creation path is not
mentioned, which is exactly how `20` C6 found this gap. Append:

> Run **creation** is in scope too: `spawn_agent`'s idempotency key resolves only
> within the principal that created it, so an idempotent replay cannot return another
> principal's run or its content references, and a foreign key behaves as a first use
> rather than as a conflict — see INV-AG-16.

`INV-AG-10` — **unchanged.** No reference is minted, omitted or reformatted.
`INV-AG-12` — **unchanged.** `ledger_read` stays unscoped; this plan closes the path
by which a foreign principal would *obtain* a reference, and deliberately does not
touch what `ledger_read` does with one it already holds. Those are different
questions and `20` decided the second.

**`Makefile:130` must change.** `scripts/validate_invariants.py` requires every
backticked `Test*` name in the manifest to (a) exist as a `func Test…` and (b) be
selected by the `-run` regex in the `invariants:` target. The regex already contains
`TestSpawnResultPayloadRecoveredRunUsesStoredRefs` as a literal, which does **not**
match any name above. Add a single alternative `TestSpawn` — consistent with the
existing prefix style (`TestRunHandle`, `TestLedgerRead`, `TestListRunEvents`). It
additionally selects three existing fast tests (`TestSpawnAgentWaitRunReturnsTaskOutput`,
`TestSpawnRefusesConcurrentRunID`, `TestSpawnResultPayloadRecoveredRunUsesStoredRefs`)
and makes the last literal redundant but harmless. Changes #8 and #9 land with Wave 4,
after the tests exist, or `make validate-invariants` fails.

## 9. What this does NOT solve

Flatly, because a plan whose title says "scope" invites more credit than it earns.

1. **It closes no reachable hole.** §1a: one principal per process, and `spawn_agent`
   is the only key-supplying caller and refuses to run without a principal. Problem B
   is a landmine, not a leak. The live value of this plan is Problem A.
2. **`ledger_read` stays unscoped** (INV-AG-12). A reference obtained any other way —
   guessed, pasted, or read from a transcript — still resolves for any caller. This
   plan removes one *acquisition* path, not the oracle.
3. **A sub-agent is still not isolated from its parent** (`multi_step.go:92-93`). The
   scope is the principal, and a sub-agent shares it by design.
4. **Two coordinators sharing one repository with different principals no longer
   share keys.** That is intended, but it is a behaviour change to a tested surface
   (`integration_test.go:396-426` passes only because those callers carry no
   principal), and a future caller that *does* carry one will see different behaviour.
5. **The coupling to `cli.orchestrationPrincipal` is by convention.** If it gains a
   third field, the coordinator's scope silently stops matching it. No cross-package
   test can pin this without closing an import cycle (§7). Doc comments at both sites
   are the whole mitigation.
6. **Cross-process idempotency remains impossible, and this plan makes it harder to
   add.** §1b proves the key index is not durable. If it were ever persisted, the
   scope would have to be persisted with it — and a `SessionID` is fresh per process,
   so a persisted scope would never match, while a *durable* principal is what
   `internal/ledger/types.go:105-124` forbids. Cross-process idempotency and
   principal scoping are mutually exclusive under the current ledger doctrine. That
   is a real design constraint this plan takes on deliberately, not an oversight.
7. **A workspace skill-config change invalidates keys minted before it**, because
   `Permission` is in the digest (§6). The failure is an honest
   `ErrIdempotencyConflict`, not a wrong result, and it cannot happen mid-process.
8. **`Depth` leaving the digest means two requests at different nesting depths would
   dedupe.** Unreachable today (`spawn_agent` is denied to sub-agents) and benign if
   it became reachable: a replay executes nothing, so no depth cap is bypassed.
9. **A non-terminal recovered run still reports as fine.** `watchRecoveredRun` sets
   `result.Err = errRecoveredRunNotResumable` (`recovery.go:66-68`), and
   `spawnResultPayload` (`orchestrate.go:226-241`) never reads `result.Err`. Adjacent,
   pre-existing, and out of scope — but a *more* reachable consequence of a working
   idempotency path than anything in §1's Problem B, and worth its own fix.
10. **Nothing about content retention.** `DeleteRun` removes no content on either
    backend (`20` §9). Still the plan worth writing next.
11. **Nothing about task ordering, redaction, or run-handle retention.** Untouched.

## 10. Plan scorecard

| Criterion | Verdict |
|---|---|
| Compiles (no import cycles) | PASS — all production edits in `internal/coordinator`, which already imports `internal/runtime`; test-import graph checked directly per §7, not inferred from `go build` |
| No breaking API change | PASS — no exported signature changes; `requestFingerprint` is unexported. **The persisted `RequestFingerprint` value format does change** — see §4's data table, where it is inert |
| Testable in isolation | PASS — `requestFingerprint` and `scopedKey` are pure; the coordinator takes a `LedgerRepository` and the memory backend is the double; principals are injected with `runtime.ContextWithCaller` |
| Backward-compatible config | PASS — no config keys, no new surface. `DisableTools` remains the off switch for `spawn_agent` |
| Backward-compatible **data** | PASS — §4. No schema change, no migration mechanism needed; pre-change fingerprints are never compared because the key index is not durable (measured) |
| Every function has a test | PASS — §5 pairs each production task with a preceding test task; `requestFingerprint`, `idempotencyScope` and `scopedKey` each have direct coverage |
| Security tests present | PASS — four negative tests (`TestSpawnForeignPrincipalGetsANewRun`, `…IsIndistinguishableFromFirstUse`, `TestSpawnKeyNamespaceIsUnambiguous`, `TestSpawnIdempotencyScopeIsThePrincipal`), and §7 states which of them passes today for the wrong reason |
| `--strict` structure gate | PASS — measured in §4; `spawn.go` 174 → ~215 against a 500 soft cap, and the `internal/cli` 800-line-including-tests cap constrains only where change #5's test goes |
| Rule `60` satisfied | PASS **only with change #6 worded generically** — the `idempotency_key` description is model-facing and covered by `TestSessionToolSurfaceIsProjectAndLanguageGeneric`; proposed wording names no storage engine, table, language or module (mutation #9) |
| Cost proportionate to harm | **PARTIAL, and stated as such.** Problem A's cost is proportionate — a documented parameter is broken. Problem B's half buys nothing reachable today (§1a, §9.1); it is justified only as the precondition for fixing A safely (§5's wave-order note), not on its own severity |

## 11. Rollback criterion

Kill or reduce this plan if:

- **The scope cannot be made unambiguous or cannot be threaded through all four uses
  of the key** (`lookupHandle`, `recoverByIdempotencyKey`, `CreateRun`,
  `newRunHandle`/`evictHandleAfterTerminal`). Then take **§3 E** — remove `TurnID`
  only, leaving `SessionID` in the digest as the gate — and register the accidental
  gate explicitly under INV-AG-16 with its own mutation proof. Do **not** ship Wave 1
  alone: Wave 1 alone is the measured Problem B.
- **A missing principal has to become a denial** for the gate to hold. It does not
  under D, and if a revision makes it necessary, that revision is `20` A″ wearing a
  different hat and `20` C3 already priced it. Take **§3 C** instead.
- **`Permission` in the digest causes real conflicts in practice** (a workspace whose
  skill permissions change while keys are in flight). Drop it from the projection and
  record the §6 trade-off as decided the other way; do not add a fallback that tries
  the digest both ways, which is `19` §5's rejected truncated-key fallback in another
  costume.
- **Cross-process idempotency becomes a requirement.** Then §9.6 is the blocking
  constraint and the plan to write is durable-principal-versus-durable-key, not an
  increment on this one. Do not persist the composed key.
- **§1a stops holding — a second principal becomes real in one process** (concurrent
  sessions, a served mode, anything that calls `chat.NewSession` more than once).
  Then Problem B is live, this plan's priority rises rather than falls, and its
  scorecard's last row flips from PARTIAL to PASS. That is the one change that makes
  this plan urgent, and it is also `20` §11's reopening condition — the two plans
  share it.

In every surviving case, **change #7 (the docs correction) lands regardless.**
`docs/product/agent.md:144-149` currently documents behaviour the code does not have,
and that is true whether or not any code in this plan is written.
