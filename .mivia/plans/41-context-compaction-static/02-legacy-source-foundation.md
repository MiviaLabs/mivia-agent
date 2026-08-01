# 41.02 — Legacy migration and sanitized source foundation

Status: blocked pending `01`.

Goal: establish the durable logical source model before checkpointing. The
SQLite/event path is the supported source of truth; JSONL is never upgraded into
a transactional checkpoint backend.

Exact scope:

- `internal/contextstate/source.go` and `_test.go`: add versioned source-event,
  source-range, sanitized payload, retention class, and provenance records.
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
type SourceReader interface { ReadRange(context.Context, SourceRange) ([]SourceEvent, error); ReadPayload(context.Context, string) ([]byte, error) }
type ImportResult struct { SessionID string; SourceRange SourceRange; Revision Revision; Imported int; IdempotencyKey string; Warnings []string }
type LegacyImporter interface { Import(context.Context, string) (ImportResult, error) }
```

Raw credentials, hidden reasoning, provider secrets, and unbounded tool output
are rejected before append. When no redaction policy exists, content-bearing
summary/source payloads are not appended; structural metadata remains allowed.

Import behavior is deterministic and idempotent: existing legacy sessions are
read once, assigned `SourceSequence=0` until imported, and converted to a new
session/source range. Partial imports roll back; repeat imports with the same
idempotency key return the original `ImportResult`. JSONL remains readable for
export/rollback but cannot publish a checkpoint.

ADLC micro-tasks:

| ID | Wave | Type | File | Test/function, dependency, command, timeout, context |
|---|---|---|---|---|
| 02-RED-001 | 1 | RED | `internal/contextstate/source_test.go` | `TestSourceEventValidation`; depends 01-REVIEW-001; `go test -run '^TestSourceEventValidation$' ./internal/contextstate`; 60s; source.go, source_test.go |
| 02-GREEN-001 | 2 | GREEN | `internal/contextstate/source.go` | `ValidateSourceEvent`; depends 02-RED-001; same command; 60s; source.go, source_test.go |
| 02-RED-002 | 2 | RED | `internal/storage/sqlite_test.go` | `TestContextSchemaMigration`; depends 02-GREEN-001; `go test -run '^TestContextSchemaMigration$' ./internal/storage`; 120s; sqlite.go, sqlite_test.go |
| 02-GREEN-002 | 3 | GREEN | `internal/storage/sqlite.go` | `migrateContextSchema`; depends 02-RED-002; same command; 120s; sqlite.go, sqlite_test.go |
| 02-RED-003 | 3 | RED | `internal/storage/context_source_test.go` | `TestSQLiteContextSourceRoundTrip`; depends 02-GREEN-002; `go test -run '^TestSQLiteContextSourceRoundTrip$' ./internal/storage`; 120s; context_source.go, context_source_test.go, sqlite.go, sqlite_test.go |
| 02-GREEN-003 | 4 | GREEN | `internal/storage/context_source.go` | `AppendSourceEvents`; depends 02-RED-003; same command; 120s; context_source.go, context_source_test.go, sqlite.go, sqlite_test.go |
| 02-RED-004 | 4 | RED | `internal/chat/session_store_test.go` | `TestLegacyImportRollbackAndIdempotency`; depends 02-GREEN-003; `go test -run '^TestLegacyImportRollbackAndIdempotency$' ./internal/chat`; 60s; session_store.go, session_store_test.go, context_source.go |
| 02-GREEN-004 | 5 | GREEN | `internal/chat/session_store.go` | `ImportLegacy`; depends 02-RED-004; same command; 60s; session_store.go, session_store_test.go, context_source.go |
| 02-REVIEW-001 | 6 | review | `docs/architecture/embedded-persistence.md` | `TestPersistenceDocsMatchContract`; depends 02-GREEN-004; `make docs-check`; 60s; embedded-persistence.md, OWNERS.yaml |
| 02-REVIEW-002 | 6 | review | `docs/security/overview.md` | `TestContextRetentionContract`; depends 02-GREEN-004; `make docs-check`; 60s; overview.md, OWNERS.yaml |

Gate: source identity survives reload; legacy sessions import deterministically;
no sensitive payload is durably appended under any redaction configuration.
