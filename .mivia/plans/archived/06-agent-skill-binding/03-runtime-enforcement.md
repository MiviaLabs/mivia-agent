# 06.03 — Runtime enforcement

**Goal:** Enforce the selected agent's skill allowlist at every reachable task boundary without a handler or registry bypass.
**Depends on:** [02](02-agent-allowlist-resolution.md) and plan `07`'s explicit agent binding.

## Work

- Add a small policy/helper seam rather than growing the already large
  orchestration files.
- Require task creation to carry one canonical `agent` identity. A legacy
  `handler` value cannot select a different skill policy or evade the selected
  agent's allowlist.
- Apply the same check to dispatch, spawn, resume, retry, and any nested path
  that can synthesize a skill task. If nested instances cannot reach the path,
  prove that and document root-only scope.
- Build the skill/tool scope from an immutable definition snapshot per runtime
  instance; never reuse a mutable global registry across concurrent agents or
  model switches.
- Enforce `agent.Tools ⊇ skill.Tools` after metadata is present.

## Verification

- `TestAgentSkillAllowlist_PerInstance`;
  `TestSkillCannotBypassAgentSelection`; `TestSkillToolsSubsetOfAgentTools`;
  `TestConcurrentAgentSkillInstances`; `TestAgentSkillBindingSurvivesModelSwitch`.
- Mutation proofs for removing the dispatch check, using a stale registry, and
  selecting a skill through `handler`.
- `go test ./internal/cli/... ./internal/subagents/... -race`.
