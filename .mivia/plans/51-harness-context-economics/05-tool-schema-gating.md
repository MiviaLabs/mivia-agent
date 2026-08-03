# 51.05 - Tool-schema gating: auth-truthful advertised tools

**Status:** DESIGN LOCKED - ADLC Step 0 complete (2026-08-02). Ready for
implementation (prove/harden path first; no parallel ToolSpecs API).
**Date:** 2026-08-02 (revised after challenge)
**Part of:** program `51` (`00-overview.md`).
**Depends on:** nothing. **Sequence:** implement **before** `04` (operator).
**Interacts with:** `04` (schema mass after this plan is auth-scoped);
program INV-CE-A / INV-CE-B.
**Blast radius:**
- **Product:** MEDIUM if relevance gating shipped (it does **not** in Stage A).
  Stage A is MEDIUM-eng / LOW-product: mostly proof + gap fix on an existing
  seam.
- **Engineering:** MEDIUM - agent scope, registry, loop toolSpecs, subagent
  spawn, integration tests for INV-AG-29 / schema identity.

## 0. Step 0 disposition (hostile challenge)

### Original design summary

- Commit to **authorization gating (a)**; relevance (b) opt-in later.
- New `ToolSpecs(authority)` on the registry; wire into loop.
- INV: schema sent iff invocable; session-fixed set; monotonic re-admission
  for relevance; one authority SoT with the dispatcher.

### Findings and dispositions

| Finding | Severity | Disposition |
|---------|----------|-------------|
| **Auth schema gating largely already exists** via `ScopedRegistry` + `applyRootAgentScope` / agent-task allowlist; loop calls `OpenAITools()` on the **already scoped** registry | BLOCK on "build ToolSpecs from scratch" | **Accepted.** Stage A is **prove, harden, document** - not a second allowlist path. |
| Plan cites **skills allowlist** as tool authority; real tool authority is `ResolvedAgent.EffectiveTools` / `DisallowedTools` (skills are separate) | BLOCK | **Accepted.** Authority SoT = agent effective tool set ∩ live registry (+ root privileged tools). |
| Baseline "nothing subsets ToolSpec" is **false** for selected agents | Confirmed | **Accepted.** Re-baseline: subset at **agent bind / switch / spawn**, not per model turn. |
| `ToolSpecs(authority)` would **duplicate** `ScopedRegistry` and risk INV-CE-05-D drift | BLOCK | **Rejected** as Stage A API. Reuse `ScopedRegistry` / scoped `sess.Tools`. |
| `/agent` switch rebuilds scoped surface from `ToolBase` | Open §6.1 | **Closed:** switch **intentionally** changes the tool prefix; INV-CE-05-B is per **agent-binding lifetime**, not process lifetime. |
| Relevance gating mid-session breaks prompt cache (INV-CE-B) | Confirmed | **Closed:** relevance **out of Stage A** and out of program 51 unless a later plan measures need. |
| Empty toolset | Open §6.3 | **Closed:** `fail_on_empty_toolset` defaults **true** at resolve (`config.AgentsGlobal`); refuse empty agents. |
| Root agent with omitted `tools` gets **full catalogue** (`defaultToolPool`) | Confirmed | Stage A does not invent relevance; full-root cost is **configuration**, not a harness bug. Restrict agent TOML to save schema mass. |
| Privileged tools always retained on ScopeRoot | Confirmed | **Intentional** (delegation). Schemas for privileged tools stay on root. |
| MultiStepHandler `restrictedRegistry` ScopeSpawned without allowlist | Risk | **Usually safe** when `FullRegistry` was pre-allowlisted by `agentTaskHandler`. Stage A adds a **guard test** that spawn path never re-expands past EffectiveTools. |
| `promptBudgetError` / oneshot / tests may use unscoped registries | MEDIUM | Audit call sites; any production path that advertises more than it can invoke is a **confirmed gap** to fix. |

### Locked thesis

**Do not build a parallel schema-gating system.** The harness already scopes
the live tool registry to the agent's effective tools (and spawn denylist).
`OpenAITools()` then advertises exactly what is registered. Stage A makes
that coupling an **explicit, tested program invariant**, closes residual
advertise/invoke mismatches, and records schema-mass telemetry so root-full
catalogue cost is visible. Relevance gating is a separate product decision
and is **not** delivered here.

## 1. Goal

