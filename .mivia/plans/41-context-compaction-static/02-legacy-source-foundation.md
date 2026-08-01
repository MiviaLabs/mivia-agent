# 41.02 — Legacy migration and sanitized source foundation

Status: ready after phase `01` review.

Goal: establish the durable logical source model before checkpointing. The
SQLite/event path is the supported source of truth; JSONL is never upgraded into
a transactional checkpoint backend.

Exact scope:

- `internal/contextstate/source.go` and `_test.go`: add versioned source-event,
  source-range, sanitized payload, retention class, and provenance records;
  this package contains DTOs/interfaces only and never imports storage.
- `internal/storage/sqlite.go`: add the schema migration for context tables;
  `internal/storage/context_source.go` and `_test.go`: implement SQLite source
  append/read and bounded content-reference access. Do not modify ledger tables.
- `internal/chat/session_store.go` and `internal/chat/session_store_test.go`:
  retain legacy load/save and add explicit import/export adapters only.
- `docs/architecture/embedded-persistence.md` and `docs/security/overview.md`:
  define purpose, data owner, retention, deletion, export, access, and audit
  behavior for sanitized source events and checkpoints.

Required API decisions:

```go
type SourceEvent struct { ID SourceID; Kind string; Role string; PayloadRef string; Size int; Provenance string; RedactionStatus string }
type SourceReader interface {
    ReadRange(context.Context, Principal, SourceRange) ([]SourceEvent, error)
    ReadPayload(context.Context, Principal, ContentRef) (SanitizedPayload, error)
}
type DeleteResult struct { SessionID string; TombstoneRevision Revision; RevokedRefs int; AuditID string }
type ExportResult struct { SessionID string; Revision Revision; Records []byte; Count int; AuditID string }
type AuditRecord struct { ID, Action, WorkspaceID, SessionID, SubjectID string; Revision uint64; Size int; CreatedAt time.Time }
type SourceMapping struct { LegacyID, SessionID string; SourceStart, SourceEnd SourceID }
type CutoverState struct { Mode, LegacySessionID, SessionID string }
type RollbackToken struct { SessionID, IdempotencyKey, Digest string }
type ImportResult struct {
    SessionID string; SourceRange SourceRange; Revision Revision; Imported int
    IdempotencyKey string; Status string; SourceMap []SourceMapping
    Cutover CutoverState; Rollback RollbackToken; PartialArtifacts []ContentRef
    Warnings []string
}
type LegacyImporter interface { Import(context.Context, Principal, string, string) (ImportResult, error) }
type SessionLifecycle interface {
    DeleteSession(context.Context, Principal, string) (DeleteResult, error)
    ExportSession(context.Context, Principal, string) (ExportResult, error)
}
```

Raw credentials, hidden reasoning, provider secrets, and unbounded tool output
are rejected before append. When no redaction policy exists, content-bearing
summary/source payloads are not appended; structural metadata remains allowed.

`SanitizeSourcePayload(context.Context, Principal, []byte, RedactionPolicy)` is
host-owned and returns a `SanitizedPayload` in the `mivia.context.payload.v1`
namespace. The payload row stores workspace, session, and subject ownership,
SHA-256, byte size, redaction status, and revocation state. `ReadPayload`
requires all principal fields to match; it never dereferences the legacy raw
`content` table. An unconfigured policy returns hash/metadata only with
`Dereferenceable=false`.

`RedactionPolicy` is a host-owned classifier configuration, not model input;
the sanitizer rejects credentials, PII, hidden reasoning, raw prompts, and
unbounded tool output before hashing or storage. A hash-only result has no
payload bytes and no resolvable reference. The test matrix includes configured
redaction, absent policy, malformed UTF-8, and adversarial credential/tool
fixtures.

`DeleteSession` is authoritative: it transactionally writes a tombstone,
increments the session revision, marks every context payload reference revoked,
and appends one bounded audit record. Existing raw orchestration bytes may stay
for their independent retention contract, but all context reads, checkpoint
loads, and exports fail with `ErrSessionTombstoned`. `ExportSession` is a
principal-scoped, sanitized-only, bounded snapshot and also writes an audit
record. Cross-workspace, cross-subject, revoked-reference, and repeated-delete
tests are mandatory.

Retention is explicit for the new namespace: sanitized payloads and audit rows
carry a retention class selected by the host policy; a bounded maintenance
operation prunes only revoked/orphaned context payload rows after that class's
configured duration, never raw orchestration content. Tombstones and audit
records are retained for the compliance class and are not removed by payload
GC. GC is principal-independent but cannot make a non-revoked reference
unreadable.

