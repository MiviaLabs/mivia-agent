# 20 — Scope content reads to their principal

**Status:** VALIDATED 2026-07-30 — **DO NOT BUILD. Decision changed from §3 A″ to
§3 D (accept and document).** The defect in §1 is real and every empirical claim
supporting it holds. A″ is withdrawn because it buys nothing against the threat
model §1c establishes and costs a measured availability regression on a shipped
guarantee. See corrections C2 and C3.
**Date:** 2026-07-30
**Depends on:** `19` (IMPLEMENTED — §4 **B**, `ledger_read`/`list_run_events`; INV-AG-10).
**Blocks:** nothing. **Composes with:** nothing.
**Blast radius:** ~~MEDIUM~~ **NONE.** No production code changes. The only edit is
`.mivia/invariants.md`: register the accepted limitation as INV-AG-12 and correct
INV-AG-9, which currently asserts a rule `ledger_read` does not follow.
**Commit:** `docs(ai): register unscoped content reads as an accepted limitation`
**Proposed commits (withdrawn):** ~~`fix(agent): route model-visible content references through one discloser`~~, ~~`fix(agent): scope content resolution to the principal that was handed the reference`~~

---

## Corrections found during validation

Every empirical claim in this plan was independently re-verified against the code.
Most held — §1, §1b, §1c, §3 A's rejection, §3 B's rejection, §4's file-size
budget and §8's id arithmetic are all correct as written. These did not hold, and
two of them reverse the decision. Where a citation drifted by a few lines it is
noted at the end rather than inline.

- **C1 — §4's change table over-counts the emit sites. There are THREE choke
  points, not five, and `19` already built the funnel for three of the plan's
  five rows.**
  `storedResultRefs` (`internal/cli/orchestrate_lifecycle.go:101-114`) landed with
  `19` and already funnels the plan's changes #2-first-half, #4 and #5:
  `modelTaskResults` calls it at `orchestrate_lifecycle.go:41`, `encodeResults` at
  `dispatch.go:295`, `delegateResultPayload` at `delegate.go:164`. Both
  `modelTaskResults` and `encodeResults` now take `[]ledger.TaskSnapshot` and
  obtain references from the task record rather than minting them locally, exactly
  as the task prompt suspected. The only sites that read
  `task.OutputRef`/`task.ErrorRef` outside that helper are
  `persistedTaskResults` (`orchestrate_lifecycle.go:63`) and `inspect_agents`'
  `taskInfo` (`orchestrate.go:368-369`). Full sweep:
  `grep -rn 'OutputRef\|ErrorRef' --include=*.go internal/ cmd/ | grep -v _test.go`
  returns nothing else in a model-visible payload. So the plan's changes #2, #4 and
  #5 collapse to one edit inside `storedResultRefs`, and #3 plus half of #2 remain
  — three call sites in two files. **This makes A″ cheaper than the plan thought,
  and it does not save it: see C2 and C3.**

- **C2 — §4's table misses a fourth ref-emitting expression, and it is one no
  field-level structural guard can see. §2's "one discloser" corollary is
  therefore not enforceable as stated, which is change #9's whole job.**
  `recoveredTaskError` (`internal/coordinator/recovery.go:297-302`) formats a
  content reference *into an error string*:
  ```go
  return fmt.Errorf("recovered task %s: %s (error content reference %s)", taskID, status, errorRef)
  ```
  That error becomes `subagents.Result.Err`, and all three live result encoders
  copy `r.Err.Error()` into the model-visible `error` field verbatim —
  `orchestrate_lifecycle.go:47`, `dispatch.go:298`, `delegate.go:167`. It is masked
  today, and only by accident of a different check: `resultsFromSnapshots`
  (`recovery.go:276-292`) stamps `Provenance.Kind = "recovered"` on **every**
  result and always returns exactly `len(snap.Tasks)` of them, so
  `allResultsRecovered` (`orchestrate_lifecycle.go:71-84`) is unconditionally true
  on the only path that can carry a recovered result, and `runTaskResults` takes
  the `persistedTaskResults` branch, which omits `Error` entirely. Unmasking it is
  a one-line change in a different package.
  A guard of change #9's shape inspects writes to ref-shaped *fields*. A reference
  interpolated by `%s` into a string in `internal/coordinator` is outside its
  reach, and there is no formulation of the guard that is both airtight and
  maintainable — banning digest-shaped substrings in model-visible error text
  would fail on legitimate prose. §2's corollary can be enforced for the three
  sites in C1 and cannot be enforced for this one.

- **C3 — A″ is a measured availability regression on the SQLite backend, on a
  shipped first-class workflow. §3 A″'s third "Against" bullet understates it as
  something that *can* happen; it is the ordinary case.**
  The chain, each link verified:
  1. Tool result bodies are persisted verbatim in session history. `SessionStore`
     writes every `provider.Message` as JSONL (`internal/chat/persistence_io.go:25-28`),
     and a tool result is a `Role: "tool"` message whose `Content` is the tool body
     — including the `output_ref` / `error_ref` strings.
  2. Reopening a session restores those messages and keeps the process's
     `SessionID`. `Session.Load` (`internal/chat/persistence.go:256-286`) replaces
     `s.Messages` wholesale and never touches `s.SessionID`; `Session.Clear`
     (`session.go:143-145` → `resetSystem`, `:125-139`) likewise. `SessionID` is
     minted once, at `session.go:119`. So the *new* process has a *new* principal
     holding *old* references.
  3. On SQLite the bytes survive the restart, so those references resolve **today**.
     Measured over a real file, one repository closed and a fresh one opened:
     ```
     PROBE ref for 'context deadline exceeded' = ref:error:05e510230f2518b842597cecaaf0106c48892735f561e532b9c3ab4ffa46c72d
     PROBE cross-process(sqlite) LoadContent: data="context deadline exceeded" err=<nil>
     PROBE fresh memory repo LoadContent: err=content not found
     ```
  Under A″ every one of those references answers `unknown_reference`, because the
  disclosure set is process-local by design and nothing in this process disclosed
  them. That is exactly the trade §11's first rollback criterion forbids: a
  low-severity confidentiality gap exchanged for a correctness regression on
  INV-AG-10's shipped guarantee. On the default memory backend there is no
  regression — the content does not survive the restart either — only a status
  change from `not_found` to `unknown_reference`.

