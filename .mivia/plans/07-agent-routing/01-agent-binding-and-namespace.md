# 07.1 — Agent binding and namespace

**Goal:** Register each named agent definition once and route task requests through one explicit `agent` field.
**Depends on:** plan `05` phases `01`–`04`.

## Changes

- Register one handler per immutable `AgentRegistry` entry under the canonical
  agent name.
- Add `agent` to `dispatch_tasks` and `spawn_agent`; it selects a file-backed
  agent definition. Preserve `handler` only for built-in compatibility when no
  `agent` is supplied.
- Make the agent list an enum in model-facing schemas and inject sanitized
  descriptions as routing hints.
- Rewrite compiled guidance that tells models to bypass named agents by always
  selecting `multi_step`.
- Reject collisions among agent names, skill names, and reserved handlers.

## TDD

RED before implementation: `TestDispatchAgentBinding`, `TestSpawnAgentField`,
`TestAgentEnumInParameters`, `TestAgentNameCollidesWithSkill`,
`TestConcurrentAgentHandlersShareImmutableDefinitions`.

## Exit criteria

Every task has at most one named agent selection, and a handler invocation
cannot access a mutable registry shared by another instance.