Import behavior is deterministic and idempotent: existing legacy sessions are
read once, assigned `SourceSequence=0` until imported, and converted to a new
session/source range. Partial imports roll back; repeat imports with the same
idempotency key return the original `ImportResult`. JSONL remains readable for
export/rollback but cannot publish a checkpoint. The importer receives an
authorized principal and an authorized legacy-session handle (not an arbitrary
filesystem path) plus operation key, records a source digest manifest, and
rolls the whole import back on malformed input, cancellation, or storage
failure. Source append is not an exported storage operation; only `Store.Commit`
may publish checkpoint-bearing source rows.

ADLC micro-tasks:

| ID | Wave | Type | File | Test/function, dependency, command, timeout, context |
|---|---|---|---|---|
| 02-RED-001 | 1 | RED | `internal/contextstate/source_test.go` | `TestSourceEventValidation`; depends 01-REVIEW-001; `go test -run '^TestSourceEventValidation$' ./internal/contextstate`; 60s; `internal/contextstate/source.go`, `internal/contextstate/source_test.go` |
| 02-GREEN-001 | 2 | GREEN | `internal/contextstate/source.go` | `ValidateSourceEvent`; depends 02-RED-001; `go test -run '^TestSourceEventValidation$' ./internal/contextstate`; 60s; `internal/contextstate/source.go`, `internal/contextstate/source_test.go` |
| 02-RED-002 | 2 | RED | `internal/storage/sqlite_test.go` | `TestContextSchemaMigration`; depends 02-GREEN-001; `go test -run '^TestContextSchemaMigration$' ./internal/storage`; 120s; `internal/storage/sqlite.go`, `internal/storage/sqlite_test.go` |
| 02-GREEN-002 | 3 | GREEN | `internal/storage/sqlite.go` | `migrateContextSchema`; depends 02-RED-002; `go test -run '^TestContextSchemaMigration$' ./internal/storage`; 120s; `internal/storage/sqlite.go`, `internal/storage/sqlite_test.go` |
| 02-REVIEW-001 | 4 | review | `internal/storage/sqlite.go` | schema/ownership review; depends 02-GREEN-002; `go test ./internal/storage`; 120s; `internal/storage/sqlite.go`, `internal/storage/sqlite_test.go`, `internal/contextstate/contracts.go` |
| 02-RED-003 | 5 | RED | `internal/storage/context_source_test.go` | `TestSQLiteContextSourceRoundTrip`; depends 02-REVIEW-001; `go test -run '^TestSQLiteContextSourceRoundTrip$' ./internal/storage`; 120s; `internal/storage/context_source.go`, `internal/storage/context_source_test.go`, `internal/storage/sqlite.go` |
| 02-GREEN-003 | 6 | GREEN | `internal/storage/context_source.go` | `appendSourceEvents`; depends 02-RED-003; `go test -run '^TestSQLiteContextSourceRoundTrip$' ./internal/storage`; 120s; `internal/storage/context_source.go`, `internal/storage/context_source_test.go`, `internal/storage/sqlite.go` |
| 02-RED-004 | 7 | RED | `internal/storage/context_source_test.go` | `TestPrincipalScopedReadRangeAndPayload`; depends 02-GREEN-003; `go test -run '^TestPrincipalScopedReadRangeAndPayload$' ./internal/storage`; 60s; `internal/storage/context_source.go`, `internal/storage/context_source_test.go`, `internal/contextstate/contracts.go` |
| 02-GREEN-004 | 8 | GREEN | `internal/storage/context_source.go` | `ReadRange`; depends 02-RED-004; `go test -run '^TestPrincipalScopedReadRangeAndPayload$' ./internal/storage`; 60s; `internal/storage/context_source.go`, `internal/storage/context_source_test.go`, `internal/contextstate/contracts.go` |
| 02-REVIEW-002 | 9 | review | `internal/contextstate/source.go` | source ownership and API review; depends 02-GREEN-004; `go test ./internal/contextstate ./internal/storage`; 120s; `internal/contextstate/source.go`, `internal/contextstate/source_test.go`, `internal/storage/context_source.go`, `internal/storage/context_source_test.go` |
| 02-RED-005 | 10 | RED | `internal/storage/context_lifecycle_test.go` | `TestDeleteExportAuditAndRevocation`; depends 02-REVIEW-002; `go test -run '^TestDeleteExportAuditAndRevocation$' ./internal/storage`; 120s; `internal/storage/context_lifecycle.go`, `internal/storage/context_lifecycle_test.go`, `internal/contextstate/contracts.go` |
| 02-GREEN-005 | 11 | GREEN | `internal/storage/context_lifecycle.go` | `DeleteSession`; depends 02-RED-005; `go test -run '^TestDeleteExportAuditAndRevocation$' ./internal/storage`; 120s; `internal/storage/context_lifecycle.go`, `internal/storage/context_lifecycle_test.go`, `internal/contextstate/contracts.go` |
| 02-RED-006 | 12 | RED | `internal/storage/context_source_test.go` | `TestReadPayloadSanitizesAndDeniesForeignPrincipal`; depends 02-GREEN-005; `go test -run '^TestReadPayloadSanitizesAndDeniesForeignPrincipal$' ./internal/storage`; 120s; `internal/storage/context_source.go`, `internal/storage/context_source_test.go`, `internal/contextstate/contracts.go` |
| 02-GREEN-006 | 14 | GREEN | `internal/storage/context_source.go` | `ReadPayload`; depends 02-RED-006; `go test -run '^TestReadPayloadSanitizesAndDeniesForeignPrincipal$' ./internal/storage`; 120s; `internal/storage/context_source.go`, `internal/storage/context_source_test.go`, `internal/contextstate/contracts.go` |
| 02-REVIEW-003 | 15 | review | `internal/storage/context_lifecycle.go` | lifecycle, sanitizer, audit review; depends 02-GREEN-006; `go test ./internal/contextstate ./internal/storage`; 180s; `internal/storage/context_lifecycle.go`, `internal/storage/context_source.go`, `internal/storage/context_source_test.go`, `internal/contextstate/contracts.go` |
| 02-RED-007 | 16 | RED | `internal/storage/context_lifecycle_test.go` | `TestExportSessionIsSanitizedAndAudited`; depends 02-REVIEW-003; `go test -run '^TestExportSessionIsSanitizedAndAudited$' ./internal/storage`; 120s; `internal/storage/context_lifecycle.go`, `internal/storage/context_lifecycle_test.go`, `internal/contextstate/contracts.go` |
| 02-GREEN-007 | 17 | GREEN | `internal/storage/context_lifecycle.go` | `ExportSession`; depends 02-RED-007; `go test -run '^TestExportSessionIsSanitizedAndAudited$' ./internal/storage`; 120s; `internal/storage/context_lifecycle.go`, `internal/storage/context_lifecycle_test.go`, `internal/contextstate/contracts.go` |
| 02-REVIEW-004 | 18 | review | `internal/storage/context_lifecycle.go` | export/delete/audit review; depends 02-GREEN-007; `go test ./internal/contextstate ./internal/storage`; 180s; `internal/storage/context_lifecycle.go`, `internal/storage/context_source.go`, `internal/contextstate/contracts.go` |
| 02-RED-008 | 19 | RED | `internal/chat/session_store_test.go` | `TestLegacyImportRollbackAndIdempotency`; depends 02-REVIEW-004; `go test -run '^TestLegacyImportRollbackAndIdempotency$' ./internal/chat`; 60s; `internal/chat/session_store.go`, `internal/chat/session_store_test.go`, `internal/storage/context_source.go` |
| 02-GREEN-008 | 20 | GREEN | `internal/chat/session_store.go` | `ImportLegacy`; depends 02-RED-008; `go test -run '^TestLegacyImportRollbackAndIdempotency$' ./internal/chat`; 60s; `internal/chat/session_store.go`, `internal/chat/session_store_test.go`, `internal/storage/context_source.go` |
| 02-REVIEW-005 | 21 | review | `docs/architecture/embedded-persistence.md` | documentation contract review; depends 02-GREEN-008; `make docs-check`; 60s; `docs/architecture/embedded-persistence.md`, `docs/security/overview.md`, `docs/OWNERS.yaml` |
| 02-RED-009 | 22 | RED | `internal/contextstate/sanitize_test.go` | `TestSanitizeSourcePayloadConfiguredAndHashOnly`; depends 02-REVIEW-005; `go test -run '^TestSanitizeSourcePayloadConfiguredAndHashOnly$' ./internal/contextstate`; 120s; `internal/contextstate/sanitize.go`, `internal/contextstate/sanitize_test.go`, `internal/contextstate/source.go` |
| 02-GREEN-009 | 23 | GREEN | `internal/contextstate/sanitize.go` | `SanitizeSourcePayload`; depends 02-RED-009; `go test -run '^TestSanitizeSourcePayloadConfiguredAndHashOnly$' ./internal/contextstate`; 120s; `internal/contextstate/sanitize.go`, `internal/contextstate/source_test.go`, `internal/contextstate/source.go` |
| 02-REVIEW-006 | 24 | review | `internal/contextstate/sanitize.go` | sanitizer boundary review; depends 02-GREEN-009; `go test ./internal/contextstate`; 120s; `internal/contextstate/sanitize.go`, `internal/contextstate/sanitize_test.go`, `internal/contextstate/source.go` |

Gate: source identity survives reload; legacy sessions import deterministically;
no sensitive payload is durably appended under any redaction configuration.