Ensure every tool schema sent to the model is a tool the current agent
binding can actually invoke, with one authority source shared by
advertisement and dispatch - and prove that property under agent switch and
spawn.

## 2. Verified baseline (re-read at Step 0)

- `Registry.OpenAITools()` emits one function schema per registered tool, in
  registration order (`internal/tools/tools.go`).
- `EstimateRequestCost` charges every tool in the `[]ToolSpec` it is handed
  (`internal/provider/context.go`). The planner prices whatever list it gets.
- Agent definitions resolve `EffectiveTools` / `DisallowedTools`
  (`internal/agents/resolve.go`, `ResolvedAgent`).
- **Root scope:** `applyRootAgentScope` → `scopedRootRegistry` →
  `tools.ScopedRegistry(..., ScopeRoot, Allowlist=EffectiveTools ∩ registry)`
  (`internal/cli/agent_handlers.go`, `internal/cli/chat_repl.go`).
- **Agent switch:** rebuild from `state.ToolBase` then re-scope
  (`internal/cli/agent_switch.go` `buildAgentScopedSurface`).
- **Routed task agents:** `ScopedRegistry(full, ScopeSpawned, Allowlist=EffectiveTools)`
  before constructing `MultiStepHandler` (`internal/cli/agent_task_handler.go`).
- **Loop advertisement:** `toolSpecs := l.Tools.OpenAITools()` once per
  `Run`, reused every step (`internal/agent/loop.go`) - stability within a
  run is free if `l.Tools` is fixed.
- Skills allowlist (`ResolvedAgent.Skills`) gates **skill invocation**, not
  tool schemas (`internal/cli/agent_skill_policy.go`).
- Empty effective toolset: refused when `FailOnEmptyToolset` (default true).
- INV-AG-29 intent (dispatcher ↔ registry agreement) is already documented
  on the scope attach path.

## 3. The defect (refined)

Not "the harness never subsets schemas." The remaining problems are:

1. **Unproven coupling** - few tests assert *schema name set == invocable
   name set* for a restricted agent across root attach, `/agent` switch, and
   spawn.
2. **Authority mis-statement** - treating skills as the tool gate invites a
   wrong implementation.
3. **Root full catalogue** - default/unrestricted agents still pay full
   schema mass every turn (config reality; not fixed by inventing relevance
   without evidence).
4. **Possible edge paths** - any production surface that builds a loop or
   request with an unscoped registry while the dispatcher is scoped (or the
   reverse) re-opens INV-CE-05-A / INV-AG-29.

Relevance-based per-turn pruning is a **different** feature and is out of
Stage A.

## 4. Locked design

### 4.1 Authority SoT (single)

| Layer | Role |
|-------|------|
| `ResolvedAgent.EffectiveTools` (+ resolve-time denylist/guardrails) | Authored/resolved allowlist |
| `tools.ScopedRegistry` | Materializes allowlist + ScopeRoot/Spawned rules into a live `*Registry` |
| `Registry.OpenAITools()` | Advertisement = registration set |
| Dispatcher handlers registered from same registry | Invocation set |

**INV-CE-05-D:** no second independent allowlist for schemas. Do not add
`OpenAIToolsFiltered(names)` that can disagree with `ScopedRegistry`.

### 4.2 Stage A deliverables

