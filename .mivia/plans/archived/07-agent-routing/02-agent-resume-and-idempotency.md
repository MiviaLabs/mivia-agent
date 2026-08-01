# 07.2 — Agent resume and idempotency

**Goal:** Ensure retries and resume never change or cross the authority of a
named agent.
**Depends on:** phase `01` and plan `12`.

## Rules

The ledger restores work metadata, not authority. Persist the canonical agent
name, effective definition digest, and explicit skill identity as work metadata
needed for replay, but never treat a persisted handler or task name as a grant.
Do not persist an editable workspace grant.

Before resume or retry executes any task, the caller must establish its current
principal and authorized agent registry. Re-resolve the recorded agent by
canonical name, re-check the caller's dispatch boundary and skill policy, and
compare the effective definition digest. Missing, renamed, changed, or
unauthorized definitions fail closed before handler invocation or ledger
mutation.

The work fingerprint and idempotency scope include the canonical agent identity,
effective definition digest, explicit skill identity, task input, and other
work-defining fields. Runtime handler names, aliases, display names, and caller
metadata are not substitutes for the canonical target. A request from another
principal or agent must behave as a new request or an indistinguishable
unauthorized request; it must never return a foreign live handle.

## TDD

RED before implementation:
`TestResumeRequiresCurrentAgentAccess`,
`TestResumeFailsWhenAgentDefinitionChanges`,
`TestResumeRejectsPersistedHandlerAuthority`,
`TestResumeRechecksSkillPolicy`, `TestIdempotencyIncludesAgentIdentity`,
`TestIdempotencyIncludesDefinitionDigest`,
`TestIdempotencyCrossAgentHandleDenied`, and
`TestAgentRenameInvalidatesInFlightResume`.

Add mutation proofs for omitted agent identity, stale definition digests,
cross-principal replay, and any code path that invokes a persisted runtime
handler without current agent resolution.

## Exit criteria

Resume and retry are fail-closed, documented, and proven against missing,
renamed, changed, and unauthorized definitions; explicit skill policy is
rechecked; and idempotency cannot alias two agent snapshots or return another
agent's handle.
