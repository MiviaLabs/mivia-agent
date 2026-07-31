# 08.03 — Identity and observability

**Goal:** Keep definition, instance, and model-generation identity distinct across every user-facing surface.
**Depends on:** [01](01-agent-catalog-cli.md), [02](02-doctor-and-config-diagnostics.md), and `07`.

## Work

- Define the event/report metadata for `agent_definition`, `agent_instance`,
  and `model_generation`; add a distinct lifecycle signal only where existing
  subagent start/end events cannot express the transition.
- Show the active agent and immutable snapshot/generation in the plain REPL,
  TUI banner/status, and both slash routers.
- Preserve the selected agent, prompt provenance, effective scope, and
  instance identity across `/model` changes. Treat the file's `model` as the
  default; an interactive override applies only to the current instance and
  cannot mutate the definition or its policy.
- Show effective tools once at definition/instance boundaries, not as repeated
  privilege payloads on every tool event.

## Verification

`TestSlashAgentsWorksInPlainREPLAndTUI`, `TestREPLAndTUIShowActiveAgent`,
`TestAgentDefinitionAndInstanceIdentityAreDistinct`,
`TestAgentLifecycleEmitsEffectiveToolsOnce`,
`TestModelSwitchPreservesAgentDefinitionAndScope`,
`TestModelSwitchRespectsAgentModelPolicy`,
`TestConcurrentInstancesShareDefinitionButNotRegistry`, and
`TestChangedDefinitionFailsClosedOnResume`.
