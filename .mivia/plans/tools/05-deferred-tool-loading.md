# tools/05 - Deferred tool loading: search-and-load tool surface

**Status:** IMPLEMENTED (2026-08-02). Steps 0-7 delivered; step 8 (a shipped
default deferred set) deliberately NOT taken - see §9. Design below is the
locked Step 0 record and is retained as the rationale for the implementation.
**Date:** 2026-08-02 (revised after Step 0 rounds 1+2; implementation notes §9)
**Depends on:** 51.05 Stage A (amendment landed - see §9); the durable
persistence migration (re-baselined at implementation: no in-flight migration
existed, so D3 landed against `chat_sessions` + a new v3 admission table);
plan 46 v1 (shipped, observation-only).
**Blast radius:** MEDIUM-HIGH - one new surface primitive
(`widenAgentSurface`), turn-boundary admission hook, durable session
state, INV-CE-05 invariants.

## 0. Step 0 disposition (hostile challenge)

- **F1 (BLOCKER)**: "rebuild between steps, same lifecycle as an agent
  switch" was mechanically impossible three ways: `loop.Run` hoists
  `toolSpecs` once (`loop.go:116`) and never re-reads the registry;
  `binding.go:155` hard-rejects surface changes while `activeTurns > 0`;
  `PublishAgentSurface` bumps `agentSurfaceGeneration` and
  `invalidateLocked()`, fencing the calling turn out of its own
  persistence. -> **Resolved: option (a), turn-boundary admission** (D6).
- **F2 (BLOCKER)**: `buildAgentScopedSurface` reloads skills from disk,
  constructs a new dispatcher, and `Close()`s the old one - the executor
  of the very batch containing `load_tools`. -> **Resolved: dedicated
  `widenAgentSurface` primitive** (D7).
- **F3**: amending INV-CE-05-B to "stable between admissions" gutted it.
  -> **Resolved: admission is a binding-generation event (E-framing)** -
  B survives verbatim (D6).
- **F4**: `sessionMeta` is the legacy path; context-enabled sessions
  never read it, and the durable persistence surface is being migrated in
  the working tree right now. -> **Resolved: single SoT** (D3 rewritten).
- **F5**: the deferred index cannot be both accurate and prefix-stable.
  -> frozen-at-bind index + idempotent `load_tools` (D8).
- **F6**: pending-call-from-deferred-tool impossibility proven
  mid-session; resume hole on digest-mismatch drop closed with a bounded
  system note.
- **F7/F8**: cap counts only admitting calls (+ total-attempt bound 32);
  lexical match = `strings.ToLower` substring over name+description,
  registration order, no locale collation.
- **F9**: stale citation - the no-Allowlist spawned-registry call is now
  `restrictedRegistry()` at `multi_step.go:427` (site :387); guard test
  still missing; `EstimateToolSchemaCost` zero-caller claim re-verified.

### Round 2 (verdict: LOCK conditional on five amendments, all applied)

- **R2-1 (BLOCKER, amendable)**: "finishAgentTurn is the turn boundary"
  was false - it runs **inside** the activeTurns window (before `done()`
  decrements), overlapping force-sent turns exist, and
  `PublishAgentSurface` has no activeTurns guard and unconditionally
  closes the old dispatcher - the round-1 F2 hazard one level up, plus
  epoch-bump fencing a sibling turn out of its own history. ->
  **publish preconditions** in D7.
- **R2-2 (MAJOR)**: D7 bypassed `SetSwitchGuard` - background
  orchestration holding the dispatcher across turns would get it closed.
  -> `CheckSwitchAllowed() == nil` joins the preconditions.
- **R2-3 (MAJOR)**: D3's "replay before first request" had no owner and
  needs CLI-layer machinery `internal/chat` cannot reach. -> named
  CLI replay hook (bindingFactory pattern) + the three load sites.
- **R2-4 (MINOR)**: `ScopedRegistry` filters in **base-registry order**,
  so an admitted tool materializes mid-array and privileged session
  tools shift - "core prefix survives" was overstated. -> D8 commits to
  admitted-tools-appended-as-tail ordering.
- **R2-5 (MINOR)**: pending stages need generation keying (stale stage
  must not apply after an `/agent` switch; correctly survives a model
  switch) and an error-path rule (publish only after durable commit).
- Verified in the plan's favor: post-turn `SaveAfterTurn` captures a
  fresh token, so the generation bump does not fence later autosaves;
  mid-turn `/agent` interleave impossible (`BeginSurfaceSwitch` rejects
  active turns); model-switch preserves `agentSurfaceGeneration`.

## 1. Verified baseline (validation + challenge re-verification)

- Schema bytes ship on every request; `EstimateRequestCost` re-marshals
  every ToolSpec per call (`provider/context.go:76-81`), including inside
  planner loops; hoist helper `EstimateToolSchemaCost`
  (`context.go:107-119`) has zero non-test callers.
- 51.05 Stage A locked, unimplemented; authority SoT =
  `EffectiveTools` via `ScopedRegistry`; no second schema allowlist
  permitted (INV-CE-05-D); advertised set fixed per **agent binding**
  (INV-CE-05-B/E).
- Plan 46 shipped `CacheUsage` observation only; caching is
  implicit-prefix; no breakpoints exist.
- Loop/turn guards as in F1 above. No resume-safe session KV exists on
  the legacy path; durable persistence surface in migration.

## 2. Goal (unchanged)

A small always-present core set plus a `load_tools` discovery surface, so
authorized-but-unused schema-heavy tools stop shipping on every request.

