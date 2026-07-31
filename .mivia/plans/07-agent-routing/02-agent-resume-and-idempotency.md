# 07.2 — Agent resume and idempotency

**Goal:** Ensure retries and resume never change or cross the authority of a named agent.
**Depends on:** phase `01` and plan `12`.

## Rules

The ledger restores work, not authority. Do not persist an agent grant in a
workspace file the agent can modify. On resume, the caller must re-establish
access to the selected agent definition; missing, renamed, or changed effective
definitions fail closed.

Include the requested agent identity in the work fingerprint and idempotency
scope. A request from another agent or principal must behave as a new request or
an indistinguishable unauthorized request, never return a foreign live handle.

## TDD

RED before implementation: `TestResumeRechecksAgentAccess`,
`TestResumeFailsWhenAgentDefinitionChanges`,
`TestIdempotencyCrossAgentHandleDenied`, and
`TestAgentRenameInvalidatesInFlightResume`.

## Exit criteria

Resume and retry behavior is fail-closed, documented, and proven against both
definition mutation and cross-agent idempotency collisions.
