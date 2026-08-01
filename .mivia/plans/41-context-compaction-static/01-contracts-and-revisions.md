# 41.01 — Contracts, authority, and revisions

Status: ready for ADLC implementation; final Step 0 blockers closed.

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
type BindingRevision struct { Provider, Model string; Generation uint64 }
type Revision struct { Session uint64; Durable uint64; Source uint64 }
type CommitToken struct {
    Revision Revision
    Binding BindingRevision
    Range SourceRange
    IdempotencyKey string
}
type CheckpointID struct {
    SessionID string
    SourceRange SourceRange
    Algorithm string
    SchemaVersion uint32
    SummaryModel string
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
type PreparationManager interface {
    Prepare(context.Context, PrepareInput) (Preparation, error)
    Discard(Preparation)
}
type CheckpointPublisher interface {
    Commit(context.Context, Preparation, TurnResult) error
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
    Principal Principal
    SessionID string
    Expected Revision
    ExpectedBinding BindingRevision
    NewSourceEvents []SourceEvent
    Checkpoint CheckpointRecord
    ActiveContext []byte
    NewSession, NewDurable, NewSourceSequence uint64
    NewBinding BindingRevision
    TurnID uint64
}
type AdvanceRequest struct {
    Principal Principal
    SessionID string
    Expected Revision
    ExpectedBinding BindingRevision
    NewSession, NewDurable, NewSourceSequence uint64
    NewBinding BindingRevision
    Reason string
}
type Principal struct { WorkspaceID, SessionID, SubjectID string }
type ContentRef struct { Ref, Namespace, SHA256 string; WorkspaceID, SessionID, SubjectID string; Size int }
type RetentionClass string
type SanitizedPayload struct { Ref ContentRef; Bytes []byte; HashOnly, Dereferenceable, Revoked bool; Retention RetentionClass }
type PayloadRecord struct { Ref ContentRef; Retention RetentionClass; Revoked bool; Data []byte }
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
type PolicySnapshot struct { SummaryEnabled bool; RedactionConfigured bool; Provider, Model, CredentialScope string; NetworkEnabled bool }
type CheckpointCandidate struct { ActiveContext []byte; SummaryMetadata []byte; SourceEvents []SourceEvent; SourceRange SourceRange }
// TurnResult is owned by contextmgr because provider messages are policy input.
// BuildCommitRequest is the only mapping from a completed turn to durable state.
type TurnResult struct { User, Assistant, Tool []provider.Message; Active []provider.Message; SourceEvents []SourceEvent; TurnID uint64 }
type SummaryRequest struct { Input []provider.Message; Budget, OutputLimit int; SourceRange SourceRange; Provider, Model string }
type Summary struct { Version uint32; Objective, State string; Decisions, Evidence, ChangedSurfaces, OpenWork, Risks []string; SourceRange SourceRange }
var (
    ErrInvalidDTO = errors.New("invalid context DTO")
    ErrPrincipalMismatch = errors.New("principal mismatch")
    ErrSessionNotFound = errors.New("session not found")
    ErrSessionTombstoned = errors.New("session tombstoned")
    ErrPayloadRevoked = errors.New("payload revoked")
    ErrSummaryUnavailable = errors.New("summary unavailable")
    ErrStaleRevision = errors.New("stale revision")
    ErrStaleBinding = errors.New("stale binding")
    ErrCheckpointConflict = errors.New("checkpoint conflict")
)
```

`PrepareInput`, `Preparation`, `TurnResult`, and `SummaryRequest` are
`contextmgr`-owned policy DTOs and may use `provider.Message`; they are never
declared in `contextstate`. The contextstate equivalents are canonical wire
bytes (`ActiveContext` and `SummaryMetadata`) plus validated source DTOs, so
the dependency-neutral package imports neither provider nor chat.

`internal/contextmgr` owns schema validation, provenance framing, exact limits,
and preparation tokens. `internal/contextstate` owns `SourceID`, `SourceRange`,
`Revision`, `CheckpointRecord`, `Snapshot`, `CommitRequest`, `AdvanceRequest`,
and the errors. Persistence owns durable CAS; chat owns session revision and
current-turn publication. Nested handlers receive only a preparation-only
capability and cannot receive a checkpoint writer, session store, or root
dispatcher.

All DTOs use constructors plus `Validate` methods; serialization is canonical
JSON (sorted object keys, UTF-8, no unknown fields, no duplicate keys) and is
hashed only after validation. `contextmgr.BuildCommitRequest(context.Context,
Preparation, TurnResult, Principal, Revision, BindingRevision)
(contextstate.CommitRequest, error)` is the sole mapper: it serializes the final
user/assistant/tool exchange and active-message projection into
`ActiveContext`, copies new source events, assigns `TurnID`, computes the new
session/durable/source revisions and binding generation, and preserves the
checkpoint idempotency key. It rejects missing post-turn state or a mismatched
principal before calling `Store.Commit`. `ErrInvalidDTO`, `ErrPrincipalMismatch`,
`ErrSessionTombstoned`, `ErrPayloadRevoked`, and `ErrSummaryUnavailable` are
stable sentinel-backed typed errors alongside the CAS errors. Foreign
principal, missing session, malformed reference, revoked payload, and
tombstoned session map respectively to `ErrPrincipalMismatch`,
`ErrSessionNotFound`, `ErrInvalidDTO`, `ErrPayloadRevoked`, and
`ErrSessionTombstoned`; all support `errors.Is`/`errors.As`. Repeated deletion
returns the original tombstone result without advancing the revision.

`CommitRequest.Checkpoint.Revision` must equal its `NewSession/NewDurable` and
source-sequence values; `Checkpoint.Binding` must equal `NewBinding`; its
`SourceRange` must cover exactly `NewSourceEvents`; and `TurnID` must match the
checkpoint and serialized active projection. `CheckpointID` equality is the
canonical tuple of session, range, algorithm, schema, summary model, and
idempotency key. Any mismatch is `ErrInvalidDTO` before CAS.

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
and 256 bytes per field; summary timeout 10 seconds. Workspace, session, and
subject IDs are each 128 bytes; a commit has at most 1,024 source events and
8 MiB aggregate event bytes; summary arrays have at most 32 entries; exports
have at most 100,000 records; deletion batches have at most 10,000 references;
each audit record is at most 1 KiB. Overflow, malformed
UTF-8, duplicate fields, and invalid IDs are deterministic local errors. These
are implementation constants, not provider or workspace configuration.

`Namespace` must equal `mivia.context.payload.v1` and is at most 64 bytes;
payload refs are lowercase `ctxp_<base32-no-padding-SHA256>` and at most 128
bytes; event `Kind`, `Role`, `Provenance`, and `RedactionStatus` are each at
most 256 bytes. The exact-boundary value is accepted; one byte/item over the
limit returns `ErrInvalidDTO` before any SQLite write. Audit batches are capped
at 1,000 records and deletion work at 10,000 references per transaction.

Sanitized payload references are content-addressed records in the new context
content namespace, never the existing raw orchestration `content` table.
`Load`, `Advance`, and payload reads require a matching `Principal`. Session deletion writes
a tombstone, revokes references, and prevents future reads; physical bytes may
remain because existing content retention is immutable. Export returns only
sanitized records and emits an audit event.

ADLC micro-tasks:

| ID | Wave | Type | File | Test/function, dependency, command, timeout, context |
|---|---|---|---|
| 01-BOOT-001 | 1 | bootstrap | `internal/contextstate/contracts.go` | package plus compile-safe DTO skeletons only; none; `go test ./internal/contextstate`; 60s; `internal/contextstate/contracts.go` |
| 01-BOOT-002 | 1 | bootstrap | `internal/contextmgr/contracts.go` | package plus compile-safe policy skeletons only; depends 01-BOOT-001; `go test ./internal/contextmgr`; 60s; `internal/contextmgr/contracts.go`, `internal/contextstate/contracts.go` |
| 01-RED-001 | 2 | RED | `internal/contextstate/contracts_test.go` | `TestRevisionAndCheckpointContracts`; depends 01-BOOT-001; `go test -run '^TestRevisionAndCheckpointContracts$' ./internal/contextstate`; 60s; `internal/contextstate/contracts.go`, `internal/contextstate/contracts_test.go` |
| 01-GREEN-001 | 3 | GREEN | `internal/contextstate/contracts.go` | `NewCommitRequest`; depends 01-RED-001; `go test -run '^TestRevisionAndCheckpointContracts$' ./internal/contextstate`; 60s; `internal/contextstate/contracts.go`, `internal/contextstate/contracts_test.go` |
| 01-RED-002 | 4 | RED | `internal/contextstate/contracts_test.go` | `TestCommitRequestValidation`; depends 01-GREEN-001; `go test -run '^TestCommitRequestValidation$' ./internal/contextstate`; 60s; `internal/contextstate/contracts.go`, `internal/contextstate/contracts_test.go` |
| 01-GREEN-002 | 5 | GREEN | `internal/contextstate/contracts.go` | `ValidateCommitRequest`; depends 01-RED-002; `go test -run '^TestCommitRequestValidation$' ./internal/contextstate`; 60s; `internal/contextstate/contracts.go`, `internal/contextstate/contracts_test.go` |
| 01-RED-003 | 6 | RED | `internal/contextmgr/contracts_test.go` | `TestPreparationTokenRejectsStaleBinding`; depends 01-GREEN-002; `go test -run '^TestPreparationTokenRejectsStaleBinding$' ./internal/contextmgr`; 60s; `internal/contextmgr/contracts.go`, `internal/contextmgr/contracts_test.go`, `internal/contextstate/contracts.go` |
| 01-GREEN-003 | 7 | GREEN | `internal/contextmgr/contracts.go` | `CapturePreparation`; depends 01-RED-003; `go test -run '^TestPreparationTokenRejectsStaleBinding$' ./internal/contextmgr`; 60s; `internal/contextmgr/contracts.go`, `internal/contextmgr/contracts_test.go`, `internal/contextstate/contracts.go` |
| 01-REVIEW-001 | 8 | review | `internal/contextstate/contracts.go` | Review dependency direction/schema/errors; depends 01-GREEN-003; `go test ./internal/contextstate ./internal/contextmgr`; 120s; `internal/contextstate/contracts.go`, `internal/contextstate/contracts_test.go`, `internal/contextmgr/contracts.go`, `internal/contextmgr/contracts_test.go` |

Rollback: any API requiring provider transport to import persistence, or any
decision that permits nested/root authority sharing, returns to Step 0.
