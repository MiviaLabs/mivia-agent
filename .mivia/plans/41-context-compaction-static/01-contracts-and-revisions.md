# 41.01 — Contracts, authority, and revisions

Status: blocked pending final Step 0 PASS.

This is the contract-lock phase. It makes no user-visible behavior change.

Locked decisions:

- Durable backend: existing embedded SQLite/event boundary under
  `internal/storage`/`internal/ledger`; `internal/chat` JSONL is import/export
  compatibility only.
- Durable DTO/revision owner: new dependency-neutral `internal/contextstate`
  package. Policy owner: new provider-independent `internal/contextmgr` package.
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
type Store interface {
    Commit(context.Context, CommitRequest) error
    Advance(context.Context, AdvanceRequest) error
    Load(context.Context, Principal, string) (Snapshot, error)
}

// Package contextstate owns all types below; contextmgr depends on it and
// storage implements it. These types must not import chat, agent, or storage.
type CommitRequest struct {
    SessionID string
    Expected Revision
    ExpectedBinding BindingRevision
    NewSourceEvents []SourceEvent
    Checkpoint CheckpointRecord
}
type AdvanceRequest struct {
    SessionID string
    Expected Revision
    ExpectedBinding BindingRevision
    NewSession, NewDurable, NewSourceSequence uint64
    NewBinding BindingRevision
    Reason string
}
type Principal struct { WorkspaceID, SessionID, SubjectID string }
type SourceEvent struct { ID SourceID; Kind, Role, PayloadRef, Provenance, RedactionStatus string; Size int }
type CheckpointRecord struct {
    ID CheckpointID
    Revision Revision
    Binding BindingRevision
    SourceRange SourceRange
    ActiveContext []byte
    SummaryMetadata []byte
    TurnID uint64
}
type Snapshot struct { Revision Revision; Binding BindingRevision; Active CheckpointRecord; Source []SourceEvent; Tombstoned bool }
type PolicySnapshot struct { SummaryEnabled bool; RedactionConfigured bool; Provider, Model string }
type CheckpointCandidate struct { ActiveContext []byte; SummaryMetadata []byte; SourceEvents []SourceEvent; SourceRange SourceRange }
type TurnResult struct { Assistant []provider.Message; SourceEvents []SourceEvent; TurnID uint64 }
type SummaryRequest struct { Input []provider.Message; Budget, OutputLimit int; SourceRange SourceRange; Provider, Model string }
type Summary struct { Version uint32; Objective, State string; Decisions, Evidence, ChangedSurfaces, OpenWork, Risks []string; SourceRange SourceRange; Untrusted bool }
type PreparationManager interface { Prepare(context.Context, PrepareInput) (Preparation, error); Discard(Preparation) }
type CheckpointPublisher interface { Commit(context.Context, Preparation, TurnResult) error }
var (
    ErrStaleRevision = errors.New("stale revision")
    ErrStaleBinding = errors.New("stale binding")
    ErrCheckpointConflict = errors.New("checkpoint conflict")
)
```

`internal/contextmgr` owns schema validation, provenance framing, exact limits,
and preparation tokens. `internal/contextstate` owns `SourceID`, `SourceRange`,
`Revision`, `CheckpointRecord`, `Snapshot`, `CommitRequest`, `AdvanceRequest`,
and the errors. Persistence owns durable CAS; chat owns session revision and
current-turn publication. Nested handlers receive only a preparation-only
capability and cannot receive a checkpoint writer, session store, or root
dispatcher.

The durable-first commit protocol is fixed: provider success → validate final
turn/source events → `Store.Commit` with expected revision/binding → publish
in-memory active messages → schedule autosave using the committed revision. Any
failure before publication discards the candidate and leaves prior active state
unchanged. `Commit` performs one transaction: CAS head, append source events,
insert idempotent checkpoint, update active pointer, advance durable revision.

Locked limits: summary input 16 KiB; each summary field 2 KiB; total serialized
summary 12 KiB; summary output 2,048 tokens; source ID 128 bytes; source event
payload 8 KiB; payload reference 256 bytes; source range 100,000 events;
checkpoint metadata 16 KiB; complete checkpoint 32 KiB; session context-state
aggregate 64 MiB; sanitized export 8 MiB; compaction event 4 metadata fields
and 256 bytes per field; summary timeout 10 seconds. Overflow, malformed
UTF-8, duplicate fields, and invalid IDs are deterministic local errors. These
are implementation constants, not provider or workspace configuration.

Sanitized payload references are content-addressed records in the new context
content namespace, never the existing raw orchestration `content` table.
`Load` and payload reads require a matching `Principal`. Session deletion writes
a tombstone, revokes references, and prevents future reads; physical bytes may
remain because existing content retention is immutable. Export returns only
sanitized records and emits an audit event.

ADLC micro-tasks:

| ID | Wave | Type | File | Test/function, dependency, command, timeout, context |
|---|---|---|---|
| 01-RED-001 | 1 | RED | `internal/contextstate/contracts_test.go` | `TestRevisionAndCheckpointContracts`; none; `go test -run '^TestRevisionAndCheckpointContracts$' ./internal/contextstate`; 60s; contracts.go, contracts_test.go |
| 01-GREEN-001 | 2 | GREEN | `internal/contextstate/contracts.go` | `NewCommitRequest`; depends 01-RED-001; same focused command; 60s; contracts.go, contracts_test.go |
| 01-RED-002 | 2 | RED | `internal/contextstate/contracts_test.go` | `TestCommitRequestValidation`; depends 01-GREEN-001; `go test -run '^TestCommitRequestValidation$' ./internal/contextstate`; 60s; contracts.go, contracts_test.go |
| 01-GREEN-002 | 3 | GREEN | `internal/contextstate/contracts.go` | `ValidateCommitRequest`; depends 01-RED-002; same focused command; 60s; contracts.go, contracts_test.go |
| 01-RED-003 | 3 | RED | `internal/contextmgr/contracts_test.go` | `TestPreparationTokenRejectsStaleBinding`; depends 01-GREEN-002; `go test -run '^TestPreparationTokenRejectsStaleBinding$' ./internal/contextmgr`; 60s; contracts.go, contracts_test.go, contextstate/contracts.go |
| 01-GREEN-003 | 4 | GREEN | `internal/contextmgr/contracts.go` | `CapturePreparation`; depends 01-RED-003; same focused command; 60s; contracts.go, contracts_test.go, contextstate/contracts.go |
| 01-REVIEW-001 | 5 | review | `internal/contextstate/contracts.go` | Review dependency direction/schema/errors; depends 01-GREEN-003; `go test ./internal/contextstate ./internal/contextmgr`; 120s; contracts.go, contracts_test.go, contextstate/contracts.go, contextstate/contracts_test.go |

Rollback: any API requiring provider transport to import persistence, or any
decision that permits nested/root authority sharing, returns to Step 0.
