# tools/05 - Deferred tool loading: search-and-load tool surface

**Status:** DESIGN VALIDATED (2026-08-02) - baselines verified against HEAD.
**This plan requires an explicit amendment to locked plan `51/05` before
implementation** (see D1): the original draft collided with INV-CE-05-D.
Sequencing: 51.05 Stage A (still unimplemented - docs-locked only at
`7de2fb8`) must ship first; then the 51.05 amendment; then this.
**Date:** 2026-08-02 (revised after code validation)
**Depends on:** 51.05 Stage A implementation; new session-state persistence
surface (D3 - unbudgeted in the original draft); plan 46 v1 (shipped,
observation-only - see D2).
**Blast radius:** MEDIUM-HIGH - registry/binding lifecycle, one new
privileged-adjacent tool, session persistence schema, INV-CE-05 invariants.

## 1. Verified baseline

- **Schema mass is real and unmitigated**: `loop.Run` hoists the spec *list*
  once (`internal/agent/loop.go:116` `OpenAITools()`), but schema bytes ship
  on every request (`:283`) and `EstimateRequestCost` re-marshals every
  ToolSpec per cost call (`internal/provider/context.go:76-81`) - including
  inside the planner's tail-fill loop (`contextmgr/planner.go:85,111,239`).
  A hoist helper `EstimateToolSchemaCost` (`context.go:107-119`) exists with
  **zero non-test callers** - built, never wired.
- **51.05 Stage A locked but unimplemented.** It locked: authority SoT =
  `ResolvedAgent.EffectiveTools` materialized by `ScopedRegistry`;
  advertisement = `OpenAITools()` over that same live registry; it
  **explicitly rejected** a `ToolSpecs(authority)` API and INV-CE-05-D
  forbids "a second independent allowlist for schemas" / any
  `OpenAIToolsFiltered(names)`. INV-CE-05-B/E fix the advertised set per
  **agent binding**, not per session (`/agent` switch rebuilds the surface).
  Relevance gating is explicitly out of program 51 until schema-mass
  telemetry justifies it.
- **Plan 46 shipped observation only** (`archived/46-...md`): `CacheUsage`
  decoding + `[provider] prompt_cache` toggle; breakpoint planner and
  `cache_control` emission were cut - no outgoing request mutation exists.
  There is no breakpoint to place; caching is implicit-prefix, observable
  via `CacheUsageEvent`.
- **No resume-safe session KV exists**: `internal/chat/persistence.go:76-98`
  persists messages + meta only; even `Session.Calibration` is lost on
  resume. An admitted-tools set has no home today.
- **Subagent scoping gap** (pre-existing): `multi_step.go:328` calls
  `ScopedRegistry` with `ScopeSpawned` and **no Allowlist** - the guard
  test 51.05 planned does not exist.
- No `load_tools`/tier/deferred concept exists anywhere in code or config.

## 2. Goal (unchanged)

A small always-present core set plus a `load_tools` discovery surface, so
authorized-but-unused schema-heavy tools stop shipping on every request of
every session.

## 3. Resolved decisions

