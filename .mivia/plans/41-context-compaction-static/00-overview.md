# 41 - Deterministic context compaction

**Status:** IMPLEMENTED - durable context checkpoints, structural compaction,
revision fencing, privacy-preserving source projection, isolated nested-agent
preparation, and interactive `/compact`/usage surfaces are wired and verified.
passes.
**Date:** 2026-08-01
**Depends on:** shipped per-model prompt budgets (`28`/`29`), current agent-loop
pruning, and the embedded persistence checkpoint contract.
**Blocks:** `42` (agent-requested compaction).
**Blast radius:** HIGH - context preparation, provider calls, session history,
agent loops, subagents, persistence, privacy, and user-visible progress events.

## 1. Goal

Replace lossy over-budget pruning with deterministic, checkpointed compaction
that triggers at approximately 80% of the effective prompt budget, targets a
smaller working set, preserves tool-call validity, and never deletes durable
history.

## 2. Current ground truth

- `config.EffectivePromptTokens` computes usable prompt capacity after the
  model's output reserve and operator/session caps.
- `chat.Session`, `agent.Loop`, and nested subagents receive that effective
  budget.
- `agent.Loop` currently calls `provider.PruneMessagesKeepTurns` before every
  request and fails if the irreducible result remains over budget.
- The provider estimator is intentionally approximate and does not promise
  provider-side acceptance.
- `docs/architecture/embedded-persistence.md` requires compaction checkpoints
  to reference exact source ranges; compaction is not deletion.

## 3. Locked design decisions

1. The trigger is relative to the effective prompt budget `B`, not the physical
   model context window: `trigger = floor(B * 0.80)`.
2. The post-compaction target is `floor(B * 0.50)`, subject to preserving the
   system prompt, current user objective, latest active tool exchange, and a
   bounded recent verbatim tail.
3. The host owns the trigger, structural retention, pairing validation, and
   hard overflow fallback. The summarizing model supplies semantic compression
   only.
4. Raw history remains recoverable. The active context contains a checkpoint
   summary plus recent messages; persistence retains the source range and
   summary metadata.
5. Compaction is permitted only after a complete tool exchange. It must never
   split an assistant tool-call message from its tool results.
6. Summaries must not include hidden reasoning, credentials, unbounded tool
   output, or raw prompts. Large content is represented by existing bounded
   references where available.
7. A compaction failure is non-fatal while the context is still under the hard
   budget; if compaction is required to fit and cannot produce a valid context,
   fail closed with `ErrPromptBudgetExceeded`.

## 4. Proposed model

Add a context-management seam, keeping provider transport unaware of context
policy:

```go
type ContextManager struct {
    PreparationManager PreparationManager
    CheckpointPublisher CheckpointPublisher
}

// ContextManager is disabled unless every phase gate passes. It composes the
// two canonical seams; it does not add a second persistence or commit API.
```

The implementation boundary is split into two new packages:
`internal/contextstate` owns dependency-neutral durable DTOs, revisions, limits,
and errors; `internal/contextmgr` owns context policy and preparation. Provider
owns message shapes and estimation. `internal/storage.SQLite` owns the durable
implementation; `internal/ledger` is not the compaction API. Chat and agent
packages own per-turn publication. Provider transport remains unaware of policy.

The concrete implementation should expose a pure planning function for tests
and a separate summarization/persistence adapter. The prepared context should
carry the messages sent to the provider, source range metadata, and whether a
compaction occurred.

The summary must use a stable, versioned structure with fields for objective,
state, decisions, evidence, changed surfaces, open work, risks, and source
range. Do not make the summary itself an authorization or policy source.

## 5. Locked corrections from Step 0

1. Durable compaction targets the existing embedded SQLite/event persistence
   architecture. `FileSessionStore`/JSONL is legacy import/export and ordinary
   compatibility snapshot support only; it is not a checkpoint source of truth.
2. “Recoverable history” means sanitized, bounded logical source events and
   content references permitted by the persistence privacy contract. Credentials,
   hidden reasoning, provider secrets, and unbounded tool output are never made
   durable by this plan.
3. With redaction unconfigured, source/tool-bearing summaries are ephemeral and
   only bounded structural checkpoint metadata is persisted. No credential
   patterns are compiled into the binary.
4. One-shot subagents use the shared budget and pairing/validation contract, but
   have no eligible history to compact: an oversized system/objective pair is a
   deterministic local rejection. They never invoke the summarizer or persistence.