1. **Invariant tests (primary)**
   - Restricted agent (e.g. reviewer tools only): every name in
     `OpenAITools()` is invocable; every EffectiveTools name present in the
     live registry appears in OpenAITools (modulo disabled/missing catalogue
     tools already warned at scope time).
   - Privileged root tools remain advertised on ScopeRoot even if absent from
     EffectiveTools (document as intentional).
   - Spawned multi-step registry never contains privileged tools and never
     contains names outside the routed agent's EffectiveTools when constructed
     via `agentTaskHandler`.
   - After simulated `/agent` switch A→B, schema set equals B's scoped set
     (not A's).

2. **Gap audit + fix**
   - Enumerate production `OpenAITools()` call sites: today
     `agent/loop.go`, `chat/context_integration.go`, `chat/context_control.go`.
   - For each, prove the `*Registry` is the session/scoped registry (or
     document intentional full catalogue).
   - Fix any path that advertises a superset of invocable tools.

3. **Telemetry (lightweight)**
   - At agent attach / switch (and optionally first step of a run), record
     schema count and optional estimated schema tokens
     (`EstimateToolSchemaCost` or prompt breakdown once `04` lands).
   - Prefer existing progress/status surfaces or a single debug log line;
   avoid a large new event type unless one already fits.

4. **Docs (owned surfaces only)**
   - Product/architecture note: schemas follow live registry; restrict
     agent `tools = [...]` to shrink cost; `/agent` changes the tool prefix.

### 4.3 What Stage A does **not** do

- New `ToolSpecs(authority)` API.
- Relevance / phase-based schema pruning.
- `list_capabilities` recovery tool.
- Mid-session monotonic re-admission machinery.
- Changing `Capability` semantics or dispatcher allow maps independently of
  the registry.
- Compressing individual schema JSON.

### 4.4 Relevance gating (deferred design note only)

If a future plan reopens relevance:

- Off by default.
- Monotonic re-admission only (INV-CE-05-C).
- Must not fork authority from EffectiveTools without an explicit
  "loaded subset" state machine shared by dispatch.
- Measure residual schema mass **after** agents are correctly restricted
  before investing.

## 5. Invariants

- **INV-CE-05-A.** For a bound agent session, every non-privileged schema
  name in `OpenAITools()` is in the agent's effective allowlist (as applied
  by `ScopedRegistry`). No schema is advertised for a capability the
  dispatcher cannot invoke because the tool is absent from the live registry.
- **INV-CE-05-B.** Under a fixed agent binding (no `/agent` switch, no
  dispatcher rebuild), the advertised schema set is byte-stable for the
  lifetime of that binding (same registration order and specs).
- **INV-CE-05-C.** (Deferred with relevance) Re-admission monotonic if
  relevance ever ships.
- **INV-CE-05-D.** Advertisement and invocation share one live `*Registry`
  derived from EffectiveTools via `ScopedRegistry` - not a second list.
- **INV-CE-05-E.** `/agent` switch replaces the binding and **may** change
  the tool prefix; that is not a violation of INV-CE-05-B. **Amendment
  (2026-08-02, plan tools/05 D6):** a binding's successor generation may widen
  the tool surface monotonically via host-mediated admission. Admission bumps
  `agentSurfaceGeneration` exactly as a switch does, so INV-CE-05-B continues
  to hold verbatim *within* each generation and the widening is an
  INV-CE-05-E event, not a B violation. Admission remains subject to
  INV-CE-05-A and -D: the admitted set is derived from the same
  `EffectiveTools` allowlist through the same `ScopedRegistry` path, so it can
  never advertise a tool the dispatcher cannot invoke.
- **INV-CE-05-F.** Empty effective toolset remains refused by default
  (`fail_on_empty_toolset`); no silent "chat with zero tools" unless the
  operator explicitly disables that guardrail.

Program ties:

- **INV-CE-A:** planner still prices the same tool list attached to the
  request.
- **INV-CE-B:** no mid-binding schema churn for economy reasons in Stage A.

## 6. Closed decisions (were open)

| # | Decision | Lock |
|---|----------|------|
| 1 | Mid-session authority change (`/agent`) | **Resets tool surface**; INV-CE-05-B is per binding. Prefix cache break is accepted. |
| 2 | Relevance gating in program 51 | **No.** Split to a future plan after schema-mass telemetry. |
| 3 | Empty tool set | **Refuse by default** (`FailOnEmptyToolset: true`). Not a normal config. |
| 4 | New ToolSpecs API | **No** in Stage A; reuse ScopedRegistry. |
| 5 | Skills vs tools | Skills do **not** gate tool schemas. |
| 6 | Sequence vs `04` | **`05` first**, then `04`. |

## 7. Delivery slices

1. **RED tests** for INV-CE-05-A/B/D/E/F on root attach, switch, and spawn.
2. **Gap audit** of OpenAITools call sites; fix mismatches if any (prod code).
3. **Telemetry** of schema count (and cost if cheap) at attach/switch.
4. **Docs** on owned surfaces only (`docs/OWNERS.yaml`).
5. Optional: if audit finds zero product bugs, Stage A may land as
   **tests + docs + telemetry only** - still a valid delivery.

## 8. Required tests

