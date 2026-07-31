# 06.02 — Agent allowlist resolution

**Goal:** Resolve `skills` from one file-backed agent definition with explicit nil/empty, inheritance, trust, and provenance semantics.
**Depends on:** `05` and [01](01-skill-metadata.md).

## Work

- Add the agent-file `skills` field only if its runtime enforcement point is
  available; otherwise fail closed and do not publish a misleading schema.
- Define the three states precisely: root omission means all trusted skills,
  explicit `[]` means none, and inherited omission copies the parent's decision.
- Resolve skill names against the same trusted source policy as agent files.
  A workspace skill must not shadow a user skill silently; preserve origin in
  the effective snapshot and reject ambiguous collisions where required.
- Reject unknown skill names before handler registration and keep the resolved
  allowlist immutable.

## Verification

- `TestAgentSkillAllowlist_OmittedAllowsAll`;
  `TestAgentSkillAllowlist_EmptyAllowsNone`; `TestAgentSkillsInherited`;
  `TestUnknownSkillRejected`; `TestWorkspaceSkillCannotShadowUserBinding`;
  `TestWorkspaceGateRequired`.
- Verify an agent snapshot cannot be changed by mutating source maps after load.
- `go test ./internal/agents/...` and `go test -race ./internal/agents/...`.
