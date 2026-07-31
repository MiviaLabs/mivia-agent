# 05.2 — Tool catalogue and registry-scope primitives

**Status:** DESIGN — implementation follows phase `01`.
**Parent:** [`00-overview.md`](00-overview.md) §§3, 7, 8, 10.
**Depends on:** phase `01`; enforcement from plan `01`.
**Blocks:** phase `03` and the dispatcher wiring in phase `04`.

## Scope

Provide reusable, role-independent primitives. This phase must not resolve TOML
inheritance or duplicate role logic from `internal/roles`.

Owned files:

- `internal/tools/names.go` — sorted `AllToolNames()` catalogue, with a test
  proving it matches the complete configured registry under representative
  workspace/Tavily configuration.
- `internal/tools/scope.go` — scope filtering that preserves tool objects and
  the `PrivilegedTool` marker rather than filtering names alone.
- `internal/subagents/names.go` — one exported reserved-handler set used by
  both role validation and dispatcher registration.
- `internal/subagents/multi_step.go` — only the thin delegation needed to reuse
  the scope primitive; do not add role-resolution policy here.

The root and spawned policies are distinct and must be expressed as named
contracts. Root scoping preserves the delegation tools needed to dispatch;
spawned scoping applies the mandatory denylist and marker exclusion. A single
ambiguous helper that appears to implement both is not acceptable.

## TDD and focused tests

RED tests precede production changes. Own these tests here:

- `TestAllToolNamesMatchesFullRegistry`;
- `TestScopedRegistry`, including object markers;
- `TestMandatoryDenylistMatchesPrivilegedMarker`;
- `TestMandatoryDenylist_RootExempt_SpawnedFiltered`.

Mutation proof M8 belongs here. Plan `01`'s dispatch-boundary tests are a
prerequisite; this phase does not replace the authorization boundary with a
registry advertisement filter.

## Exit criteria

Role validation can ask for a complete catalogue without importing CLI wiring,
and both root and spawned scope behavior have explicit, contradictory-case
tests. The phase must leave one final registry object that phase `04` can pass
unchanged into every handler and dispatcher that executes under a role.
