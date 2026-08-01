# 07.1 - Agent binding and namespace

**Goal:** Register each named agent definition once and make `agent` the sole
model-facing task selector.
**Depends on:** plan `05` phases `01`–`04` and the shipped skill policy from
plan `06`.

## Changes

- Register one private runtime handler per immutable `AgentRegistry` entry under
  the canonical agent name. Handler construction remains in the plan `05`
  seam; this phase only binds task selection to that registration.
- Require `agent` on every `dispatch_tasks` and `spawn_agent` task. Permit an
  explicit `skill` field only as a separate invocation target under that agent's
  policy; it never supplies or overrides `agent`.
- Remove model-facing `handler` and `name` selection. Reject those fields,
  `role`, built-in runner names, and unknown selector fields at both schema and
  decode boundaries. Do not default a missing agent or translate an old field.
- Make the available agent names an enum in model-facing schemas and inject
  sanitized descriptions as routing hints. Skill names, if exposed, get their
  own field and policy check rather than sharing the agent selector.
- Resolve each task against the caller's authorized immutable registry and
  intersect the derived task scope with the caller's dispatch boundary.
- Reject collisions among agent names, skill names, reserved runtime handlers,
  and tool names, reporting the conflicting source paths before registration.
- Rewrite compiled model guidance and tool descriptions so they describe
  `agent`/`skill` selection and never instruct the model to choose a runner.

## TDD

RED before implementation:
`TestDispatchAgentBinding`, `TestSpawnAgentField`,
`TestAgentFieldRequired`, `TestHandlerAndNameSelectorsRejected`,
`TestBuiltInRunnerCannotSelectAgent`, `TestSkillDoesNotReplaceAgent`,
`TestAgentEnumInParameters`, `TestTaskAgentScopeCannotWidenCaller`,
`TestAgentNameCollidesWithSkillToolAndReservedHandler`, and
`TestConcurrentInstancesShareImmutableDefinitions`.

Include direct JSON execution tests, not only schema inspection, so permissive
decoding cannot reintroduce rejected fields. The nested task object must reject
unknown properties and the required/enum contract must be exercised for both
tools.

## Exit criteria

Every task has exactly one named agent selection before coordinator or ledger
creation. Skill invocation, when present, is explicit and separately
authorized. No omitted or alternate selector can reach a built-in runner or a
different agent, and no handler invocation can access mutable registry state
shared by another instance.
