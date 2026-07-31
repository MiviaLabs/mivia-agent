# 05.1 — Config trust and agent-file schema

**Status:** BLOCKED until the file-safety and `skills` decisions are closed.
**Goal:** Discover one safe, presence-preserving TOML definition per named agent.
**Parent:** [`00-overview.md`](00-overview.md) §§2–5.
**Depends on:** shipped plans `01`, `04`, and `27`.
**Blocks:** phases `02`–`04`.

## Scope

Keep global settings in trusted `~/.mivia/mivia.toml`: the workspace gate and
guardrails only. Discover agent files independently from:

- `~/.mivia/agents/<name>.toml` — trusted user definitions;
- `<workspace>/.mivia/agents/<name>.toml` — gated workspace definitions.

The normalized filename is the canonical name and the in-file `name` must match.
Read each file with source provenance. Reject unknown keys, malformed TOML,
duplicate names, invalid names, non-regular files, symlinked directories/files,
hardlink ambiguity, path escapes, and replacement races. Resolve source
directories through `EvalSymlinks`; when user and workspace directories are the
same, keep only the user interpretation.

Workspace `[agents]` gate/guardrail values are ignored with a warning. The user
file is the only authority for those global controls.

## Owned production files

- `internal/config/agents.go` — global settings, fixed-directory discovery,
  provenance, safe reads, and presence-preserving top-level agent TOML types;
- `internal/config/types.go` — only the fields needed to carry filtered prompt
  configuration and global agent settings.

`internal/skills/frontmatter.go` remains out of scope. The old markdown-agent
parser is not revived.

## TDD and focused tests

Write RED tests before production changes. Own tests for:

- one-file top-level parsing and `TestAgentSpec_NilVsEmpty`;
- filename/name agreement and unknown-key rejection;
- `TestWorkspaceAgentsGate` and `TestWorkspaceGlobalSettingsIgnored`;
- `TestWorkspaceAgentsRefusedWhenWorkspaceIsHome`;
- `TestGateKeepsUserMeaningWhenWorkspaceIsHome`;
- symlink directory/file, hardlink, path-escape, and replacement-race refusal;
- `TestAgentMaxTurnsZeroIsError` and mutual-exclusion remediation text.

Mutation proofs M3, M11, and M12 belong here. Add the new file-safety cases to
the eventual invariant row; do not leave them as prose only.

## Exit criteria

Layer A yields an immutable, source-tagged collection of distinct agent file
definitions; global guardrails have exactly one trusted owner; and the release
decision for `skills` is either rejection in `05` or an atomic `05`+`06`
delivery with enforcement. “Accept, warn, and ignore” is not an exit state.