5. Session revision, durable revision, source-event sequence, and model/binding
   generation are distinct fields. Publication requires all captured values to
   match; stale work returns an explicit conflict and cannot mutate memory or
   disk.
6. The user-visible compaction path is disabled until planner, summary/privacy,
   persistence, typed event, and recovery gates are all complete. Foundation
   phases may land only if behavior remains unchanged.
7. Recoverable history is defined as: sanitized message metadata plus bounded
   sanitized content when an explicit workspace redaction policy is configured;
   otherwise only structural metadata and content hashes/references persist.
   Rejected content is omitted, never guessed or silently stored raw.
8. The data purpose is restoring a bounded workspace session context. The active
   workspace/session principal owns access; deletion logically tombstones the
   session, revokes all context references, and emits an audit event. Physical
   rows remain inspectable until the existing retention owner garbage-collects
   them; compaction never deletes durable history.
9. The atomic storage operation is `contextstate.Store.Commit`, which appends
   sanitized source events, inserts the checkpoint, updates the active pointer,
   and advances durable state in one SQLite transaction. `Advance` handles clear,
   switch, and other non-checkpoint revision changes.

## 5a. Final Step 0 corrections

The hostile review rerun found and closed these contract defects before
implementation: contextstate contains DTOs/interfaces only and SQLite owns
source/lifecycle implementations; `EnsureSession`, operation IDs, request
fingerprints, payload records, ordered turn envelopes, and explicit clear/switch
pointer fields are part of the durable contract; migrations are versioned and
reject dirty/newer schemas; checkpoint completion is explicit; and the CLI is
the sole SQLite owner while ledger/chat borrow the pointer. JSONL is never a
fallback for context-enabled turns or checkpoint failure. Summary providers
receive only a host-sealed bounded sanitized envelope, with default-deny
redaction, credential, network, and endpoint policy. Owner-bound principal
capabilities, composite owner constraints, versioned exports/audits, and a
dedicated source-event allowlist prevent caller-forged access and generic event
leakage. Authorized tombstoned reads return
`ErrSessionTombstoned`, not a tombstoned snapshot.

## 6. Integration points

- `internal/contextmgr/`: add the provider-independent manager, pure planner,
  summary schema, bounded validator, commit token, and feature gate.
- `internal/provider/context.go`: retain pairing-safe helpers and token
  estimation; do not make provider transport depend on persistence or policy.
- `internal/agent/loop.go`: replace direct prune-only preparation with the
  context manager before each provider request; emit a dedicated compaction
  event with bounded metadata.
- `internal/chat/session.go`: use the same preparation path for plain chat and
  agent turns; preserve stale-turn and clear/load generation guards.
- `internal/subagents/multi_step.go` and `oneshot.go`: inherit the same policy
  and budget; nested compaction must not gain orchestration or persistence
  authority.
- `internal/storage/` and its ledger/event boundary: persist sanitized source
  events, active projection, checkpoint metadata, exact source ranges, and
  atomic revision pointers. `internal/chat` adapts legacy JSONL only.

## 7. Required test matrix

- No compaction below the threshold; exact threshold; and one-token-over.
- Repeated compaction over a long tool-heavy run.
- Compaction target remains below budget with a large system prompt.
- One oversized current user turn remains an explicit local rejection.
- Multiple tool calls retain every matching tool result.
- Compaction never runs between an assistant tool call and its results.
- Summary failure leaves history unchanged and does not corrupt the next turn.
- Session save/load restores checkpoint and source-range metadata.
- Model switch recomputes trigger and target from the new effective budget.
- Plain chat, agent chat, and nested subagents use equivalent policy.
- Repeated compaction is idempotent for the same source range.
- No secrets, hidden reasoning, raw prompts, or unbounded tool output reach
  summary events, logs, fixtures, or persisted metadata.
- `go test -race` covers concurrent send, clear, load, model switch, and save.

## 8. Observability and UX

Add bounded `EventCompaction` detail containing only trigger type, before/after
estimated tokens, source-range identifiers, and summary version. Do not emit
summary content in routine progress events. The TUI/REPL may display a short
notice such as `context compacted: 82k -> 49k tokens`.

## 9. Verification gates

```text
go test ./internal/provider ./internal/agent ./internal/chat ./internal/subagents
go test -race ./internal/provider ./internal/agent ./internal/chat ./internal/subagents
go vet ./internal/provider ./internal/agent ./internal/chat ./internal/subagents
make verify
make docs-check
```