## 3. Locked decisions

**D6 - admission is turn-boundary, framed as a binding-generation event.**
`load_tools` executes inside a turn and therefore only **records intent**:
it validates names/query against the deferred set, stages the admission,
and returns "admitted: [...] - available from your next turn." At
`finishAgentTurn` (after the turn's persistence completes under its own
still-valid fence), the staged admission runs as a surface publication:
`agentSurfaceGeneration` bumps exactly as an `/agent` switch does, so
INV-CE-05-B holds **verbatim** within each generation and admission is an
INV-CE-05-E event - no amendment to B. The 51.05 amendment therefore
shrinks to one sentence: "a binding's successor generation may widen the
tool surface monotonically via host-mediated admission." Cost accepted:
the model finishes the current turn without the new tool; the tool result
says so honestly.

**D7 (amended R2-1/2/5) - `widenAgentSurface(names)` primitive with
publish preconditions.** A narrow function that derives the new registry
via `ScopedRegistry` with `Allowlist = core ∪ admitted` (one authority
path - INV-CE-05-D intact), builds core in base order and **registers
admitted tools as an appended tail** (R2-4/D8), reuses the existing skill
registry/scope (no disk I/O), and publishes under `s.mu` **only when
ALL of**: `activeTurns == 1` (only the finishing turn) AND `!switching`
AND `stage.TurnID == s.turnID` (superseded/force-sent turns never
publish) AND `CheckSwitchAllowed() == nil` (background orchestration
guard, R2-2). Otherwise the stage stays **pending** for the next
qualifying boundary, with a bounded user-visible note after repeated
deferrals. The old dispatcher is `Close()`d only on a publish that
satisfied all four (guaranteeing no sibling turn or background run holds
it). Stages are keyed by the `AgentSurfaceGeneration` captured at staging
and no-op on mismatch (R2-5) - a stage thus survives a model switch
(generation preserved) and dies on an `/agent` switch (generation
bumped). Error path: a stage publishes only after the staging turn's
history is durably committed; on the context path, after
`contextHead` adoption, never in the resync or Commit-failure branches;
an errored/discarded turn drops its stage.

**D3 (rewritten) - single persistence SoT.** Context-enabled sessions
persist the admitted set (names + agent name + digest) in the durable
context store's session state; legacy file sessions persist it in
`sessionMeta`. Never both. Resume replays the D7 rebuild **before the
first request** (not merely "before the first turn"); digest mismatch
drops the set fail-closed and injects a bounded system note naming the
dropped tools (F6). **Replay ownership (R2-3)**: a CLI-registered replay
hook (the `bindingFactory` pattern - `internal/chat` cannot construct
`NewSessionDispatcher`) invoked synchronously from all three load sites
(auto-resume `chat_repl_loop.go:69`, `/load`
`chat_slash_handlers.go:184`, and the TUI load path), which are
turn-free, so publication there is unconditionally safe. This step
re-baselines against the in-flight persistence migration before
implementation.

**D4 - unit is the agent binding** (unchanged): `/agent` switch resets
admissions to that agent's core; persisted set keyed by agent name +
digest.

**D5 - wire the schema-cost hoist as step 0** (unchanged): independent,
produces the schema-mass telemetry 51.05 demands, and the before/after
measurement.

**D8 (amended R2-4) - deferred index frozen at bind; admitted tools
appended as a tail.** Index generated once per binding into the system
prompt (name + one-liner each); prefix-stable by construction; therefore
stale-by-design after admissions. `load_tools` on already-admitted names
is idempotent: free (no cap charge), returns "already loaded" (F5/F7).
Ordering reality (R2-4): `ScopedRegistry` filters in base-registry
order, so naive admission would materialize tools mid-array and shift
the privileged session-tool tail. D7 therefore builds core-in-base-order
+ admitted-as-appended-tail, making the core block byte-stable across
admissions by construction. Cache reality (46 v1 is implicit-prefix):
each admission invalidates the prefix from the first appended tool
onward, once, at a turn boundary; the frozen index keeps system-prompt
bytes stable. Step-2 gate: golden test of the serialized tool block
asserting the CHOSEN order (core block byte-identical across an
admission, admitted tail appended, privileged tail after) - not merely
"registry order".

## 4. Design

### 4.1 Tiers

`[tools] core = [...]` + per-agent `tools_core` override (pointer,
inheritance-preserving). Unset = everything core (plan fully inert).
Deferred = `EffectiveTools` minus core; `load_tools` always core when
anything is deferred.

### 4.2 `load_tools`

```json
{ "query": "web search", "names": ["fetch_url"] }
```

- `names`: exact staging; `query`: `strings.ToLower` substring match over
  name + description, results in registration order, no locale collation
  (F8). Both may appear; one call stages all matches (batched with any
  other staged admissions this turn into one D6 publication).
- Returns staged names + one-liners + "available from your next turn".
- Unknown/unauthorized names: bounded error listing valid deferred
  candidates; never widens authority beyond `EffectiveTools`.
- Caps (F7): 8 admitting publications per binding; failing/idempotent
  calls don't consume it; separate total-attempt bound of 32 per binding
  for the abuse case, bounded error after either.

### 4.3 Subagents

Routed task agents ship with exact tools pre-admitted via the agent
definition; `load_tools` in a subagent is a fallback. Precondition: add
the missing guard test for the no-Allowlist spawned registry
(`restrictedRegistry()`, `multi_step.go:427`, site :387).

## 5. Invariants

- Advertised = invocable at every moment; single authority path
  (INV-CE-05-A/D preserved; B holds verbatim per generation; admission is
  an E event) (D6/D7).