- **C4 — §1's equality oracle is real, and thinner than §1 implies. The plan's
  own claim about which references are guessable is incomplete in the *safe*
  direction.**
  Confirmed real: the key is `sha256(content)` and `persistResultContent` stores
  `r.Err.Error()` (`record_results.go:78`), so a stored error digest is an
  installation-independent constant — measured above,
  `sha256("context deadline exceeded")` is the same value in every mivia store
  that ever recorded that string. Enumerating the vocabulary rather than asserting
  it is small: the parameterless forms reachable at
  `subagents.Result.Err` are `context canceled` and `context deadline exceeded`
  (`subagents.go:181,190,286`), `dependency <id> failed` (`subagents.go:143`, with
  a caller-chosen id), `multi-step subagent "<handler>": {context canceled |
  context deadline exceeded | empty task prompt | invalid task input: <json error>}`
  (`multi_step.go:49,54,57`), `agent exceeded max_steps (N)` (`agent/loop.go:179`),
  `nil completer`, `nil tools` (`loop.go:162,165`), and provider errors, which are
  not low-entropy. So a few dozen guesses cover the closed forms — directionally
  the plan is right.
  What §1 misses, in the plan's favour and against its own severity case:
  **a failed task still gets a high-entropy output reference.** `buildResult`
  (`internal/subagents/multi_step.go:122-149`) returns a non-empty payload
  *alongside* the error, and that payload carries `"elapsed"` at millisecond
  resolution, so `ref:output:` is unguessable even for failures. The oracle is
  confined to `ref:error:` and, within it, to the closed forms, and each guess
  yields exactly one bit: "was this exact string ever recorded in this store."
  Nothing about which run, which task, or when. Given §1c, that store's contents
  were recorded by the same single principal, or — on SQLite — by the same user's
  earlier processes in the same workspace.

- **C5 — §3's trichotomy preserves the letter of `19` §3's second corollary and
  narrows the capability that corollary existed to protect. The plan does not
  record the second half.**
  The table is right: with A″, `not_found` is reachable only for a disclosed
  reference, so it still means "the bytes are absent" and the corollary is
  genuinely strengthened. But `19` §1 and §8 name the tool's *most valuable
  answer* as proving that a reference is a dead pointer, and after A″ that answer
  exists only for references disclosed in this process. Every reference from a
  reloaded transcript (C3) and every reference an operator pastes from a log
  answers `unknown_reference`, which by construction says nothing about presence.
  The corollary survives; the diagnostic does not.

- **C6 — §1a's "seventh case" is the wrong one, and the path the table actually
  misses is closed only by accident.**
  Two findings.
  *Resume is not a leak because it is not reachable.* `ResumeInterruptedRun` has
  **no production caller** — `internal/coordinator/types.go:52` declares it on the
  interface and nothing invokes it outside tests. So §1a's paragraph about a
  recovered run exposing a previous process's `OutputRef` values describes a
  surface that does not exist yet, not "`15`'s resume surface working as designed."
  *The path the table misses is `spawn_agent`'s idempotency replay.* `Spawn`
  resolves an existing run by key with no principal check whatever —
  `lookupHandle` (`spawn.go:106-113`) and `recoverByIdempotencyKey`
  (`recovery.go:26-57`) key on the idempotency key alone — and `spawnResultPayload`
  hands that run's task references straight back, with no
  `orchestrationHandleAccessible` gate, because spawn is the creation path.
  `storeOrchestrationHandle` correctly refuses to overwrite the original owner
  (`orchestration_state.go:72-75`), which protects `inspect_agents` but not this
  return value. What closes it: `requestFingerprint` (`spawn.go:97-104`) marshals
  the whole `[]subagents.Task`; `subagents.Task` carries `SessionID` and `TurnID`
  with no JSON tags (`subagents.go:14-30`); and `buildSpawnTasks` stamps
  `caller.SessionID` (`orchestrate_spawn_tasks.go:55`). A different principal
  presenting the same key therefore gets `ErrIdempotencyConflict`. A real gate, in
  the right place, achieved by an incidental property of a digest computed for a
  different purpose. Worth knowing; not worth a plan.

- **C7 — the residual benefit §9 invites the reader to weigh is, today, empty.**
  §9 already concedes A″ does not isolate sub-agents, does not touch direct store
  access, and adds no retention. Adding §1c (one principal, confirmed) and C4, what
  remains is: an agent can no longer confirm, one bit per guess, whether a
  parameterless error string was ever recorded by *itself* (memory backend) or by
  *the same user's earlier processes in the same workspace* (SQLite). On the
  default backend the reader and the writer of that content are the same agent in
  the same session. There is no principal for the gate to defend against, and §1c
  says so; the plan then proposes the gate anyway. That is the gap C3 prices.

**Confirmed as written**, with own evidence:

- **§1's core defect.** `ledgerReadTool.Execute` is `ledger_tools.go:98-154`:
  unmarshal, empty check, `ledger.ParseReference` (`:112`), `t.repo.LoadContent`
  (`:122`). No `principalFromContext`, no `runHandles` lookup, no
  `orchestrationHandleAccessible`. `list_run_events` gates at `:339-352` with
  exactly the INV-AG-9 comment the plan quotes — **cited line-exact**. `ledger_read`
  is the only run-scoped read surface in the file with no gate.
- **§1's store claims, on both backends.** `defaultOrchestrationRepo` is a package
  var (`orchestration_state.go:29`). Memory content is a flat `map[string][]byte`
  keyed by ref (`memory_claims.go:34-54`). SQLite DDL is
  `content (ref TEXT PRIMARY KEY, data BLOB NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`
  (`internal/storage/store.go:269-271`) — no run column, no principal column; reads
  are `SELECT data FROM content WHERE ref = ?` (`:449`, **line-exact**).
- **§1's retention claim, and stronger than stated.** `MemoryLedgerRepository.DeleteRun`
  (`memory.go:349-362`) deletes the idempotency index and the run record and never
  touches `m.content`. `StorageLedgerRepository.DeleteRun` (`storage.go:437-442`)
  *delegates to `s.mem.DeleteRun`*, so on SQLite it deletes nothing from disk at
  all. There is no `DeleteContent`, no expiry, no pruning: `grep -rn
  'DeleteContent\|retention\|expire\|prune\|VACUUM' internal/ledger internal/storage`
  returns only `VACUUM INTO` in `Backup`. Measured on both:
  ```
  PROBE memory after DeleteRun: content="x" err=<nil>
  PROBE sqlite  after DeleteRun: content="y" err=<nil>
  ```
- **§1's `bytes` field.** `Bytes: len(data)` at `ledger_tools.go:148`,
  pre-truncation and pre-redaction — **line-exact**.
- **§1a path 2.** `inspect_agents` calls `orchestrationHandleAccessible` at
  `orchestrate.go:342` before building any `taskInfo` — **cited range correct** —
  and is `Privileged()` (`:303`).
