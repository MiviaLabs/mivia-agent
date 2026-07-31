# 05.2 — Tool catalogue and immutable scope primitives

**Status:** SHIPPED with parent plan 05.
**Goal:** Give every agent instance a fresh, correctly scoped registry without mutating published definitions.
**Depends on:** phase `01`; enforcement from plan `01`.
**Blocks:** phase `03` and dispatcher wiring in phase `04`.

## Scope

Provide reusable, agent-independent primitives. The published agent catalog and
each resolved definition are immutable. Each invocation derives a fresh loop,
dispatcher, and scoped registry; no concurrent instance mutates a shared
registry.

Owned files:

- `internal/tools/names.go` — sorted `AllToolNames()` catalogue;
- `internal/tools/scope.go` — scope filtering that preserves tool objects and
  the `PrivilegedTool` marker;
- `internal/subagents/names.go` — one reserved-handler set used by agent
  validation and dispatcher registration;
- `internal/subagents/multi_step.go` — only the thin delegation needed to build
  a per-invocation scope; no agent-definition policy here.

Root and spawned policies are distinct: the root may retain delegation tools
needed to dispatch, while spawned agents receive the mandatory denylist and
marker exclusion. A single ambiguous helper is not acceptable.

## TDD and focused tests

RED tests precede production changes. Own:

- `TestAllToolNamesMatchesFullRegistry`;
- `TestScopedRegistry`, including object markers;
- `TestMandatoryDenylistMatchesPrivilegedMarker`;
- `TestMandatoryDenylist_RootExempt_SpawnedFiltered`;
- a race test that fans out many instances from one immutable agent definition.

Mutation proof M8 belongs here. Plan `01`'s dispatch-boundary tests remain the
authorization boundary; a registry filter is not sufficient by itself.

## Exit criteria

An agent definition can be reused concurrently without shared mutable state, and
root/spawned scope behavior has explicit contradictory-case tests.
