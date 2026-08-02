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