- **§1a path 3.** `runEventInfo` is `ledger_tools.go:376-383` and has no ref field
  — **line-exact**. `selectRunEvents` never reads `event.Payload`.
- **§1a path 4.** `grep -rn 'OutputRef\|ErrorRef\|output_ref\|error_ref\|ref:'
  internal/events internal/agent internal/audit internal/chat internal/runtime`
  returns **nothing**. Confirmed.
- **§1a path 6.** `defaultStorePath` is `os.UserCacheDir()/mivia/workspaces/<hash>/orchestration.db`
  (`internal/config/defaults.go:68-80`), outside the workspace; `read_file` is
  workspace-confined (`internal/tools/read.go:15-16`); no allowlist is compiled in
  (`60` §4). Unreachable by `read_file`, reachable by `run_command` only under a
  workspace allowlist that grants a path reader.
- **§1b.** `MultiStepHandler` copies `SessionID` and `Role` into the child loop's
  options at `multi_step.go:92-93`; `runThroughCoordinator` stamps the caller's
  `SessionID` onto every task at `orchestrate.go:36`; `orchestrationPrincipal` is
  `{sessionID, role}` at `orchestration_state.go:42-45`. A principal gate cannot
  isolate a sub-agent from its parent. Confirmed. *Also confirmed:* every
  ref-emitting tool is `Privileged()` (`delegate.go:52`,
  `orchestrate_lifecycle.go:127,214`, `orchestrate.go:77,303`, `dispatch.go:62`) and
  on `restrictedRegistry`'s denylist (`multi_step.go:238-242`), so a sub-agent can
  never obtain a reference from a tool — only from a prompt its parent wrote.
- **§1c.** `chat.NewSession` mints `SessionID` once (`session.go:119`); `Clear`
  (`:143-145`) and `Load` (`persistence.go:279-285`) both leave it untouched;
  `openSessionByName` calls `Load` on the same object (`welcome.go:262`).
  `orchestrate.go:30` is the one synthesiser of a second principal, and runs
  created under it are unreachable by everyone. One principal per process,
  confirmed — **and this is the finding that decides the plan (C7).**
- **§3 A's rejection.** `runHandles` entries really expire: `storeOrchestrationHandle`
  (`orchestration_state.go:71-103`) waits on `record.handle.Done()`, then a
  `retention` timer, then `runHandles.Delete`. Default is 10 minutes, in two places
  (`:77` and `orchestrationHandleRetention`, `:110-115`). Gating content on a run
  handle would therefore stop resolving a reference the model still holds. Also
  confirmed: references are content-addressed, so the ref→run relation is
  many-to-many and "its owning run" is not well defined.
