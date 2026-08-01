# 05.3 - Agent-definition resolution and validation

**Status:** SHIPPED with parent plan 05.
**Goal:** Resolve file-backed agent definitions into immutable, validated runtime snapshots.
**Depends on:** phases `01` and `02`.
**Blocks:** phase `04`.

## Scope

Create the pure `internal/agents` package. It must not construct CLI dispatchers
or implement a second registry filter.

Owned files:

- `internal/agents/agent.go` - `AgentSpec`, `ResolvedAgent`, provenance, and
  immutable `AgentRegistry` types;
- `internal/agents/resolve.go` - inheritance, replacement semantics, and
  `tools_add`/`tools_remove` deltas;
- `internal/agents/catalogue.go` - filename/name, source, catalogue, skill,
  and reserved-handler collision checks;
- `internal/agents/policy.go` - global guardrail evaluation, empty-toolset
  policy, and description sanitization; delegates scope filtering to
  `internal/tools`;
- `internal/cli/agent_definitions.go` - Layer-B loading and `--agent` selection
  only; handler construction belongs to phase `04`.

Inheritance is only between file-backed agent definitions with explicit source
rules. A workspace definition cannot use inheritance to bypass the user gate or
widen global guardrails. The private compiled fallback is not a named public
parent. Missing parents, cycles, source-boundary violations, and changed parent
snapshots fail closed.

## TDD and focused tests

Write resolver RED tests before implementation. Own:

- `TestAgentResolve_InheritsPool`;
- `TestAgentResolve_ToolsAddExtendsParent` and `TestAgentResolve_ToolsRemove`;
- `TestAgentResolve_InheritanceCycle` and source-boundary inheritance tests;
- `TestAgentResolve_EmptyToolsetRefused`;
- `TestValidateAgainstCatalogue_UnknownToolName`;
- `TestAgentAllowlistIntersectsDisabledTools`;
- `TestWorkspaceAgentCannotShadowUserAgent`;
- `TestUserGuardrailsCannotBeLoosenedByWorkspaceAgent`;
- `TestAgentDescriptionSanitized`;
- agent-vs-skill and agent-vs-reserved-handler collision tests.

Mutation proofs M1, M2, M4, M5, M6, M7, and M9 belong here. `skills` remains
rejected unless plan `06` is made atomic with this delivery.

## Exit criteria

Every named file produces one immutable agent definition, every tool name is
classified as typo versus disabled, source trust is preserved through
inheritance, and no CLI dispatcher or model switch is constructed here.