**D1 - admission is a binding rebuild, not a filter view (51.05 amendment).**
INV-CE-05-D forbids a second schema allowlist beside the registry. So
`load_tools` must not filter advertisement - it must **narrow then widen the
registry itself**: admission rebuilds the scoped registry with
`Allowlist = core ∪ admitted` (all ⊆ `EffectiveTools`), through the same
path an `/agent` switch already uses (`buildAgentScopedSurface`). The
advertised set remains *identical* to the invocable set at every moment -
INV-CE-05-A/D hold by construction; what changes is that a binding's
registry can grow monotonically. This reframing must be written into 51.05
as an amendment ("binding tool-surface may grow via host-mediated
admission; never shrink mid-binding") and accepted before implementation.
INV-CE-05-B's "byte-stable per binding" relaxes to "byte-stable between
admissions, admissions monotonic" - that is the amendment's core ask.

**D2 - cache interaction is implicit-prefix, not breakpoints.** Rewrite of
the original §3.3: growth of the tool block invalidates the implicit prefix
from that byte onward - once per admission, observable via the existing
`CacheUsageEvent`. Mitigations: batch admission (one call may admit many),
and ordering the serialized tool block core-first/admitted-appended so the
core prefix stays byte-stable across admissions (verify provider serializers
preserve registry order before relying on this).

**D3 - persistence rides `sessionMeta`.** The admitted-names list (small,
strings only) is added to `sessionMeta` (`persistence.go:89-98`) and the
durable catalog equivalent, replayed on load by re-running the D1 rebuild
before the first turn. Names no longer in `EffectiveTools` at load time are
dropped with a bounded warning (agent definition changed - fail closed,
consistent with digest revalidation). This is new persistence surface and
is budgeted as its own step.

**D4 - unit is the agent binding, not the session** (matches INV-CE-05-E).
An `/agent` switch resets the admitted set to that agent's core. The
persisted set is keyed by agent name + digest; a digest mismatch on resume
drops it.

**D5 - wire the schema-cost hoist as step 0.** `EstimateToolSchemaCost`
exists unused; wiring it into `EstimateRequestCost` callers (hoisted out of
planner loops) is independent, immediately valuable, and produces the
schema-mass telemetry 51.05 says must justify relevance gating. It also
gives this plan its before/after measurement for free.

## 4. Design

### 4.1 Tiers

- `[tools] core = [...]` config + per-agent `tools_core` override in
  `AgentFileSpec` (pointer, inheritance-preserving, like existing fields).
  Default: **unset = everything core** (plan fully inert until opted in).
- Deferred = `EffectiveTools` minus core. `load_tools` itself is always
  core when any tool is deferred.

### 4.2 `load_tools`

```json
{ "query": "web search", "names": ["fetch_url"] }
```

- `names`: exact admission; `query`: lexical match over name + description
  (deterministic). Both may appear; all matches admit in one batch (D2).
- Returns admitted names + one-line descriptions; schemas appear from the
  next request (the D1 rebuild happens between steps, same lifecycle as an
  agent switch, so no mid-request mutation).
- Unknown or non-authorized names: bounded error listing valid deferred
  candidates - never widening authority beyond `EffectiveTools`.
- Per-binding admission-call cap (default 8) with a bounded error after.
- System prompt carries a compact deferred index (name + one-liner) - part
  of the stable prefix.

### 4.3 Subagents

Routed task agents already receive `Allowlist=EffectiveTools`
(`agent_task_handler.go:110`) and should ship with exact tools pre-admitted
via the agent definition - `load_tools` in a subagent is a fallback.
Precondition: add the missing guard test for `multi_step.go:328`'s
no-Allowlist `ScopedRegistry` call (51.05's planned test) - this plan must
not build on an unverified scoping assumption.

## 5. Invariants

- Advertised = invocable at every moment (INV-CE-05-A/D preserved by D1's
  rebuild approach; no second list exists).
- Admission monotonic per binding; never shrinks mid-binding; reset on
  binding change (D4).
- Admitted ⊆ `EffectiveTools` always; resume drops stale names fail-closed
  (D3/D4).
- Zero config -> byte-identical behavior to today (everything core).
- Deterministic: same admission calls -> same registry -> same serialized
  tool block (golden).
- Dispatcher rejects calls to non-admitted deferred tools with
  "not loaded: use load_tools" (this is just the existing unknown-tool
  path - the tool genuinely is not in the registry).

## 6. Implementation steps

0. Wire `EstimateToolSchemaCost` hoist + schema-mass telemetry (D5) -
   independent, land first, measure.
1. Author + get accepted the 51.05 amendment (D1); implement 51.05 Stage A
   if still unshipped (hard precondition).
2. Guard test for the multi_step no-Allowlist scoping (4.3).
3. Tier config (`core`/`tools_core`), inert-by-default.
4. `load_tools` + registry-rebuild admission lifecycle + call cap.
5. `sessionMeta` persistence + resume replay + digest-mismatch drop (D3).
6. Deferred index in system prompt; core-first tool-block ordering check
   (D2); `CacheUsageEvent`-based before/after measurement.
7. Ship a default deferred set (orchestration + web tools) **only if** the
   step-0 telemetry shows material per-request schema mass.

## 7. Testing

- Authority: admission outside EffectiveTools fails closed.
- Monotonicity + binding reset on `/agent` switch; resume replay incl.
  digest-mismatch drop.
- Determinism goldens on the serialized tool block across admissions
  (core prefix byte-stable).
- Inertness: no core config -> identical requests to HEAD.
- Request-size assertion: N deferred tools unloaded vs loaded.
- INV-CE-05 suite passes unchanged plus the amended B/E variants.

## 8. Failure analysis

- Model thrash-loads everything turn 1: index is minimal; call cap bounds
  it; worst case equals today's behavior.
- Model lacks a tool it never loads: deferred index always visible;
  failure is an explicit unknown-tool error, never silence.
- Amendment rejected by 51.05 owners: plan dies cleanly at step 1 with
  only step 0 (independently valuable) shipped - by design.
