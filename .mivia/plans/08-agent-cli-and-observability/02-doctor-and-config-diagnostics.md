# 08.02 — Doctor and configuration diagnostics

**Goal:** Report agent loading state clearly even when runtime providers are unavailable.
**Depends on:** [01](01-agent-catalog-cli.md).

## Work

- Extend `mivia doctor` with explicit loaded, gated, malformed, shadowed, and
  not-loaded states, including source and remediation.
- Keep inspection paths independent of provider credentials and dispatcher
  construction; a missing API key must not turn a useful config diagnosis into
  an empty success.
- Distinguish a gated workspace collection from a successfully empty
  collection, and preserve user-config authority over the gate.

## Verification

`TestDoctorReportsAgentLoadStateWithoutEmptySuccess`,
`TestGatedWorkspaceAgentIsNotShownAsActive`, malformed-file diagnostics, and
no-provider-credential tests. Run the focused CLI/config tests and a race pass.
