# 41 - Deterministic context compaction

**Status:** DESIGN - implementation-ready only after ADLC Step 0 challenge.
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
type ContextManager interface {
    Prepare(ctx context.Context, messages []provider.Message, budget int) (PreparedContext, error)
}
```

The concrete implementation should expose a pure planning function for tests
and a separate summarization/persistence adapter. The prepared context should
carry the messages sent to the provider, source range metadata, and whether a
compaction occurred.

The summary must use a stable, versioned structure with fields for objective,
state, decisions, evidence, changed surfaces, open work, risks, and source
range. Do not make the summary itself an authorization or policy source.

## 5. Integration points

- `internal/provider/context.go`: retain pairing-safe pruning helpers and add
  compaction-aware context assembly; keep token estimation in one package.
- `internal/agent/loop.go`: replace direct prune-only preparation with the
  context manager before each provider request; emit a dedicated compaction
  event with bounded metadata.
- `internal/chat/session.go`: use the same preparation path for plain chat and
  agent turns; preserve stale-turn and clear/load generation guards.
- `internal/subagents/multi_step.go` and `oneshot.go`: inherit the same policy
  and budget; nested compaction must not gain orchestration or persistence
  authority.
- `internal/chat/persistence.go` or the future ledger-backed session store:
  persist checkpoint metadata and exact source ranges without deleting source
  messages.

## 6. Required test matrix

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

## 7. Observability and UX

Add bounded `EventCompaction` detail containing only trigger type, before/after
estimated tokens, source-range identifiers, and summary version. Do not emit
summary content in routine progress events. The TUI/REPL may display a short
notice such as `context compacted: 82k -> 49k tokens`.

## 8. Verification gates

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

## 9. Rollback criterion

Return to Step 0 if one preparation path cannot serve plain chat, agent loops,
and nested agents; if a valid provider request cannot be proven after
compaction; or if persistence cannot retain an exact source range without
turning the checkpoint into an authorization or privacy boundary.