Before implementation, run ADLC Step 0 with architecture, correctness,
security/privacy, and persistence reviewers. Before declaring complete, run the
repository bug-audit loop and record only confirmed findings or targeted tests.

## 10. Rollback criterion

Return to Step 0 if one preparation path cannot serve plain chat, agent loops,
and nested agents; if a valid provider request cannot be proven after
compaction; or if persistence cannot retain an exact source range without
turning the checkpoint into an authorization or privacy boundary.

## 11. Step 0 validation disposition

Four independent hostile reviews were run against the current working tree:
architecture, correctness, security/privacy, and persistence/concurrency. All
returned `BLOCK`; no reviewer edited files. The scoped provider, agent, chat, and
subagent tests passed during review, but passing baseline tests does not validate
this design.

Confirmed blockers incorporated into the phase breakdown:

1. The current session save path persists only active `[]provider.Message`; it
   cannot preserve raw source history or exact source ranges after compaction.
2. Preparation must be transactional. A failed summary/provider call must not
   publish a summary, compacted history, or autosave.
3. `SessionStore` has no checkpoint, source-range, revision, or idempotent publish
   contract. The file-backed format must be explicitly extended or declared
   compatibility-only; “future ledger store” is not an implementation boundary.
4. One-shot subagents bypass the loop and therefore bypass any shared manager.
   Their deliberate policy (shared isolated preparation or explicit rejection)
   must be chosen and tested.
5. Summary safety cannot rely on model instructions. The host must validate a
   versioned, bounded schema, preserve untrusted provenance, and enforce the
   no-content compaction event contract even when redaction is unconfigured.
6. Clear, load, model switch, turn completion, and save require a monotonic
   revision/CAS fence so stale work cannot resurrect or overwrite newer state.
7. The 50% target is subordinate to mandatory content. Oversized system prompts,
   current objectives, tool exchanges, summary requests, and summary output need
   explicit deterministic rejection and accounting rules.

## 12. Phase order and gates

The phase files in this directory are the authoritative implementation breakdown.
Each phase is independently reviewable but must land in order:

1. `01-contracts-and-revisions.md` — locked APIs, state machine, backend, privacy,
   one-shot, limits, and feature gate.
2. `02-legacy-source-foundation.md` — sanitized source-event schema and JSONL
   import/export boundary.
3. `03-atomic-checkpoint-persistence.md` — SQLite/event checkpoint transaction,
   CAS, idempotency, manifests, and crash recovery.
4. `04-session-operation-fencing.md` — clear/load/switch/turn/autosave fences.
5. `05-structural-planner.md` — pure deterministic planner, no behavior flip.
6. `06-summary-privacy-publication.md` — bounded summarizer and transactional
   publication, still behind the feature gate.
7. `07-surface-integration.md` — plain chat, agent loops, and nested agents.
8. `08-events-audit-and-closeout.md` — typed events, UX, hostile audit, and gates.

`ADLC-task-catalog.md` contains the locked task IDs, exact functions, file
scopes, dependencies, commands, timeouts, and context scopes used for Step 1/2
validation.

No phase may begin production implementation until the revised plan passes a
new ADLC Step 0 challenge. Every implementation phase follows RED → GREEN,
uses one-file micro-tasks, and has a race-tested wave gate.

Phase landing rule: phases 01–04 are non-user-visible foundations; phase 05 is
testable but must not replace pruning; phases 06–07 land as one gated feature
slice (or remain disabled); phase 08 follows only after the slice is enabled and
verified.

## 13. Parallel plan/validate/question disposition

The parallel cycle ran four independent tracks: concrete planning, hostile
architecture/correctness challenge, security/privacy challenge, and ADLC Step 1
validation. All agreed that the previous phase files were still `BLOCKED` because
they named placeholder types and broad file groups rather than an implementable
transaction and task catalog.

The corrections are now incorporated:

- `internal/contextstate` is the dependency-neutral contract package;
- `internal/storage.SQLite` is the concrete backend and `internal/ledger` is not
  overloaded with context history;
- `Store.Commit`/`Advance`/`Load` and the SQLite transaction ordering are named;
- recoverability, retention, deletion, export, access, and unconfigured-redaction
  behavior are explicit;
- summary/provider/network and nested capability boundaries are explicit;
- typed compaction events precede surface integration;
- every listed implementation task has an exact file, function/test, dependency,
  command, timeout, and context scope in the phase files/catalog.

The remaining gate is a final hostile Step 0 review of this revised contract.
Only a panel result of `PASS` changes the status to implementation-ready.