- Admission monotonic across a binding's generations; reset on binding
  change; admitted ⊆ `EffectiveTools` always; resume drops stale sets
  fail-closed with a visible note (D3/D4).
- A turn that stages an admission persists under its own fence; the
  generation bump happens strictly after (D6 ordering).
- Zero config -> byte-identical behavior to today.
- Deterministic: same staged admissions -> same registry -> same
  serialized tool block; system-prompt bytes stable across admissions
  (D8 goldens).
- Calls to unadmitted deferred tools hit the existing unknown-tool path -
  proven impossible to be "pending" mid-session (F6).

## 6. Implementation steps

0. Wire `EstimateToolSchemaCost` + schema-mass telemetry (D5).
1. Draft + land the one-sentence 51.05 amendment (D6 framing); implement
   51.05 Stage A if still unshipped (hard precondition).
2. Guard test for `restrictedRegistry()` spawned scoping (4.3).
3. Re-baseline D3 against the landed persistence migration.
4. Tier config, inert-by-default.
5. `load_tools` staging + `widenAgentSurface` + turn-boundary publication
   + caps (D6/D7, F7).
6. Persistence + resume replay + digest-mismatch note (D3).
7. Frozen deferred index; serializer-order gate; `CacheUsageEvent`
   before/after measurement (D8).
8. Default deferred set (orchestration + web) only if step-0 telemetry
   shows material mass.

## 7. Testing

- Ordering: stage -> durable turn commit -> generation bump -> next
  request carries new schemas (the F1 sequence, now as a passing test).
- No mid-batch dispatcher close: load_tools alongside sibling calls in
  one batch; siblings complete on the original dispatcher.
- Overlapping-turn matrix (R2-1): force-sent sibling turn keeps its
  dispatcher AND its history (no ErrStaleOperation from a sibling's
  publication); superseded turn's stage never publishes.
- SwitchGuard refusal (R2-2): background dispatch_tasks run active ->
  stage defers, publishes at the next allowed boundary; bounded note
  after repeated deferrals.
- Stage lifecycle (R2-5): drop on `/agent` switch (generation mismatch),
  survive model switch, drop on errored/discarded turn.
- Resume replay (R2-3): all three load sites widen before the first
  request; digest mismatch drops with the F6 note.
- Authority/monotonicity/reset/resume matrix incl. digest-mismatch note.
- Goldens: system-prompt byte-stability across admissions; tool-block
  registry-order serialization (step-2 gate).
- Cap semantics: idempotent free, failing calls hit attempt bound only.
- Inertness: no core config -> requests byte-identical to HEAD.
- INV-CE-05 suite unchanged and passing.

## 8. Failure analysis

- Model needs the tool *this* turn: it gets an honest "next turn" result;
  worst case one wasted turn - visible, bounded, and rarer than the
  per-request schema tax it replaces.
- Thrash-loading: publication cap + attempt bound; worst case equals
  today's all-core behavior.
- Amendment rejected: plan dies at step 1 with step 0 (independently
  valuable) shipped.
- Persistence migration changes shape under D3: step 3 re-baseline gates
  implementation; this plan does not lock line numbers there.

## 9. Implementation notes (2026-08-02)

Delivered against the locked design. Deviations and confirmations:

- **Step 0 (D5).** `EstimateToolSchemaCost` is now hoisted: `contextmgr.Plan`
  prices the tool schemas once and passes the charge down through
  `planCompact` / `retainMessages` / `calibratedCost` via the new
  `provider.EstimateMessagesPromptCost`. One plan used to re-marshal every
  ToolSpec up to four times. Schema-mass telemetry (`schemaMass`) is recorded
  at attach, `/agent` switch, and every admission, published as a
  `KindConfigChange` event and shown by `/tools`.
- **Step 1.** The one-sentence amendment landed on INV-CE-05-E in
  `51-harness-context-economics/05-tool-schema-gating.md`. 51.05 Stage A
  itself is a tests+docs+telemetry slice; the invariant tests this plan needed
  from it (spawn-scope guard, advertise/invoke identity under admission) ship
  here instead of being blocked behind it.
- **Step 2.** `internal/subagents/spawned_scope_guard_test.go` pins the
  no-Allowlist `restrictedRegistry()` path: subset-of-input and no privileged
  or delegation leak, including `load_tools` itself.
- **Step 3 (D3 re-baseline).** No persistence migration was in flight at
  implementation time. The durable surface is `chat_sessions` (schema v2), so
  admissions landed as a **new v3 table** `chat_session_admissions` behind the
  optional `contextstate.SessionAdmissionCatalog` interface, rather than by
  widening `SaveSession`/`LoadSession`. Legacy file sessions carry the record
  in `meta.json`. Exactly one of the two is ever written.
- **R2-3 (replay ownership).** All four load sites (auto-resume, `/load`, the
  TUI `/load` handler, and the welcome-screen picker) funnel through
  `Session.Load`, so the replay hook is invoked there once instead of being
  wired per site. The widener itself is still CLI-registered
  (`SetSurfaceWidener`, the bindingFactory pattern).
- **R2-1 (publish preconditions).** Implemented as
  `Session.TryPublishAgentSurface`, which re-checks `activeTurns == 1`,
  `!switching`, the turn id and the surface generation **under the same lock
  acquisition as the swap**. Checking and then publishing would leave exactly
  the gap R2-1 describes. `CheckSwitchAllowed()` (R2-2) is checked immediately
  before, outside the lock, because it calls owner code.