| Test | Asserts |
|------|---------|
| Restricted agent OpenAITools ⊆ EffectiveTools ∪ privileged(root) | INV-CE-05-A |
| Every EffectiveTools name present in ToolBase appears after scope (minus disabled warning path) | no silent drop of allowed tools |
| Dispatcher Invoke denies tool absent from scoped registry; schema also absent | dual half of allowlist |
| Two consecutive Run/prepare paths same schema bytes under fixed binding | INV-CE-05-B |
| Switch reviewer → go-engineer changes schema set to match new scope | INV-CE-05-E |
| Spawned agent cannot advertise or invoke privileged names | spawn boundary |
| Spawned agent cannot expand past EffectiveTools | allowlist hold |
| Empty tools agent fails resolve when fail_on_empty_toolset | INV-CE-05-F |
| EstimateRequestCost drops by removed schema charge when comparing full vs scoped registry | cost coupling |

## 9. API surface

| Symbol | Change |
|--------|--------|
| `tools.ScopedRegistry` | **Reuse** - document as the schema gate |
| `tools.Registry.OpenAITools` | **Unchanged** semantics |
| `cli.applyRootAgentScope` / `scopedRootRegistry` | **Unchanged** unless audit finds a bug |
| `cli.agentTaskHandler` allowlist scope | **Unchanged** unless audit finds a bug |
| New exported APIs | **None required** for Stage A |

### Files likely touched

| File | Why |
|------|-----|
| `internal/cli/*_test.go` / `agent_integration_test.go` | INV tests for attach/switch |
| `internal/tools/scope_test.go` or registry tests | Name-set identity helpers |
| `internal/subagents/*_test.go` / `cli/agent_task_handler*` | Spawn allowlist hold |
| `internal/cli/agent_handlers.go` or switch path | Only if gap fix needed |
| `internal/agent/loop.go` | Only if toolSpecs must re-read registry per step after switch mid-run (today switch rebuilds session; verify no stale loop Tools pointer) |
| Owned docs | Operator-facing note |

## 10. Micro-task waves (Step 1 draft)

| ID | Wave | Type | File / focus | Verify |
|----|------|------|--------------|--------|
| t1 | 1 | test | Integration: restricted agent schema ⊆ allowlist + privileged | RED or already green |
| t2 | 1 | test | Spawn EffectiveTools hold + no privileged leak | RED or green |
| t3 | 2 | audit | List prod OpenAITools sites; note each registry source | written in plan completion notes |
| t4 | 2 | prod | Fix any confirmed advertise/invoke mismatch (1 file per fix) | t1/t2 pass |
| t5 | 3 | test | `/agent` switch schema set follows new agent | pass |
| t6 | 3 | review | Dual-path authority (no second list) | PASS |
| t7 | 4 | telemetry/docs | Schema count at attach; OWNERS-safe doc if required | verify + docs-check |
| t8 | 4 | verify | `go test` on tools, cli, subagents, agent; race on tools | all pass |

## 11. Plan scorecard

| Criterion | Result |
|-----------|--------|
| Compiles against current architecture | PASS |
| No duplicate authority list | PASS |
| Testable without network | PASS |
| Prefix stability under fixed binding | PASS |
| `/agent` behavior specified | PASS |
| Relevance complexity deferred | PASS |
| Empty toolset specified | PASS |
| Sequence with `04` specified | PASS |
| Rollback criterion | PASS |

## 12. Rollback criterion

Revert Stage A production fixes if:

- A restricted agent loses a tool that is in EffectiveTools and present in
  ToolBase (false deny), or
- Schemas advertise a tool the dispatcher cannot invoke (false allow on
  advertise side), or
- Agent switch leaves stale toolSpecs on an in-flight loop after surface
  publish.

Tests-only delivery has nothing to roll back beyond the tests.

## 13. Residual risk

- Root orchestrator with full catalogue still pays full schema mass; fixing
  that is **agent TOML / RequireExplicitTools policy**, not Stage A code.
- Prompt-cache cost of `/agent` switch is accepted; not measured here.
- MultiStepHandler accepts pre-scoped FullRegistry by convention - a future
  caller that passes an unscoped full registry without allowlist would
  over-advertise; the guard test pins the production construction path.
- Relevance remains the only large remaining schema economy for a full-root
  agent and is explicitly deferred.

## 14. Out of scope

- Per-schema compression / abbreviation.
- Provider-side tool caching APIs.
- Changing Capability class semantics.
- Mid-turn relevance pruning.
- Plan `04` class-aware divisors (ships after this).

## 15. Completion report template (after implement)

- Outcome (gaps found / none; tests added)
- Changed files
- Verification commands + results
- Schema mass before/after for one restricted agent fixture
- Residual risk / relevance trigger status