- **§3 B's rejection.** `internal/ledger/types.go:105-118` carries exactly the
  quoted decision ("Permission, scope, role, session and turn are deliberately
  absent…"), and `SessionID` is not restored on resume — `tasksFromSnapshots`'
  contract comment is `recovery.go:342-353`, naming `SessionID` at `:344`
  (**line-exact**).
- **§3 B's schema claim.** `OpenSQLite` issues four pragmas
  (`journal_mode`, `synchronous`, `foreign_keys`, `busy_timeout`,
  `internal/storage/store.go:247`) and three `CREATE TABLE IF NOT EXISTS`
  (`events`, `run_claims`, `content`, `:253-275`). No `PRAGMA user_version`, no
  version table, no migration runner: `grep -rn 'user_version\|schema_version\|migrat'
  internal/` finds only an unrelated TUI comment and
  `storage_schema.go:353`, which documents the absence.
- **§4's file-size claim.** `internal/cli/ledger_tools.go` is **451 lines** against
  the `--strict` 500 ceiling — exact, not drifted. The 800-line
  `internal/cli/structure_test.go` cap has headroom (largest file in the package is
  `delegation_test.go` at 691).
- **§8's id arithmetic.** `.mivia/invariants.md` has `INV-AG-1`…`INV-AG-7`,
  `INV-AG-9`, `INV-AG-10`, `INV-AG-11`; `INV-AG-8` is absent; `INV-AG-12` is free.
  Plan `21` landed by *amending* `INV-AG-11` and consumed no id. Confirmed.
- **A″ would not have bricked the tool.** Worth recording, since it was the
  cheapest way for the plan to fail: a real model tool call does carry a principal.
  `internal/agent/loop_tools.go:404-416` passes `opts.SessionID` into
  `Dispatcher.Invoke`, which installs a `Caller` in the call context
  (`internal/runtime/dispatcher.go:317-322`). `contentRefAdmitted`'s
  deny-on-missing-principal rule would not have denied ordinary reads.

**Mutation proof for the accepted limitation.** The invariant row registered below
names `TestLedgerReadWorksOnMemoryBackend` as the pin for "content reads are not
principal-scoped". That test's stated purpose is backend independence, so it was
checked rather than assumed: a deny-on-missing-principal gate was inserted between
`ParseReference` and `LoadContent`, and the test fails.

```
--- FAIL: TestLedgerReadWorksOnMemoryBackend (0.00s)
    ledger_tools_test.go:60: ledger_read returned {"ref":"ref:output:88e066c6...","status":"unknown_reference"}
--- FAIL: TestModelVisibleOutputRefResolves (0.00s)
    ledger_tools_test.go:143: output_ref handed to the model did not resolve through ledger_read: {...,"status":"unknown_reference"}
--- FAIL: TestModelVisibleErrorRefResolves (0.00s)
--- FAIL: TestLedgerReadReportsNotFoundForAbsentContent (0.00s)
--- FAIL: TestLedgerReadRedactsOutput (0.00s)
--- FAIL: TestLedgerReadTruncatesLargeContent (0.00s)
--- FAIL: TestLedgerReadKeepsFramingUnderResultCap (0.00s)
--- FAIL: TestLedgerReadRedactsBeforeTruncating (0.00s)
```

The mutation was reverted from a `/tmp` copy, not from git; `md5sum` re-matched and
the suite went green again. Note the second and third failures: `19`'s two
load-bearing INV-AG-10 tests fail under any principal gate, which is the plan's
own §4 change #8 ("update existing tests to disclose first") seen from the other
side.

**Citation drift** (claim correct, line wrong): `ledger_read`'s ungated body
`ledger_tools.go:98-131` → the function is `98-154`, the ungated region `112-131`;
`memory_claims.go:34-53` → `34-54`; `store.go:269-272` → `269-271`;
`memory.go:334-347` (`DeleteRun`) → `349-362`; `multi_step.go:88-93` → `92-93`;
`orchestrate.go:26-36` → `25-39`; `welcome.go:218-231` → `218-230`;
`orchestration_state.go:76-102` → `71-103` plus `110-115`;
`modelTaskResults` `orchestrate_lifecycle.go:33-50` → `34-52`;
`encodeResults` `dispatch.go:281-308` → `276-325`;
`taskInfo` `orchestrate.go:358-372` → `353-371`;
`delegate.go:160-185` → `159-186`; `read.go:16,28` → `15-16`.

**Net effect on the decision.** §1's defect is real and correctly diagnosed. The
plan's rejections of A, B and C all hold on their own terms. What fails is A″: C7
shows it defends against no principal that exists (§1c, the plan's own finding),
C4 shows the surface it closes yields one bit about the reader's own history, C3
shows it costs a measured availability regression on the durable backend for the
ordinary act of reopening a saved session, and C2 shows the funnel that was
supposed to make it safe cannot be made airtight. §11's first rollback criterion
fires on its substance — "a gate that sometimes drops legitimate references is
worse than no gate" — so the plan takes its own escape hatch. **DECISION: §3 D.**
No production code changes. The limitation is registered as INV-AG-12 and
INV-AG-9 is corrected to stop over-claiming (§8, rewritten).

On §11's closing instruction to "keep the disclosure funnel" regardless: it is
already kept. `19` shipped `storedResultRefs`, which is that funnel for all three
minting sites (C1). The two remaining sites read a reference off a task record and
have nothing to mint, so routing them through a pass-through with no gate behind it
would be ceremony — and C2 shows the guard could not be completed anyway.

---

**Two premises corrected up front (original text, still valid):**

- **`03` is CLOSED**, not pending (`.mivia/plans/03-agentkit-embedded-serving.md:3`, and `00` §4 at `00-agent-roles-program-overview.md:89`: "any future plan proposing to embed or serve instruction content is proposing new work, not resuming `03`"). There is no funded path to multi-tenant serving. The cross-tenant escalation is therefore **hypothetical**, and this plan is not justified by it. §1c gives the justification that survives that correction.
- **The principal *is* in scope at the content write site.** `recordRunResults` takes `tasks []subagents.Task` (`internal/coordinator/record_results.go:12`) and `subagents.Task` carries `SessionID, TurnID, Role` (`internal/subagents/subagents.go:16-18`). `t.SessionID` is one identifier away from `persistResultContent`. But §3 B shows that availability is not what makes principal-keying wrong.

---

## 1. The defect

`ledger_read` resolves any reference any caller presents, with no principal gate.

`internal/cli/ledger_tools.go:98-131` is the whole authorization story: unmarshal, `ledger.ParseReference`, `t.repo.LoadContent`. There is no `principalFromContext`, no `runHandles` lookup, no `orchestrationHandleAccessible`. Compare `list_run_events` **in the same file**, `:339-352`:

```go
// INV-AG-9: every run-scoped tool gates on the caller's principal, and an
// unknown run and an inaccessible run must be indistinguishable.
rawHandle, ok := runHandles.Load(params.RunID)
...
if !ok || !orchestrationHandleAccessible(ctx, record, t.dispatcher, t.repo) {
```

`ledger_read` is the only run-scoped read surface in the file with no such gate. `.mivia/invariants.md` INV-AG-9 was extended for `list_run_events` and not for `ledger_read`.

**The store is unscoped by construction, on both backends.**

- Memory: `defaultOrchestrationRepo` is a package var (`internal/cli/orchestration_state.go:29`), one `MemoryLedgerRepository` shared by every session that does not supply its own. Its content is a flat `map[string][]byte` keyed by ref (`internal/ledger/memory_claims.go:34-53`).
- SQLite: `CREATE TABLE content (ref TEXT PRIMARY KEY, data BLOB NOT NULL, created_at TEXT ...)` (`internal/storage/store.go:269-272`). No run column, no principal column. Reads are `WHERE ref = ?` (`:449`).
- **No retention, and `DeleteRun` does not touch content.** `internal/ledger/memory.go:334-347` deletes the run record and idempotency keys; the content map is untouched. The SQLite path is the same shape. Content therefore outlives its run, its session, and — on SQLite — its process, forever.

**The equality oracle.** The key is `sha256(content)`. So `status: ok` versus `not_found` answers "were these exact bytes ever recorded", and the `bytes` field (`ledger_tools.go:148`, deliberately the pre-truncation, pre-redaction length) answers "how long were they". For task *output* this is unguessable. For error text it is not: `persistResultContent` stores `r.Err.Error()` (`internal/coordinator/record_results.go:78`), which is short, templated, and drawn from a small vocabulary. A few hundred guesses cover most of it.

### 1a. Reachability: how an agent comes to hold a reference it was not handed

Six candidate paths. **Two are real.**

| # | Path | Real? | Evidence |
|---|---|---|---|
| 1 | Task results from `spawn_agent` / `dispatch_tasks` / `delegate` | **No — authorized** | `modelTaskResults` (`orchestrate_lifecycle.go:33-50`), `encodeResults` (`dispatch.go:281-308`). These are the caller's own runs. |
| 2 | `inspect_agents` | **No — already gated** | `orchestrate.go:341-344` calls `orchestrationHandleAccessible` before emitting refs. Also `Privileged()`. |
| 3 | `list_run_events` metadata | **No** | `runEventInfo` (`ledger_tools.go:376-383`) has no ref field. Payloads are never returned. |
| 4 | Event payloads, audit metadata, logs | **No** | `grep` for `output_ref\|OutputRef\|ref:` across `internal/events`, `internal/agent` and the audit path returns nothing outside `internal/cli`. |
| 5 | **Guessing** | **YES** | §1's oracle. Cheap against recorded error text; infeasible against output. |
| 6 | **Direct store access** | **Conditionally YES** | On SQLite the store is a file under `os.UserCacheDir()`, outside the workspace. `read_file` is workspace-confined (`internal/tools/read.go:16,28`) so cannot reach it. `run_command` can, **if** the workspace's `run_allowlist` contains anything that reads a path — and no allowlist is compiled in (`60` §4). Where reachable it yields every ref *and every byte*, with no tool-level gate. |

Path 5 is the defect this plan can close. Path 6 is out of reach of any change to `ledger_read` and is named so the plan is not mistaken for a confidentiality boundary it cannot be.

A seventh case looks like a leak and is not: **resume**. A recovered run re-registered in this process exposes task snapshots — including `OutputRef` values minted by a *previous* process under a *different* `SessionID` — through path 2. That is `15`'s resume surface working as designed. Any scheme that blocks it (§3 B) removes a shipped feature.

### 1b. Sub-agents inherit the parent principal — verified

`MultiStepHandler` copies `SessionID` and `Role` into the child loop's options (`internal/subagents/multi_step.go:88-93`), and `runThroughCoordinator` stamps the caller's `SessionID` onto every task (`internal/cli/orchestrate.go:26-36`). `orchestrationPrincipal` is exactly `{sessionID, role}` (`orchestration_state.go:42-45`).

**Consequence:** a principal gate does not isolate a sub-agent from its parent's content, and cannot be made to without a separate provenance concept. Every option in §3 inherits this. `ledger_read` is unprivileged on purpose and remains so.

### 1c. How many principals actually exist in one process today

One, in the shipped CLI. `chat.NewSession` mints `SessionID` once (`internal/chat/session.go:119`); `/clear` resets history and keeps the object (`internal/cli/welcome.go:218-231`); `openSessionByName` calls `Load` on the same object (`welcome.go:262`). `SessionID` is stable for the process lifetime.

The one way a second principal appears is `orchestrate.go:30`: with no caller in context, a fresh `SessionID` is synthesized. Runs created under it are unreachable by *everyone*, so that is an availability wart, not a confidentiality one.

**This is why today's severity is low.** One process, one principal, one local user. The blast radius is the same user's own earlier content — plus, on SQLite, content from that user's previous processes. Under a hypothetical multi-tenant host it would be cross-tenant, but there is no such host planned, and a process-local map (§3 A″) is not what would protect it either.

## 2. Invariant to establish

> Content is resolvable only by a principal this process handed the reference to.

Corollaries:

- **One discloser.** A reference becomes readable at exactly one place — the function that writes it into a model-visible payload. Emitting and recording cannot be separated, or a new emit site silently breaks INV-AG-10. This is `19` §3's "one minter" corollary applied to the read side.
- **Three misses, three answers.** `malformed` means the shape is wrong; `unknown_reference` means this principal was never handed it; `not_found` means **this principal holds it and the bytes are absent**.
- **A gate is not confidentiality.** §1a path 6 and `19` §8's lethal-trifecta exposure survive this plan untouched.

## 3. Options

### A. Resolve the ref to its owning run, then require `orchestrationHandleAccessible`

*For:* Reuses the exact gate `list_run_events` uses. No new state. Works on both backends.

*Against — it breaks INV-AG-10 on a timer.* `runHandles` entries are deleted `retention` after the run completes, default 10 minutes (`orchestration_state.go:76-102`). A ref handed to the model resolves for ten minutes and then stops. `list_run_events` can accept this because the run id's *existence* is what expires; `ledger_read` cannot, because the model still has the ref in its transcript.

*Also against:* refs are content-addressed, so the ref→run relation is many-to-many — two runs producing identical output share one ref. "Its owning run" is not well-defined.

### B. Key stored content per principal

*For:* The strongest form. A miss is indistinguishable from absence with no extra status.

*Against — it contradicts a standing decision.* `internal/ledger/types.go:105-118`: "Only fields describing the WORK live here. Permission, scope, role, session and turn are deliberately absent: the ledger is a file in the workspace and the agent has file tools, so a persisted permission would be a privilege grant the agent could write for itself." Keying content by `SessionID` writes a principal into that store.

*Against — it deletes resume.* `SessionID` is fresh per process and resume deliberately does not restore it (`recovery.go:344`). Every pre-restart ref becomes permanently unreadable.

*Against — the schema change is not free.* There is no schema version table: `OpenSQLite` issues three `CREATE TABLE IF NOT EXISTS` and four pragmas, none of them `user_version`. `CREATE TABLE IF NOT EXISTS` never alters an existing table, so an added column is silently absent on every existing database file and the first `INSERT` naming it fails at runtime, with nothing able to detect the mismatch.

### C. Withdraw `ledger_read`; expose event metadata only

`19` §14 names this as the withdrawal path.

*For:* Closes the oracle completely. Zero new code. Cannot regress.

*Against:* Removes the capability `19` was written to add, over a defect whose demonstrated impact is "an agent can confirm which templated error strings this same user's earlier tasks produced" — strings it can usually reproduce by causing the same failure. `19` §14's own withdrawal trigger was different: routine bulk-reading, not observed.

### D. Accept and document

*For:* Proportionate to §1c. Honest.

*Against:* Leaves INV-AG-9 asserting a rule the sibling tool in the same file does not follow. `19` gated `list_run_events` on the reasoning that "read-only is not an exemption"; that reasoning applies verbatim to content and was not applied.

### A″ (**WITHDRAWN — see C2, C3, C4, C7**). Admit only refs this process disclosed to this principal

Keep the store unscoped. Maintain a process-local, append-only set: principal → the refs this process has written into a model-visible payload for that principal. `ledger_read` admits a ref iff it is in the caller's set.

*For:*
- **Preserves INV-AG-10 exactly.** Membership is granted at disclosure and never expires — strictly better than A.
- **Closes the oracle.** A guessed ref was never disclosed, so it reports `unknown_reference` regardless of whether bytes exist. Path 5 is dead.
- **No schema change, no migration, no version table needed.** Works identically on the default memory backend and on SQLite, because it does not touch the store.
- **Persists no ownership.** The set is in-process memory, so `types.go:105-118` is respected.
- **Resume keeps working.** A recovered run's refs are disclosed through `inspect_agents` (already principal-gated) and become readable at that moment.

*Against:*
- **A new failure mode:** a future emit site that forgets to disclose hands out a ref that no longer resolves — INV-AG-10 broken silently. This is the whole risk, and the reason §4 requires a single discloser plus a structural test mirroring `19`'s `TestReferenceHasSingleMinter`. If that funnel cannot be made airtight, take **D**, not a partial gate.
- **Unbounded growth**, technically: ~75 bytes per ref per principal. 10 000 tasks ≈ 1 MB for the process lifetime. Not capped — a cap would evict a ref the model still holds.
- **Process-local.** A restart resets the set; refs from a previous process report `unknown_reference`. A resumed session whose transcript was reloaded from disk *can* hold such a ref. Document it; do not add a persistence back door.

~~**DECISION: A″.** The only option that narrows the surface without breaking INV-AG-10 (A), contradicting the ledger's no-authority-fields decision and deleting resume (B), or withdrawing a working capability over a low-severity oracle (C). D remains the fallback if §5 Wave 1 cannot land cleanly.~~

**DECISION (validated): D.** A″'s claim to "preserve INV-AG-10 exactly" is false on
the SQLite backend: membership never expires, but the *set* does, at every process
boundary, and the model's transcript does not (C3). Its claim to close the oracle
is true and worth almost nothing, because §1c establishes there is no second
principal and C4 establishes each guess yields one bit about the reader's own
history. Its stated single failure mode — a forgotten disclosure — cannot be
guarded end to end, because a reference also leaves through an error *string*
(C2). A, B and C are rejected for the reasons given above, which all hold. That
leaves D, which the plan itself named as the fallback.

### Resolving the tension in `19` §3's second corollary

`19` §3 requires: **`not_found` means the bytes are absent** — never "you asked with the wrong key shape." Any scoping introduces a second reason a lookup can miss.

**Resolution: do not overload `not_found`. Add a third status.**

| Status | Meaning | Preserves the corollary? |
|---|---|---|
| `malformed reference` | shape is not canonical | yes (already shipped) |
| `unknown_reference` | this principal was never handed this ref | yes — the answer is about *entitlement*, and says nothing about presence |
| `not_found` | **this principal holds this ref and the bytes are absent** | **yes — strengthened** |

The corollary becomes narrower and truer. And `unknown_reference` leaks nothing: it is returned identically whether the bytes exist under another principal or do not exist at all — INV-AG-9's indistinguishability property transposed to content.

## 4. Blast radius and changes — **WITHDRAWN with A″**

> Retained for the record. Changes #1-#9 and #11 are **not implemented**; #10 is
> implemented in the corrected form given in §8. Two structural errors in this
> table are worth keeping visible: it counts five emit sites where there are three
> (C1), and it omits the reference that leaves through an error string (C2).

MEDIUM, confined to `internal/cli`. **The write side does not change**, so §1's finding that the principal is available at `persistResultContent` is recorded and deliberately unused.

**File-size constraint is load-bearing.** `internal/cli/ledger_tools.go` is already 451 lines against the `--strict` 500-line ceiling. The disclosure set and admission helper live in a **new** file; `ledger_tools.go` may gain at most ~10 lines.

| # | File | Change |
|---|---|---|
| 1 | `internal/cli/content_disclosure.go` (new) | The disclosure set, `discloseTaskRefs`, and the admission check. Per §6. |
| 2 | `internal/cli/orchestrate_lifecycle.go:33-70` | `modelTaskResults` and `persistedTaskResults` route refs through `discloseTaskRefs`. |
| 3 | `internal/cli/orchestrate.go:358-372` | `inspect_agents`' `taskInfo` routes through `discloseTaskRefs`. |
| 4 | `internal/cli/dispatch.go:281-308` | `encodeResults` routes through `discloseTaskRefs`. |
| 5 | `internal/cli/delegate.go:160-185` | The `addRef` call sites route through `discloseTaskRefs`. |
| 6 | `internal/cli/ledger_tools.go:98-131` | After `ParseReference`, before `LoadContent`: admit or return `unknown_reference`. |
| 7 | `internal/cli/content_disclosure_test.go` (new) | Unit tests for #1. |
| 8 | `internal/cli/ledger_tools_test.go` | Scoping and status-trichotomy tests; update existing tests to disclose first. |
| 9 | `internal/cli/ref_disclosure_surface_test.go` (new) | Structural guard: every model-visible ref write goes through `discloseTaskRefs`. |
| 10 | `.mivia/invariants.md` + `Makefile:131` | New INV-AG-12 row, amended INV-AG-9/10 rows, new test names. |
| 11 | `docs/product/agent.md` | Document `unknown_reference`; state that references from a previous process do not resolve. |

**Change #9 is not optional**, for the reason `19` §10 gave for its own change #8: the failure mode of A″ is a forgotten disclosure, and only a structural test makes that impossible rather than merely unlikely.

**No config surface.** `DisableTools` remains the off switch.

## 5. Implementation waves — **WITHDRAWN with A″. Not executed.**

Per `.mivia/rules/05-adlc-agentic-development-lifecycle.md` Step 1: one file per task, a test task before each production task, reviewer every 2–3 tasks.

**Wave 1 — the discloser** (must land airtight or the plan reverts to §3 D)
1. `internal/cli/content_disclosure_test.go` — disclosure/admission round-trip; distinct principals do not see each other's refs; empty ref never disclosed; absent caller denied; concurrent disclose/admit under `-race`.
2. `internal/cli/content_disclosure.go` — change #1.
3. `internal/cli/ref_disclosure_surface_test.go` — change #9, written against the *unrefactored* sites so it starts RED.
   *Reviewer checkpoint.*

**Wave 2 — route every emit site through it** (Wave 1 task 3 goes GREEN here)
4. `internal/cli/orchestrate_lifecycle.go` — change #2.
5. `internal/cli/orchestrate.go` — change #3.
6. `internal/cli/dispatch.go` — change #4.
7. `internal/cli/delegate.go` — change #5.
   *Reviewer checkpoint.*

**Wave 3 — the gate** (gating before Wave 2 breaks every legitimate read)
8. `internal/cli/ledger_tools_test.go` — change #8.
9. `internal/cli/ledger_tools.go` — change #6.
   *Reviewer checkpoint.*

**Wave 4 — end-to-end and registration**
10. `TestModelVisibleOutputRefStillResolvesAfterScoping` — the §7 load-bearing test.
11. `.mivia/invariants.md` + `Makefile:131` — change #10.
12. `docs/product/agent.md` — change #11.

**Wave ordering is a correctness constraint, not a preference.** Wave 3 before Wave 2 makes every `ledger_read` return `unknown_reference`.

## 6. API surface — **WITHDRAWN with A″. No symbol below exists.**

`internal/cli/content_disclosure.go`:

```go
// contentDisclosureSet records, per principal, every content reference this
// process has handed to that principal. Membership is granted at disclosure and
// never expires: a reference handed to the model must keep resolving
// (INV-AG-10), so nothing here evicts.
type contentDisclosureSet struct {
	mu   sync.RWMutex
	refs map[orchestrationPrincipal]map[string]struct{}
}

// disclosedContentRefs is process-global for the same reason runHandles is: the
// emitting tool and the resolving tool are different objects on one dispatcher.
var disclosedContentRefs = newContentDisclosureSet()

func newContentDisclosureSet() *contentDisclosureSet

// disclose records refs as readable by principal. Empty refs are ignored.
func (s *contentDisclosureSet) disclose(principal orchestrationPrincipal, refs ...string)

// admits reports whether principal was handed ref by this process.
func (s *contentDisclosureSet) admits(principal orchestrationPrincipal, ref string) bool

// discloseTaskRefs is the ONE place a content reference becomes readable. It
// records the refs for the caller in ctx and returns them unchanged, so a caller
// cannot emit a reference without disclosing it. Callers with no principal in
// ctx get the refs back undisclosed — the reference is still correct, it is
// simply unreadable, which is the safe direction.
func discloseTaskRefs(ctx context.Context, outputRef, errorRef string) (string, string)

// contentRefAdmitted reports whether the caller in ctx may resolve ref. A
// missing principal is a denial, matching orchestrationHandleAccessible.
func contentRefAdmitted(ctx context.Context, ref string) bool
```

`internal/cli/ledger_tools.go` — the only addition, between `ParseReference` and `LoadContent`:

```go
if !contentRefAdmitted(ctx, params.Ref) {
	return jsonPayload(map[string]any{
		"status": "unknown_reference",
		"ref":    params.Ref,
	}), nil
}
```

**Rule `60`:** the `ledger_read` description gains one sentence describing `unknown_reference` in language-neutral terms — "a reference this caller was not given" — naming no storage engine, table, language or module. `TestSessionToolSurfaceIsProjectAndLanguageGeneric` already covers this text.

## 7. Verification — **WITHDRAWN with A″.** No test below was written.

> One mutation from this table *was* run, inverted, as evidence for the accepted
> limitation: mutation #1's opposite — *adding* the gate — must break
> `TestLedgerReadWorksOnMemoryBackend`. It does; the literal output is in the
> corrections section above. That is what makes INV-AG-12's pin non-vacuous.

```bash
go build ./... && go vet ./...
go test ./internal/cli/ -race -count=1
go test ./internal/... ./cmd/... -race
make verify && make invariants
```

**Tests:**

- `TestModelVisibleOutputRefStillResolvesAfterScoping` — **the load-bearing one.** Run a task end-to-end, take the `output_ref` from the tool result the model receives, resolve it as the same caller, assert the bytes come back. Proves the gate did not break `19`.
- `TestLedgerReadRejectsUndisclosedRef` — store content directly, present its ref, assert `unknown_reference` and that no bytes are returned. The §1 defect as a test; fails today.
- `TestLedgerReadRejectsForeignPrincipalRef` — P1 runs a task, P2 presents P1's ref, assert `unknown_reference`.
- `TestGuessedDigestIsIndistinguishableFromAbsent` — the oracle test. A ref whose bytes ARE recorded but were never disclosed, and a ref whose bytes were never recorded, must produce byte-identical responses (`bytes` included).
- `TestLedgerReadDistinguishesThreeMisses` — the three statuses are textually distinct, and `not_found` is reachable only for a disclosed ref.
- `TestSubAgentInheritsParentContentScope` — pins §1b as **intended behaviour** so a later reader does not "fix" it silently.
- `TestEveryModelVisibleRefIsDisclosed` — change #9. Structural.
- `TestLedgerReadScopingWorksOnMemoryBackend` — the default backend.
- `TestContentDisclosureSetIsRaceFree` — `-race`, concurrent `disclose`/`admits`.
- `TestDisclosureWithoutPrincipalDeniesRead` — no caller in ctx ⇒ denial.

**Mutation proofs:**

| # | Mutation | Test that MUST fail |
|---|---|---|
| 1 | Remove the `contentRefAdmitted` gate | `TestLedgerReadRejectsUndisclosedRef` |
| 2 | Make `contentRefAdmitted` ignore the principal | `TestLedgerReadRejectsForeignPrincipalRef` |
| 3 | Return `not_found` instead of `unknown_reference` | `TestLedgerReadDistinguishesThreeMisses` |
| 4 | Include `bytes` in the `unknown_reference` response | `TestGuessedDigestIsIndistinguishableFromAbsent` |
| 5 | Drop `discloseTaskRefs` from any one emit site | `TestEveryModelVisibleRefIsDisclosed` |
| 6 | Admit a ref when ctx has no principal | `TestDisclosureWithoutPrincipalDeniesRead` |
| 7 | Add eviction/TTL to the disclosure set | `TestModelVisibleOutputRefStillResolvesAfterScoping` |
| 8 | Gate scoping on the SQLite backend | `TestLedgerReadScopingWorksOnMemoryBackend` |

Mutations #1 and #2 are the regression proofs for §1 and must be recorded with `Regression: INV-AG-12`.

## 8. Invariant registration — rewritten for decision D

`.mivia/invariants.md` has `INV-AG-1`…`INV-AG-7`, `INV-AG-9`, `INV-AG-10`, `INV-AG-11`. **`INV-AG-8` is absent** — a gap, not a free slot; do not reuse it. **The next free id is `INV-AG-12`.** Confirmed; plan `21` amended `INV-AG-11` and consumed no id.

Under D, `INV-AG-12` records the **accepted limitation** rather than a gate. It is
still a Safety row: the property it states is testable and mutation-provable, and
the reason to write it down is that INV-AG-9 currently reads as though it already
covered content, which is what let this gap go unnoticed through `19`.

Registered (Agent Loop table):

```
| INV-AG-12 | Safety | Content resolution is deliberately NOT principal-scoped: `ledger_read` resolves any well-formed reference any caller presents, so the content digest is an equality oracle over everything the store holds, and content outlives its run — `DeleteRun` removes no content on either backend and there is no retention. Accepted, not overlooked: one principal exists per process, a sub-agent shares its parent's principal by design, and a process-local admission set would break a reference the model still holds across a restart while defending against no principal that exists (plan 20 §1c, C3, C7). The two answers `ledger_read` gives about a reference are therefore about the bytes only — malformed shape, or absent content — and `not_found` keeps meaning the bytes are absent | `TestLedgerReadWorksOnMemoryBackend`, `TestLedgerReadRejectsMalformedRef`, `TestLedgerReadReportsNotFoundForAbsentContent`, `TestLedgerToolsAreUnprivilegedAndReachSubAgents`, `TestListRunEventsRequiresRunOwnership` | 2026-07-30 (plan 20 validated → D) |
```

Why those pins, and why they are not vacuous:

- `TestLedgerReadWorksOnMemoryBackend` resolves a reference with a bare
  `context.Background()` — no caller, no principal — and asserts `status:"ok"`.
  Inserting a deny-on-missing-principal gate makes it fail (literal output in the
  corrections section). It observes the unscoped property directly, even though its
  doc comment is about backend independence.
- `TestLedgerReadRejectsMalformedRef` and `TestLedgerReadReportsNotFoundForAbsentContent`
  pin the **two**-answer contract D keeps, so a future `unknown_reference` cannot be
  added without a deliberate edit here.
- `TestLedgerToolsAreUnprivilegedAndReachSubAgents` pins that the tool reaches
  sub-agents, which is the §1b half of the row.
- `TestListRunEventsRequiresRunOwnership` pins the deliberate asymmetry with the
  sibling tool in the same file, so the row cannot be read as an oversight.

Amend `INV-AG-9` — it currently says "every run-scoped tool gates on the caller's
principal", which `ledger_read` does not do. Append: "Content resolution is
deliberately outside this invariant: a reference is not a run id, it survives its
run handle's retention window, and gating it on a handle would stop resolving a
reference the model still holds — see INV-AG-12."

`INV-AG-10` — **unchanged.** D adds no status and removes no reference, so the
existing text and test list stay exactly as `19` and `21` left them.

`Makefile:131` — **no change needed.** All five pinned names are already selected:
`TestLedgerRead` and `TestListRunEvents` are prefixes in the regex and
`TestLedgerToolsAreUnprivilegedAndReachSubAgents` is listed verbatim. Verified with
`python3 scripts/validate_invariants.py`.

## 9. What this does NOT solve

> Under D, all six items below stand and item 6 is no longer a cost, because no
> disclosure set is added. The list is the severity analysis INV-AG-12 records.
> One addition from validation: **content still has no retention on either
> backend, and on SQLite `DeleteRun` deletes nothing at all** — it delegates to the
> in-memory projection (`storage.go:437-442`), so the run's rows survive on disk
> too. That is a bigger and more concrete defect than the oracle this plan set out
> to close, and it is the plan worth writing next.

Stated flatly, because a gate named "scope content to its principal" invites more credit than it earns.

1. **A sub-agent is not isolated from its parent** (§1b). Untrusted sub-agent output can be read back by anything sharing that principal. Fixing this needs a provenance concept that does not exist and is not proposed here.
2. **Direct store access is untouched** (§1a path 6). A tool-level gate cannot address this; only store-level encryption or confinement could.
3. **Confidentiality of content the caller legitimately holds is unchanged.** `19` §8's second exposure stands verbatim: the `content_is_data` framing remains the only mitigation, and it is a prompt, not a control.
4. **Content still has no retention and no deletion.** `DeleteRun` does not remove content, and the `content` table has no expiry. A″ makes it *unaddressable* by a new process rather than *absent*. Retention is a real defect and a separate plan — it needs the schema-versioning work §3 B priced.
5. **It provides nothing under a multi-tenant host.** A process-local map is not a tenant boundary.
6. **The disclosure set is a new stateful dependency of a shipped guarantee.** Its failure mode — a forgotten disclosure — breaks INV-AG-10 silently. Change #9 is the mitigation and the reason Wave 1 gates the rest.

## 10. Plan scorecard

**Re-scored for D.** The A″ scorecard below is retained; its `--strict` and
rule-`60` rows were correct, and its "Every function has a test" row was the one
that mattered, because C2 shows the *guard* could not be completed even though the
tests could. Under D:

| Criterion | Verdict |
|---|---|
| Compiles (no import cycles) | N/A — no code change |
| No breaking API change | PASS — no API, no new tool status |
| Backward-compatible config / data | PASS — nothing touched |
| Security tests present | PASS — the limitation is pinned by five existing tests, one of them mutation-proved (§8) |
| Cost proportionate to harm | PASS — this is the row A″ failed. C3 priced the cost above the harm |
| Invariants honest | PASS **only with the §8 edit** — INV-AG-9 as written over-claims |

Original A″ scorecard, retained:

| Criterion | Verdict |
|---|---|
| Compiles (no import cycles) | PASS — all changes in `internal/cli`; `orchestrationPrincipal` is already local there |
| No breaking API change | PASS — helpers unexported; one new tool status, additive in the JSON envelope |
| Testable in isolation | PASS — the set is a plain struct; the gate takes a `context.Context`; memory repo is the double |
| Backward-compatible config | PASS — no new keys, no schema change, no migration |
| Every function has a test | PASS — Waves 1–4 pair each production task with a preceding test task |
| Security tests present | PASS — three negative tests (`secure-change`) |
| `--strict` structure gate | PASS **only via change #1's new file** — `ledger_tools.go` is 451/500 and takes ≤10 more |
| Rule `60` satisfied | PASS — one language-neutral sentence, covered by the existing session-surface guard |

## 11. Rollback criterion — **FIRED**

The first criterion fired on its substance during validation, and the plan took its
own escape hatch. What fired it, precisely: the criterion says to take D "if a
forgotten disclosure cannot be made to fail a test." A test over the three real
emit sites (C1) *could* be made to fail — but C2 shows a reference also leaves
through an error string in another package, where no field-level guard reaches it,
so the corollary the guard exists to enforce is not enforceable. And the
criterion's stated reason — "a gate that sometimes drops legitimate refs is worse
than no gate" — is satisfied outright by C3, which measures the gate dropping every
reference in a reloaded transcript on the durable backend.

**What would reopen this plan.** Any one of these changes the arithmetic:

- **A second principal becomes real in one process** — concurrent sessions, a
  served mode, or anything that makes `chat.NewSession` run more than once per
  process. §1c is the entire severity argument; when it stops holding, re-derive
  from A″, not from D. Note that A″ would still need C3's answer.
- **Content gets retention or deletion** (§9, the plan worth writing next). A
  content store that forgets makes the oracle's window finite and would let a
  scoping scheme be keyed on something durable rather than process-local.
- **A durable principal becomes legitimate at the store level.** §3 B is rejected
  on `types.go:105-118` plus the absence of any migration mechanism, both
  confirmed. Fix the second and B is a different conversation.

Original criteria, retained — kill this plan if:

- **Wave 1 cannot be made airtight.** If a forgotten disclosure cannot be made to fail a test, stop and take §3 **D**: register INV-AG-12 as an *accepted limitation* with the §1c severity analysis, and change no code. A gate that sometimes drops legitimate refs is worse than no gate — it trades a low-severity confidentiality gap for a correctness regression on a shipped guarantee.
- **`unknown_reference` proves confusing in practice.** If the model retries or treats it as transient, collapse to §3 D rather than folding it into `not_found`, which would destroy `19` §3's corollary.
- **Disclosure-set memory becomes material.** Do not add eviction (mutation #7). Take §3 **C** — withdraw `ledger_read` — which is `19` §14's own withdrawal path.

In each case keep the disclosure funnel: routing every model-visible reference through one function is worth doing on its own merits, because it is where `19`'s "one minter" discipline belongs on the read side.