- **Turn-id semantics.** D7 said a stage publishes only when
  `stage.TurnID == s.turnID`. Implemented as `stage.TurnID <= s.turnID` plus
  sole-active-turn: strict equality made "stays pending for the next
  qualifying boundary" unreachable, since a later boundary necessarily has a
  later turn id. Supersession is still excluded - a superseded or errored turn
  drops its stage before the boundary is reached, so a stage that survives to
  a quiet boundary is publishable by construction.
- **D8 ordering.** `tools.ScopedRegistryWithTail` builds the core block in base
  order and appends admitted tools as a tail. Goldens assert the core block's
  serialized schemas are byte-identical across an admission and that the
  privileged session tools stay behind the tail.
- **Step 8 NOT taken.** No default deferred set ships. The gate the plan set
  ("only if step-0 telemetry shows material mass") needs measurements from real
  configurations, which this change makes possible but does not itself produce.
  Zero config remains fully inert.

### Test map

| Plan §7 requirement | Where |
|---|---|
| stage -> commit -> generation bump -> next request carries new schemas | `cli.TestAdmittedToolReachesTheNextRequest` |
| no mid-batch dispatcher close | `cli.TestSiblingToolCallsCompleteInTheSameBatchAsLoadTools` |
| overlapping-turn matrix | `chat.TestSiblingTurnBlocksPublication`, `chat.TestTryPublishAgentSurfaceRechecksAtomically` |
| SwitchGuard refusal + bounded note | `cli.TestBackgroundWorkDefersTheAdmission`, `cli.TestDeferralNotesAreBounded` |
| stage lifecycle (agent switch / model switch / errored turn) | `chat.TestStageDiesOnAnAgentSurfaceChange`, `chat.TestStageSurvivesAModelSwitch`, `chat.TestErroredContextTurnDropsItsStage`, `chat.TestCommitFailureDropsTheStage` |
| resume replay + digest-mismatch note | `chat.TestContextCatalogReplaysTheAdmittedSet`, `chat.TestContextCatalogDropsTheSetWhenTheDigestChanged`, `cli.TestAdmittedToolsSurviveSaveAndLoad`, `cli.TestResumeDropsAStaleAdmittedSetWithANote` |
| authority / monotonicity / reset | `cli.TestLoadToolsRejectsUnauthorizedNames`, `cli.TestAgentSwitchResetsAdmissions`, `subagents.TestRestrictedRegistryDropsPrivilegedAndDelegationTools` |
| goldens: prompt byte-stability, tool-block order | `tools.TestScopedRegistryWithTailKeepsCoreBlockStableAcrossAdmissions`, `cli.TestAdmittedToolIsAppendedAsATail`, `cli.TestAdmittedToolReachesTheNextRequest` (prompt bytes) |
| cap semantics | `cli.TestLoadToolsIdempotentCallsAreFree`, `cli.TestLoadToolsAttemptBoundStopsALoopingModel`, `chat.TestPublicationBoundIsChargedPerBatchNotPerName` |
| inertness | `cli.TestDeferredLoadingIsInertWithoutCoreConfig`, `cli.TestPlanToolTiersWithoutACoreListIsInert` |

## 10. Step 5 bug audit (2026-08-02)

Four hostile auditors (concurrency/lifecycle, authority/invariants,
persistence/migration, plain correctness), then three independent validators
instructed to REFUTE every finding with an executed reproduction. All twelve
survived; two were found independently by two auditors. All are fixed and
regression-tested.

| # | Severity | Defect | Fix |
|---|---|---|---|
| 1 | High | A failed `/agent B` left `state.TierPlan`/`SkillRegFull`/`SkillScope` set to B while `state.Selected` stayed A, so the next admission published **B's core tier under agent A** - advertised *and* invocable. Reproduced: `write_file` went live for a reader agent. Mirror case un-deferred A's entire set. | Surface construction no longer mutates `agentSessionState`; the plan, skill registry and scope ride on `agentSurface` and are committed with `state.Selected`. Plus `tieredRootRegistry` now clamps the core list *and* the admitted tail against the selected agent's authorized set, so a stale plan cannot widen anything. |
| 2 | High | `[tools] core` silently gutted routed sub-agents and skills: the core-only registry was passed as the spawn/skill **authority**, so a routed agent got `rootCore ∩ ownTools` (measured: **zero** tools) and a skill needing a deferred tool was never registered while still being advertised. | `SessionDispatcherOpts.AuthorityRegistry` separates "authorized to execute" from "advertised to the root model". Nil defaults to `Registry`, so every other caller is unchanged. |
| 3 | Major | `replayAdmission` published with none of the D7 preconditions and never called `CheckSwitchAllowed`, so a plain `/load` closed a dispatcher a live `dispatch_tasks` run still owned (wiping completed results and closing its ledger store). | The guard is checked and the surface generation is required before the widener runs. |
| 4 | Major | The resume drop paths cleared `admittedTools` without narrowing `s.Tools`, leaving a surface wider than the state the session reported and persisted - and told the user the tools were "not restored" while they were still live. | All three drop paths republish a core-only surface. An ordinary no-op resume does not churn the dispatcher. |
| 5 | Minor | `DropPendingAdmission` was unconditional, so an unrelated later turn's failure destroyed a stage that had been deferred with an explicit "will be retried" promise. | Drops are keyed by turn id; appending to a stage moves its ownership to the appending turn. |
| 6 | Critical (**pre-existing**) | A crash between a migration's apply and finalize phases bricked the context store *permanently*: `repairContextSchema` cleared the dirty flag but never bumped `user_version`, so the bare `CREATE TABLE` re-ran and failed forever. Reproduced for **v1, v2 and v3** - v1/v2 predate this plan. | Repair now re-drives the whole finalize phase. v3 DDL is `IF NOT EXISTS`. Table-driven regression over all three versions. |
| 7 | Medium | Failing `load_tools` calls charged no bound at all: 3200 unknown-name calls left the 32-attempt budget untouched, so the documented anti-loop backstop was inert for exactly the case it names. | `ChargeAdmissionAttempt` is charged first in `Execute`, before argument parsing. |
| 8 | Minor | `RemainderSpool` was written unlocked after a publication and read unlocked by `sendAgent` - a validator produced a real `WARNING: DATA RACE`. | Captured into the turn snapshot under `s.mu`; `SetRemainderSpool` publishes under the lock. |
| 9 | Low | Deleting or pruning a session orphaned its admission row forever (5 create/delete cycles left 5 rows). No authority leak - a same-named session clears it on first save. | Reclaimed in the same transaction as each of the three delete paths. |
| 10 | Low | `firstLine` cut at the first `.` regardless of context, so `list_dir`'s index entry rendered as `...(default "` - unbalanced, all parameter information gone. The only description the model sees for a deferred tool. | Cuts only at a sentence-terminating period outside quotes and brackets; live-registry sweep asserts balanced delimiters for every shipped tool. |
| 11 | Low | Schema-mass telemetry counted admitted tools as still withheld, double-counting their tokens (measured: 439 tokens in both figures). | The admitted set is excluded from both the deferred count and the held registry. |
| 12 | Latent | `FileSessionStore.Save` wiped `meta.ToolAdmission`. Dormant - no production code constructs one today. | Preserved like `CreatedAt`, so the next caller does not inherit the trap. |

