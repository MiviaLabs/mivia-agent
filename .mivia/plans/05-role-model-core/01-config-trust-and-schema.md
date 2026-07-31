# 05.1 — Config trust and presence-preserving schema

**Status:** BLOCKED until the `skills` field decision is closed.
**Parent:** [`00-overview.md`](00-overview.md) §§2–6.
**Depends on:** shipped plans `01`, `04`, and `27`.
**Blocks:** phases `02`–`04`.

## Scope

Build the Layer-A representation that can distinguish user configuration from
workspace configuration and preserve TOML key presence. Read the two fixed
paths independently; do not route role loading through `config.Load`, whose
single-file candidate selection can hide the user's configuration.

The user file is authoritative. The workspace file is untrusted and is ignored
for role/prompt/skill-handler surfaces unless the user file enables
`[agents].load_workspace_config`. If the resolved user and workspace namespace
directories are the same, keep the user interpretation and discard the
workspace interpretation, including through symlinked paths.

## Owned production files

- `internal/config/agents.go` — presence-preserving role TOML types, fixed-path
  reads, source provenance, gate, and guardrail merge.
- `internal/config/types.go` — only the fields needed to carry the gate and
  source-filtered prompt configuration; keep unrelated config behavior stable.

`internal/skills/frontmatter.go` is explicitly out of scope. P2 is a no-op after
the TOML-only decision; do not revive markdown-role parsing.

## TDD and focused tests

Write RED tests before production changes, with test ownership in the config
package. Cover:

- `TestRoleResolve_ToolsAndToolsAddIsError` — including remediation text;
- `TestWorkspaceRolesGate`;
- `TestGate_IgnoredInWorkspaceConfig`;
- `TestRoleSpec_NilVsEmpty`;
- `TestRoleMaxTurnsZeroIsError`;
- `TestWorkspaceRolesRefusedWhenWorkspaceIsHome`;
- `TestGateKeepsUserMeaningWhenWorkspaceIsHome`.

Mutation proofs owned here are M3, M11, and M12. The focused gate is the config
package's targeted test command; the exact package path and test file must be
recorded when the implementation is performed.

## Exit criteria

The phase is complete only when source provenance, gate defaults, home-directory
collision handling, guardrail monotonicity, and nil-versus-empty presence are
proven by tests. It must also explicitly choose one of these release-safe paths:

1. reject `skills` in plan 05; or
2. make plan 05 and plan 06 one atomic delivery with a real enforcement point.

“Accept, warn, and ignore until 06” is not an exit state.
