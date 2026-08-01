# 41.01 — Contracts, authority, and revisions

Status: blocked pending final Step 0 PASS.

This is the contract-lock phase. It makes no user-visible behavior change.

Locked decisions:

- Durable backend: existing embedded SQLite/event boundary under
  `internal/storage`/`internal/ledger`; `internal/chat` JSONL is import/export
  compatibility only.
- Shared policy owner: new provider-independent `internal/contextmgr` package.
- One-shot policy: use the same hard-budget and validation contract, but reject
  an irreducible system-plus-objective pair locally; no history, summarizer, or
  persistence is available to one-shot handlers.
- Unconfigured redaction: summaries containing source/tool content are
  ephemeral; only bounded structural checkpoint metadata may persist.
- Summary transport: injected `SummaryProvider` using the existing provider
  abstraction; no raw HTTP client or implicit network fallback.
- Feature rollout: `ContextManager` is constructed in disabled mode until phases
  02–08 pass; disabled mode preserves existing pruning behavior.

Exact planned API (names may only change through a new Step 0 review):

```go
type SourceID struct { SessionID string; Sequence uint64 }
type SourceRange struct { Start, End SourceID }
type BindingRevision struct { Model string; Generation uint64 }
type Revision struct { Session uint64; Durable uint64; Source uint64 }
type CommitToken struct {
    Revision Revision
    Binding BindingRevision
    Range SourceRange
    IdempotencyKey string
}
type PrepareInput struct {
    Messages []provider.Message
    Budget int
    Revision Revision
    Binding BindingRevision
    Policy PolicySnapshot
}
type Preparation struct {
    Messages []provider.Message
    Candidate CheckpointCandidate
    Token CommitToken
    Compacted bool
}
type ContextManager interface {
    Prepare(context.Context, PrepareInput) (Preparation, error)
    Commit(context.Context, Preparation, TurnResult) error
    Discard(Preparation)
}
type SummaryProvider interface {
    Summarize(context.Context, SummaryRequest) (Summary, error)
}
type CheckpointStore interface {
    CommitCheckpoint(context.Context, CheckpointCommit) error
    LoadActive(context.Context, string) (CheckpointSnapshot, error)
}
```

`internal/contextmgr` owns schema validation, provenance framing, exact limits,
and preparation tokens. Persistence owns durable CAS; chat owns session revision
and current-turn publication. Nested handlers receive only an isolated manager
and cannot receive a checkpoint writer, session store, or root dispatcher.

Locked limits: summary input 16 KiB; each summary field 2 KiB; total serialized
summary 12 KiB; summary output 2,048 tokens; source ID 128 bytes; compaction
event 4 metadata fields and 256 bytes per field; summary timeout 10 seconds;
checkpoint metadata 16 KiB. Overflow, malformed UTF-8, duplicate fields, and
invalid IDs are deterministic local errors. These are implementation constants,
not provider or workspace configuration in this slice.

ADLC micro-tasks:

| Wave | Type | File | Task / verification |
|---|---|---|---|
| 1 | RED | `internal/contextmgr/contracts_test.go` | Assert source IDs, revisions, tokens, limits, and conflict errors; focused test fails by assertion. |
| 2 | GREEN | `internal/contextmgr/contracts.go` | Add the exact types and validation helpers; focused test passes. |
| 2 | RED | `internal/chat/revision_test.go` | Assert session/durable/model revisions and stale-token rejection. |
| 3 | GREEN | `internal/chat/revision.go` | Add revision capture/compare primitives; focused test passes. |
| 4 | review | plan + contract files | Validator checks dependency direction, API ownership, numeric limits, and one-shot decision. |

Rollback: any API requiring provider transport to import persistence, or any
decision that permits nested/root authority sharing, returns to Step 0.