Also removed: `rebuildAgentScopedDispatcher`, dead since before this plan.

Residual risk: two processes opening the same context store concurrently can
still collide on the v1/v2 bare `CREATE TABLE` (fails once, next open succeeds,
not bricked). Serializing migration under `BEGIN IMMEDIATE` is deliberately not
done here.

## 11. Step 5 bug audit, round 2 (2026-08-03)

Round 2 targeted the round-1 FIXES, which were new code written fast. Four
auditors (authority-registry split, chat admission fixes, storage/switch fixes,
fresh-eyes scenarios), then three validators required to refute each finding
with an executed reproduction. All eight survived. **Three were introduced by
the round-1 fixes** - the audit loop earning its keep.

| # | Severity | Defect | Fix |
|---|---|---|---|
| 1 | High | `/model` reinstated round-1 #2 **and** broke INV-CE-05-A. `buildModelBinding` is the third `SessionDispatcherOpts` site and round 1 missed it: `AuthorityRegistry` nil collapsed routed-agent authority (`tools: []` on the wire, skill deregistered), and `DeferredTools`/`Session` unset meant `load_tools` was advertised but **not registered** - calling it returned `unknown tool "load_tools"`, killing deferred loading for the binding with no model-reachable recovery. | `/model` now rebuilds through the same `buildSurfaceFromBase` path `/agent` and admission use, carrying the frozen tier plan and the admitted set. `ModelBinding.Registry` publishes into `s.Tools` so the advertised set and the dispatcher stay one publication. The generation fence is unchanged and still refuses a binding prepared before an admission. |
| 2 | High | A refused narrowing on resume cleared `admittedTools` while leaving the tools live and invocable - round-1 #4's divergence, reintroduced through the discarded return value of `republishSurface`. `TestResumeRespectsTheSwitchGuard` **passed because of the bug**. | Fail closed on the reporting side: when narrowing is refused and the tools are still live, the admitted set is restored and a note says they stay loaded. The test that enshrined the bug now asserts the opposite. |
| 3 | High (**introduced by round-1 #9**) | `deleteSessionSnapshotRow` deleted and committed the admission row before falling through to `deleteCatalogContextSession`'s separate transaction, so a failed retention delete left the session **alive and its admitted set destroyed** - then resumed silently, by design, with no note. | The admission row is reclaimed only when the snapshot delete matched; the retention path reclaims its own inside its own transaction, and a truly orphaned row is swept when neither path matches. |
| 4 | Medium (path B **introduced by round-1 #5**) | The stage was stamped with `s.turnID` rather than the executing turn's id, so under force-send a turn's stage was destroyed by an unrelated turn's failure (A), or published by a turn that never asked for it (B). | `StageToolAdmission` takes the turn id, read from the dispatcher caller frame via the new `TurnIDFromContext` - host-set, never model-supplied. The unreachable `stage.TurnID > s.turnID` guard was removed rather than left as a fake defence. |
| 5 | Medium | Every publication minted a new `remainder.Spool`, whose grants are per-instance while the store is shared - so loading a tool made `read_output` answer **`denied`** for refs the same session had just produced. Pre-existing for `/agent`; tools/05 added two routine model-triggerable paths. | The spool is session-scoped: rebuilds reuse the live one, so publication is an identity re-publish rather than a revocation. |
| 6 | Low-Med | The frozen index still lists loaded tools as "not currently loaded", and round-1 #7 made every call chargeable - so no-op re-requests could burn all 32 attempts and lock out genuine loads. | A pure no-op refunds its attempt, bounded by a consecutive-no-op limit that errors with corrective text. Neither the frozen index (F5/D8) nor the chargeability of failing calls (#7) is touched. The `already loaded` result now says the index is frozen at bind time. |
| 7 | Low | Session cleanup closed the attach-time dispatcher, so after any publication the live dispatcher's `OnClose` hooks never ran. Pre-existing for `/agent`. | `Session.CloseDispatcher` snapshots the live dispatcher under the lock at call time - one place covering attach, `/agent`, admission and `/model`. |
| 8 | Medium (test validity) | `TestCatalogContextDeleteReportsAnUnreclaimableAdmissionRow` passed on an error from a path it never entered, and was a duplicate. | Rewritten with a conditional trigger that can only fire from inside the retention transaction, so it cannot pass without reaching it. Its siblings were re-checked for the same failure mode. |

Also closed while reconciling: `PruneSessionSnapshots` had M3's shape (unconditional
admission reclamation for a name that may be a live context-backed session). It
has no production caller today, so it was a latent trap rather than a live bug.

Residual risk: unchanged from round 1 - concurrent opens can still collide on
the v1/v2 bare `CREATE TABLE` (fails once, next open succeeds, not bricked).

## 12. Step 5 bug audit, round 3 (2026-08-03)

Round 3 targeted the round-2 fixes plus a drift/dead-code lens. Four auditors,
three validators requiring executed reproductions. Thirteen findings, all
confirmed; two severities were corrected DOWN by validators (the spool store
defect is unreachable in shipped chat; the refund ceiling is a constant, not
growth in the deferred-set size). **Three more were introduced by the round-2
fixes.**

| # | Severity | Defect | Fix |
|---|---|---|---|
| 1 | High | **One root cause wearing three hats:** the code had no representation for "staged but not yet published", so `StageToolAdmission` folded pending names into the admitted set. `load_tools` then told the model `These are callable now` about a tool staged seconds earlier that `Tools.Get` could not find; a pure re-request emitted *only* that line, with no "next turn" correction; and the no-op streak error - the message the refund design leans on - told the model to call a tool that would fail with unknown-tool. | `AdmissionStageResult` gained `AlreadyStaged`, populated from `pendingAdmission.Names` separately from `admittedTools`. Staged-again names render under the existing "available from your next turn" sentence; the streak error is built from the admitted-only subset. |
| 2 | Medium (**introduced by round-2 #4**) | Round-1 #5 was still reachable **sequentially**: a deferred stage carrying an explicit "will be retried" promise was destroyed when the next turn appended one name (taking ownership) and then errored. The turn-id guard only defended against turns that never touched the stage. | Stage ownership is now per name: `dropPendingAdmissionForTurn` filters out only the failing turn's entries and clears the stage only when nothing survives. |
| 3 | Medium (**introduced by round-2 #1**) | `/model` re-read skills from disk but never committed them to `agentSessionState`, so the next admission rebuilt from the attach-time registry - reverting a skill added mid-session and, worse, **resurrecting one the operator had deleted**, silently invocable again. | `modelSwitchSurface` reuses `state.SkillRegFull`. A model switch is not a new binding; skill re-discovery is `/agent`'s job. Deliberately not a build-time `commitTo`, which would install derived state for a binding `SwitchBinding` may still refuse. |
| 4 | Medium | `scopedRootRegistry` wrote its "disabled tools omitted" warning straight to `os.Stderr`, and this feature made it fire twice on an inert attach and **once per tool admission, mid-turn**. Under the TUI - the default surface - that corrupts the rendered frame, which is the exact hazard `warnBindingOnce` exists to prevent, and this was the first model-triggerable source. | The function returns `(registry, disabled)` and never prints; the diagnostic is emitted only at the attach and `/agent` entry points, so the mid-turn write is gone rather than deduped. |
| 5 | Medium | Docs and plan claimed `/tools` reports schema mass. Only the classic REPL did; the TUI's `/tools` listed names, and its event loop drops `KindConfigChange` - so the feature's only operator-facing justification was absent on the surface almost everyone runs. | The measurement is prepended to the TUI tools dialog. `TestSlashToolsReportsSchemaMass` renamed to `...Classic`; a TUI test owns the other half. |
| 6 | Low (**introduced by round-2 #5**) | The reused spool outlives the store it reads through: a `Spool` captures its `ContentStore`, and a dispatcher that owns its ledger store closes it on publication. Round 2 turned "old refs say denied" into `read_output: sql: database is closed` for old **and** new refs. Unreachable in shipped chat (`setupSessionContext` always supplies a shared store) - a landmine for embedders. | Store ownership hoisted out of the per-surface dispatcher: opened once at attach, held on `agentSessionState`, passed as `Repo` on every rebuild, closed in cleanup. Also needed a `sessionLedgerRepo` wrapper, because the coordinator close hook type-asserts the concrete repo and closes it. |
| 7 | Low | The refund weakened the 32-call bound to a hard 128, and the streak reset ran *before* the publication-bound rejection so a permanently-rejected call bought three more free calls forever. | Refund is a per-binding budget; the reset moved below the rejection. Ceiling back to ~35. |
| 8 | Low | The "consecutive" no-op counter was never reset at a turn boundary, so one innocent re-request per turn hard-errored on turn 4 and charged an unrefunded attempt every turn after. | Reset at the turn boundary, matching the documented semantics. |
| 9 | Low | `TestNoOpLoadToolsCallsDoNotBurnTheGenuineBudget` passed with the refund deleted - mutation-proved by two agents independently. Its scenario never approached the ceiling. | Rewritten to spend the full budget; **verified to fail under the same mutation**. |
| 10-13 | Low | Drift: `sessionRouting.Resolved`'s "nil keeps deferred loading inert" was false (a per-agent `tools_core` still defers); `applyRootAgentScope` had become production-dead while its comment stated a load-bearing invariant and INV-AG-29 cited a test that only exercised it; a dangling `rebuildAgentScopedDispatcher` citation; a planner test-seam comment justifying a seam this feature deleted. | Comment corrected; dead helper and its alias test deleted with INV-AG-29 retargeted at the test that drives the real attach path; citations fixed. |

Rounds 1-3 found 12, 8 and 13 defects. The count did not fall monotonically
because each round changed lens: round 2 attacked round 1's fixes, round 3
added a drift lens that had never been run and which accounts for five of its
thirteen. Six of the thirty-three were introduced by an earlier round's fix.

## 13. Step 5 bug audit, round 4 (2026-08-03)

Round 4 used four lenses never run before: adversarial input, an empirical
concurrency STRESS harness (rather than reasoning about locks), the round-3
fixes, and an advisory simplification pass. Three validators required executed
reproductions and rebuilt the important ones from scratch. Ten bug findings,
all confirmed - but three had their severity or their FIX corrected.

| # | Severity | Defect | Fix |
|---|---|---|---|
| 1 | High | **`Session.Load` was the only surface-mutating entry point with no exclusion against live turns.** `/agent` takes `BeginSurfaceSwitch`, `/model` refuses on `activeTurns > 0`; `Load` took nothing, and the TUI dispatches `/load` on the update goroutine with no `m.waiting` gate while the turn runs on a worker. A validator built a **deterministic** reproduction: the turn boundary publishes `core+old+glob`, `/load`'s narrowing is refused on a stale generation, and the restore clobbers the turn's writeback - `glob` live and callable, unreported, unpersisted. Two ancillary defects fell out of the same gap (a live turn's stage destroyed; a boundary publication fenced out by `Load`'s `turnID++`). | A sibling `loading` reservation that blocks turns but **not** publication. The validator's prescribed `BeginSurfaceSwitch` does not work: a load publishes a surface itself, so that reservation would make every resume refuse its own narrowing. Plus the TUI `/load` gate. |
| 2 | Medium | Confirmed `-race` on the exported `Session.Tools`, driven by `/tools` reading it unlocked during a publication. The validator's sweep found a **second reachable site nobody had flagged**: `buildModelBinding` reads it mid-turn, because `/model` builds its candidate before `SwitchBinding` refuses. | All reachable and latent readers routed through `AgentSurfaceSnapshot()`. |
| 3 | Medium (**introduced by round-3 #1**) | `AlreadyStaged` promised "callable from your next turn" without recording the asking turn as an owner, so the original owner's boundary destroyed the stage - round-1 #5 by a new route. Found independently by two agents. | Per-name owner **sets** (option (a)). The validator prototyped both candidates and executed them: option (b) was smaller but failed `TestDroppingOneTurnsStageKeepsAnotherTurnsNames`, discarding a locked D7 property. |
| 4 | Medium | Two turn surfaces never drained admission notes - the classic interactive REPL and `oneShot`. The auditor found one; the validator's exhaustive enumeration found the second, where the process exits so the note is never seen at all. | Both drain; `oneShot` to stderr, since stdout is its answer channel. |
| 5 | Low-Med | Model-supplied names reached the terminal with ANSI escapes intact (`ESC[2J` clear-screen executed). **The auditor's proposed `%q` fix was rejected by the validator**, who proved the defect is general to `boundedToolText` - `read_file` echoes model input through the same path. | One line in `boundedToolText` routing through the repo's existing `SafeChatBlockText`, prototyped by the validator with no regression across the cli suite. Byte-vs-rune truncation fixed in the same function. The `%q` asymmetry was fixed separately as consistency, not as the security fix. |
| 6 | Low (**introduced by round-3 #6**) | `sessionLedgerRepo` hid `Recover` from the coordinator, so `/resume` discovery returned zero interrupted runs. The auditor rated it High; the validator proved it unreachable in shipped chat (same narrow config as round-3 #6). | Ownership made explicit: `initCoordinator` closes only a store it opened. The wrapper is deleted - the type-hiding trick would break the next optional interface too. |
| 7 | Low (**introduced by round-3 #6**) | The session-owned ledger store leaked on the attach error path: ownership is taken before the dispatcher is built, and a failed build returns no cleanup. | Closed and cleared on the error return. |
| 8 | Low | `load_tools` declared no `maxItems`, and the unknown-name error amplifies input O(n): 10k names produced a 1.2 MB error whose **full pre-cap body is durably written to the content store**. | `maxItems` declared (as `float64` - the validator's type is enforced by a test, because an untyped literal silently leaves the guard inert) and the echoed list bounded. |
| 9 | None | `dropPendingAdmissionForTurn`'s desync guard was followed by an unguarded index - a panic if it ever fired. The validator forced the desync (panic confirmed) and then proved it unreachable. | Dead defensive code removed; the new owner-set shape has no equivalent. |
| 10 | Low | `s.loadedContextSession` written unlocked. Not reachable today; lock-discipline only. | Written under `s.mu`. |

Advisory fold-in from the simplification review: `AdmissionStage.TurnID` was
write-only in production and exported through `PendingAdmission()`, reading as
a contract a future reader would trust - which is how the whole-stage-ownership
bug happened. Deleted.

### Simplification review (advisory, not gates)

A tenth lens reported ten findings; the reviewer explicitly recommended taking
only two, and flagged what to leave alone. Taken: the dead `AdmissionStage.TurnID`.
**Not taken, deliberately**: converting `internal/storage`'s hand-rolled
transactions to the new helpers (~-70 lines) and the other seven. Every round so
far has seen its own fixes introduce new defects, and a mechanical refactor of
migration and retention code - the area that produced the one CRITICAL finding -
is exactly where that risk is worst. The reviewer's own residual-risk note says
to land the largest one alone. Recorded here as known, deliberate debt.

The reviewer's rejected-concerns list is worth keeping: the five surface-build
functions, the four registry-scoping functions, `commitTo`, and the double
`switching`/`activeTurns` check are all load-bearing and were argued for
explicitly, not left by accident.

### Where the loop stands

Rounds 1-4 found 12, 8, 13 and 10 defects; **nine of the 43 were introduced by
an earlier round's fix**. The count is not converging on zero because each round
has changed lens rather than re-running the last one - round 4's stress harness
and input lens had never been applied. What IS converging: round 4 found no new
defect in the original feature design, and its concurrency harness reported the
core invariants (INV-CE-05-A, admitted-subset-of-advertised, authority bounds,
no use-after-close, counter bounds) clean across ~64k operations.

## 14. Step 5, round 5 (2026-08-03): mutation testing, security, and economics

Round 5 used three lenses aimed at whether the previous four rounds could be
trusted, rather than at finding more instances.

### 14.1 Mutation testing: 100% diff-coverage detected 71% of injected defects

113 mutants attempted, 104 applied cleanly, **30 survived (29%)** with the whole
suite green. Coverage had reached 1622/1622 changed statement lines, so this is
the gap between "executed" and "asserted". Survivors clustered on exactly the
properties the 43 recorded defects were about:

- **Both layers of the `switching` guard could be deleted together** and stay
  green - nothing verified that a publication is refused while `/agent` holds
  `BeginSurfaceSwitch`, which is the R2-1 hazard.
- **The admitted-tail authority clamp was unguarded** (round-1 #1's last line of
  defence). The core-side clamp was tested; the tail alone was not.
- **`AdmissionDigest` need not fingerprint the deferred tier** - a split
  changing only the deferred side kept its digest, defeating D3's fail-closed
  resume.
- **`sentenceEnd`'s quote/bracket tracking was dead to the suite** - five
  separate mutations survived; every case in the test that named the property
  was actually decided by the followed-by-space rule.
- **Two tests written to pin round-3 fixes passed for the wrong reason**: the
  `/agent` widener tests could not distinguish "re-installed by the switch" from
  "left over from attach", because the fixture's startup widener closes over the
  live state.

All 30 are now killed or documented as equivalent mutants with the argument.
Four were judged genuinely equivalent and recorded as such rather than papered
over with contrived assertions.

**One real defect fell out of the exercise.** `ScopedRegistryWithTail` at
`ScopeRoot` admitted a name in the operator guardrail denylist - host-mediated
admission could re-enter an INV-AG-29 denial. It was saved only by the caller's
clamp, and its own doc comment claimed a guarantee it did not have. The function
now enforces the denylist itself in both modes.

### 14.2 Security and privacy: no findings

The lens was run and came back clean. Recorded so it is not re-run blind:
`chat_session_admissions` is structurally identical to `chat_sessions` (which
stores whole transcripts) and is reclaimed by every shipped delete path;
redaction correctly does not apply (compiled-in tool names, an operator-chosen
agent name, integers); the `tool_schema_mass` event has no persisting sink; the
prompt-injection conclusion from round 4 still holds and extends (the deferred
candidate set is snapshotted before session tools register, and `AllToolNames`
is a static catalogue); `make secret-scan` clean.

Two residual risks recorded: `noteAdmissionDrop` renders persisted names to the
terminal unbounded and unsanitized (inert - the file store that would make it
model-reachable has no production caller), and the `ScopedRegistryWithTail`
doc-comment overstatement, now fixed.

### 14.3 Economics: the feature does not earn its complexity

Measured against the real registry with the repo's own estimator:

| | Advertised | Est. tokens |
|---|---|---|
| Inert | 19 | 5,179 |
| Enabled, core of 4 | 14 | 3,979 |
| **Agent scoping (`EffectiveTools`) alone - already shipped** | | **3,771** |

**Agent scoping saves more (1,408 tokens) than deferred loading (1,200)**, with
no `load_tools` schema, no frozen index, no cache breaks and no wasted turns.

Three findings make the gap structural, not tunable:

1. **56% of the surface cannot be deferred at all.** `state.ToolBase` is
   snapshotted before `NewSessionDispatcher` registers the privileged session
   tools, so the 2,913-token orchestration/ledger block is unreachable by this
   mechanism - and "orchestration + web" is exactly what §6 step 8 named as the
   intended default deferred set.
2. **Break-even is 4-6 admissions out of 6 deferred tools.** Loading everything
   costs +332 tok/request forever (`load_tools` 208 + index 124).
3. **Cache economics invert the saving.** The admitted tail lands before the
   privileged block, so an admission invalidates 79% of the tool block plus the
   system prompt plus the conversation. Estimated at standard implicit-cache
   ratios, one admission needs ~70-450 subsequent requests to amortize.

What the measurement vindicates: the frozen index is 12x cheaper than the
schemas it replaces, the core block genuinely is byte-stable across an
admission, and a publication costs 19-55 microseconds - imperceptible, and the
one cost the plan worried about.

**Step 8 remains untaken and the recommendation is now evidence-backed rather
than provisional.** If schema mass is the real problem, the privileged block is
where the money is, and reaching it needs a tier split applied *after*
dispatcher registration - a different mechanism.
