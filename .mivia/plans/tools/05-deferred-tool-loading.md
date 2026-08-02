# tools/05 - Deferred tool loading: search-and-load tool surface

**Status:** DESIGN - delta over `51-harness-context-economics/05`
(tool-schema gating), which is DESIGN LOCKED for its Stage A
(authorization-truthful advertising). Do not start before 51.05 Stage A
ships; re-scope against whatever relevance gating 51.05 later stages define.
**Date:** 2026-08-02
**Depends on:** `51/05` Stage A (advertised iff invocable, one authority
source of truth). Pairs with plan `46` (prompt caching): a stable core
schema block is exactly what gets cached.
**Blast radius:** MEDIUM-HIGH - model-facing tool discovery contract, loop
toolSpecs, INV-AG-29 / schema-identity invariants from 51.05.

## 1. Problem

Every request serializes every registered tool schema
(`EstimateRequestCost` re-marshals all ToolSpecs per call). 51.05 Stage A
removes *unauthorized* tools; this plan addresses the remaining mass: tools
the agent is authorized for but will not use this session. Rarely-used,
schema-heavy tools (orchestration set, web set) are paid for on every turn
of every session.

## 2. Goal

A small always-present **core set** plus a `load_tools` discovery surface:
the model searches by need, the harness admits matching schemas into the
session's advertised set. Admission is monotonic within a session
(load-only, never unload mid-session) so prompt-cache prefixes and 51.05's
session-fixed-set reasoning stay intact.

## 3. Design

### 3.1 Tool tiers (host-defined, per agent definition)

- **core**: always advertised - file/search basics (`read_file`,
  `write_file`, `search_replace`, `list_dir`, `grep`, `glob`) plus
  `load_tools` itself. Config: `[tools] core = [...]` and per-agent
  override in the agent TOML (`EffectiveTools` remains the authorization
  ceiling; tiers only affect advertising, never authority - 51.05's
  invariant).
- **deferred**: authorized but not advertised until loaded.

### 3.2 `load_tools`

```json
{ "query": "web search", "names": ["fetch_url"] }   // either or both
```

- `names`: exact admission of authorized deferred tools.
- `query`: keyword match over name + description (lexical only - no model
  call, deterministic, testable).
- Returns the admitted tools' names and one-line descriptions; their full
  schemas appear in the next request's tool block. Unknown/unauthorized
  names return a bounded error naming valid candidates - never widening
  authority.
- A compact deferred-tool index (name + one-liner each, budget ~30 tokens
  per tool) is included in the system prompt so the model knows what exists.
  This index is part of the stable cached prefix.

### 3.3 Cache interaction (plan `46`)

Admission grows the tool block, invalidating the tools-prefix cache once
per load. Batch semantics: all names/matches in one call admit together;
description text nudges the model to load what it needs in one call.
Placement of the cache breakpoint after the tool block means a mid-session
load costs one cache refill - acceptable and observable (emit an event).

### 3.4 Subagents

Spawned scopes already get `Allowlist=EffectiveTools`; tiering applies the
same way, but task-focused subagents should usually ship with the exact
tools pre-admitted by the parent's task route (agent definition lists them)
- `load_tools` in a subagent is a fallback, not the norm.

## 4. Invariants

- Advertised ⊆ loaded ⊆ authorized; `load_tools` can never admit a tool
  outside `EffectiveTools` (51.05's authority SoT is the single check).
- Admission is monotonic per session; the advertised set is deterministic
  given the load-call sequence (resume-safe: persisted in session state and
  replayed on load).
- Dispatcher accepts calls only to advertised tools - a hallucinated
  deferred-tool call fails with "not loaded: use load_tools", not silently.
- Zero deferred tools configured -> behavior identical to today.

## 5. Steps

1. Land after 51.05 Stage A; align on the registry authority seam.
2. Tier config + agent-TOML surface; default everything core (opt-in).
3. `load_tools` tool, lexical matcher, bounded errors.
4. Session-state persistence of the admitted set + resume replay.
5. System-prompt deferred index; cache-event on admission.
6. Measure: schema bytes per request before/after on a real session corpus;
   ship the default deferred set (orchestration + web tools) only if the
   measured saving is material.

## 6. Testing

- Authority: load attempt outside EffectiveTools fails closed.
- Monotonicity + resume: kill/reload mid-session reproduces the set.
- Determinism: same queries -> same admissions (golden).
- Cost: request-size assertion with N deferred tools unloaded vs loaded.

## 7. Failure analysis

- Model thrash-loads everything turn 1: index one-liners are honest but
  minimal; per-session load-call cap (e.g. 8) with a bounded error after.
- Model stuck lacking a tool it never loads: the deferred index is always
  visible; failure mode is a visible "not loaded" error, never silence.
