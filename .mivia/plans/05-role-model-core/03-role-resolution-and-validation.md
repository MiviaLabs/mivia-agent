# 05.3 — Role resolution and validation

**Status:** DESIGN — implementation follows phases `01` and `02`.
**Parent:** [`00-overview.md`](00-overview.md) §§3, 4, 7, 9, 10.
**Depends on:** phases `01` and `02`.
**Blocks:** phase `04`.

## Scope

Create the pure role model and Layer-B resolver. Keep it independent from CLI
dispatcher construction and reuse `tools.AllToolNames()` and the scope
primitive rather than implementing a second filter.

Owned files:

- `internal/roles/role.go` — `Spec`, `ResolvedRole`, `Guardrails`, `Origin`,
  and the role registry types.
- `internal/roles/resolve.go` — default-base resolution, inheritance cycles,
  replacement semantics, then `tools_add`/`tools_remove` deltas.
- `internal/roles/catalogue.go` — catalogue membership, nearest-name errors,
  and reserved-handler collision checks.
- `internal/roles/policy.go` — mandatory denylist and guardrail evaluation
  order, empty-toolset policy, description sanitization, and delegation to the
  phase-02 scope primitive. It must not implement a second registry filter.
- `internal/cli/agent_roles.go` — Layer-B loading and selection only. Split
  handler registration into the integration boundary so resolution remains
  testable without a session dispatcher.

Role-versus-skill collision checking requires loaded skill names. It therefore
belongs at the phase-04 seam after skill loading, or phase 04 must inject the
names into this validator. It must not be guessed from an empty registry.

## TDD and focused tests

Write resolver RED tests before implementation. Own:

- `TestRoleResolve_InheritsPool`;
- `TestRoleResolve_ToolsAddExtendsParent`;
- `TestRoleResolve_ToolsRemove`;
- `TestResolve_InheritanceCycle`;
- `TestResolve_EmptyToolsetRefused`;
- `TestResolve_EvalOrder`;
- `TestValidateAgainstCatalogue_UnknownToolName`;
- `TestRoleAllowlistIntersectsDisabledTools`;
- `TestWorkspaceRoleCannotShadowUserRole`;
- `TestGuardrails_WorkspaceCannotLoosen`;
- `TestRequireExplicitTools_DefaultRoleUnaffected`;
- `TestRoleDescriptionSanitized`;
- the reserved-handler half of the namespace collision tests.

Mutation proofs M1, M2, M4, M5, M6, M7, and M9 belong here. The `skills` field
must remain rejected unless the phase-01 release decision makes plan `06`
atomic and supplies its enforcement point.

## Exit criteria

Every resolved field has explicit presence semantics, every tool name is
classified as typo versus disabled, workspace input cannot loosen user
guardrails, and no CLI dispatcher or model switch is constructed here.
