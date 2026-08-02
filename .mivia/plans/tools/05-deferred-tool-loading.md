# tools/05 - Deferred tool loading: search-and-load tool surface

**Status:** DESIGN REWORKED - ADLC Step 0 round 1 complete (2026-08-02),
verdict REWORK; the three demanded decisions (F1/F2, F3, F4) are locked
below. **Round 2 re-challenge required before implementation** - the
challenge conditioned lock on these decisions being attacked once made.
The 51.05 amendment may now be drafted (its text depends on F3, resolved).
**Date:** 2026-08-02 (revised after Step 0 challenge)
**Depends on:** 51.05 Stage A implementation (still docs-only at
`7de2fb8`); the in-flight durable-persistence migration (F4 -
re-baseline D3 after it lands); plan 46 v1 (shipped, observation-only).
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

**D7 - `widenAgentSurface(names)` primitive.** A narrow function that:
derives the new registry via `ScopedRegistry` with
`Allowlist = core ∪ admitted` (one authority path - INV-CE-05-D intact),
**reuses** the existing skill registry/scope (no disk I/O), and swaps the
dispatcher at the turn boundary where nothing is executing (no mid-batch
`Close()` - the F2 hazard is structurally absent at that point). It is
not the `/agent` switch path; it shares only `PublishAgentSurface`'s
generation/fencing bookkeeping.

**D3 (rewritten) - single persistence SoT.** Context-enabled sessions
persist the admitted set (names + agent name + digest) in the durable
context store's session state; legacy file sessions persist it in
`sessionMeta`. Never both. Resume replays the D7 rebuild **before the
first request** (not merely "before the first turn"); digest mismatch
drops the set fail-closed and injects a bounded system note naming the
dropped tools (F6). This step re-baselines against the in-flight
persistence migration before implementation.

**D4 - unit is the agent binding** (unchanged): `/agent` switch resets
admissions to that agent's core; persisted set keyed by agent name +
digest.

**D5 - wire the schema-cost hoist as step 0** (unchanged): independent,
produces the schema-mass telemetry 51.05 demands, and the before/after
measurement.

**D8 - deferred index frozen at bind.** Generated once per binding into
the system prompt (name + one-liner each); prefix-stable by construction;
therefore stale-by-design after admissions. `load_tools` on
already-admitted names is idempotent: free (no cap charge), returns
"already loaded" (F5/F7). Cache reality (46 v1 is implicit-prefix): each
admission invalidates the prefix from the tool block onward **once**, at
a turn boundary; the frozen index keeps the system prompt bytes stable so
invalidation never starts at byte 0. Precondition check kept as a hard
step-2 gate: verify provider serializers emit tools in registry order
(core-first) before relying on partial prefix survival.

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

- Ordering: stage -> turn persistence -> generation bump -> next request
  carries new schemas (the F1 sequence, now as a passing test).
- No mid-batch dispatcher close: load_tools alongside sibling calls in
  one batch; siblings complete on the original dispatcher.
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
