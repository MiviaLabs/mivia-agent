# 41.02 — Legacy migration and sanitized source foundation

Status: blocked pending `01`.

Goal: establish the durable logical source model before checkpointing. The
SQLite/event path is the supported source of truth; JSONL is never upgraded into
a transactional checkpoint backend.

Exact scope:

- `internal/storage/` and `internal/ledger/`: add versioned source-event and
  source-range records with session ID, sequence, role/type, bounded content or
  content reference, provenance, retention class, and redaction status.
- `internal/chat/session_store.go` and `internal/chat/session_store_test.go`:
  retain legacy load/save and add explicit import/export adapters only.
- `docs/architecture/embedded-persistence.md` and owned security documentation:
  define purpose, data owner, retention, deletion, export, access, and audit
  behavior for sanitized source events and checkpoints.

Required API decisions:

```go
type SourceEvent struct { ID SourceID; Kind string; PayloadRef string; Size int; Provenance string }
type SourceAppend struct { SessionID string; ExpectedRevision Revision; Events []SourceEvent }
type SourceReader interface { ReadRange(context.Context, SourceRange) ([]SourceEvent, error) }
type LegacyImporter interface { Import(context.Context, string) (Revision, error) }
```

Raw credentials, hidden reasoning, provider secrets, and unbounded tool output
are rejected before append. When no redaction policy exists, content-bearing
summary/source payloads are not appended; structural metadata remains allowed.

ADLC micro-tasks:

| Wave | Type | File | Task / verification |
|---|---|---|---|
| 1 | RED | `internal/storage/source_events_test.go` | Malformed, oversized, duplicate, secret-class, and source-range tests. |
| 2 | GREEN | `internal/storage/source_events.go` | Implement sanitized source-event schema and validators. |
| 2 | RED | `internal/chat/session_store_test.go` | Legacy import/export round-trip and no-checkpoint migration tests. |
| 3 | GREEN | `internal/chat/session_store.go` | Wire explicit compatibility adapter without claiming checkpoint durability. |
| 4 | review | owned persistence/security docs | Verify retention, deletion, export, access, and audit semantics. |

Gate: source identity survives reload; legacy sessions import deterministically;
no sensitive payload is durably appended under any redaction configuration.
