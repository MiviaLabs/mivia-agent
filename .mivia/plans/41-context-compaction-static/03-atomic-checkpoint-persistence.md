# 41.03 — Atomic checkpoint persistence and crash recovery

Status: blocked pending `02`.

Goal: make source append, checkpoint metadata, active projection, and durable
revision one recoverable publication unit.

Protocol: append immutable source events, write a checkpoint record referencing
an exact `SourceRange`, then atomically publish the active-checkpoint pointer in
the SQLite transaction. A checkpoint record contains schema version, algorithm
version, summary metadata, source range, session/durable/source revisions,
binding generation, and idempotency key. Equal idempotency retries are no-ops;
same key with different content is a conflict; stale expected revisions return a
typed stale-write error.

Exact scope:

- `internal/storage/sqlite.go`: create `context_sessions`,
  `context_source_events`, and `context_checkpoints`; the existing generic
  `events` table remains orchestration-only.
- `internal/storage/context_store.go` and `_test.go`: implement
  `SQLite.Commit`, `SQLite.Advance`, and `SQLite.Load` for `contextstate.Store`.
- `internal/storage/sqlite_failure_test.go`: failure injection and reopen
  recovery. `internal/ledger` is intentionally not modified.

The exact atomic API is:

```go
func (s *SQLite) Commit(context.Context, contextstate.CommitRequest) error
func (s *SQLite) Advance(context.Context, contextstate.AdvanceRequest) error
func (s *SQLite) Load(context.Context, contextstate.Principal, string) (contextstate.Snapshot, error)
```

`Commit` validates expected session revision and binding generation, appends
source events, inserts checkpoint by idempotency key, updates the active pointer,
and advances durable revision in one transaction. `ErrStaleRevision`,
`ErrStaleBinding`, and `ErrCheckpointConflict` are the only expected conflict
outcomes. Recovery selects the newest complete committed pointer; incomplete
rows remain inspectable and are never treated as active.

Migration SQL is versioned and must enforce these constraints:

```sql
CREATE TABLE context_sessions(
  session_id TEXT PRIMARY KEY, session_revision INTEGER NOT NULL,
  durable_revision INTEGER NOT NULL, source_sequence INTEGER NOT NULL,
  provider TEXT NOT NULL, model TEXT NOT NULL,
  binding_generation INTEGER NOT NULL, active_checkpoint_id TEXT,
  tombstoned INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE context_source_events(
  session_id TEXT NOT NULL, sequence INTEGER NOT NULL,
  event_id TEXT NOT NULL UNIQUE, kind TEXT NOT NULL, role TEXT NOT NULL,
  payload_ref TEXT, payload_size INTEGER NOT NULL,
  provenance TEXT NOT NULL, redaction_status TEXT NOT NULL,
  PRIMARY KEY(session_id, sequence),
  FOREIGN KEY(session_id) REFERENCES context_sessions(session_id)
);
CREATE TABLE context_checkpoints(
  checkpoint_id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
  source_start INTEGER NOT NULL, source_end INTEGER NOT NULL,
  algorithm TEXT NOT NULL, schema_version INTEGER NOT NULL,
  summary_model TEXT NOT NULL, idempotency_key TEXT NOT NULL UNIQUE,
  session_revision INTEGER NOT NULL, durable_revision INTEGER NOT NULL,
  binding_generation INTEGER NOT NULL, turn_id INTEGER NOT NULL,
  summary_metadata BLOB NOT NULL, active_context BLOB NOT NULL,
  content_fingerprint TEXT NOT NULL, created_at TEXT NOT NULL,
  FOREIGN KEY(session_id) REFERENCES context_sessions(session_id),
  CHECK(source_start <= source_end)
);
```

`active_checkpoint_id` must reference a checkpoint for the same session and
source range; `content_fingerprint` is SHA-256 over canonical sanitized bytes.
The existing raw `content` table is never used for context payload references.

No in-place JSONL chunk deletion or rename loop is valid for checkpoint
correctness. Incomplete revisions remain inspectable and recovery selects the
newest complete committed revision.

ADLC micro-tasks:

| ID | Wave | Type | File | Test/function, dependency, command, timeout, context |
|---|---|---|---|---|
| 03-RED-001 | 1 | RED | `internal/storage/context_store_test.go` | `TestCommitRejectsStaleRevision`; depends 02-GREEN-002; `go test -run '^TestCommitRejectsStaleRevision$' ./internal/storage`; 120s; context_store.go, context_store_test.go, sqlite.go, contextstate/contracts.go |
| 03-GREEN-001 | 2 | GREEN | `internal/storage/context_store.go` | `Commit`; depends 03-RED-001; same command; 120s; context_store.go, context_store_test.go, sqlite.go, contextstate/contracts.go |
| 03-RED-002 | 2 | RED | `internal/storage/context_store_test.go` | `TestCommitIdempotencyAndConflict`; depends 03-GREEN-001; `go test -run '^TestCommitIdempotencyAndConflict$' ./internal/storage`; 120s; context_store.go, context_store_test.go, sqlite.go, contextstate/contracts.go |
| 03-GREEN-002 | 3 | GREEN | `internal/storage/context_store.go` | `insertCheckpoint`; depends 03-RED-002; same command; 120s; context_store.go, context_store_test.go, sqlite.go, contextstate/contracts.go |
| 03-RED-003 | 3 | RED | `internal/storage/sqlite_failure_test.go` | `TestCheckpointRecoveryAfterInjectedFailure`; depends 03-GREEN-002; `go test -run '^TestCheckpointRecoveryAfterInjectedFailure$' ./internal/storage`; 180s; sqlite_failure_test.go, context_store.go, sqlite.go, contextstate/contracts.go |
| 03-GREEN-003 | 4 | GREEN | `internal/storage/context_store.go` | `Load`; depends 03-RED-003; same command; 180s; context_store.go, sqlite_failure_test.go, sqlite.go, contextstate/contracts.go |
| 03-RED-004 | 4 | RED | `internal/storage/context_store_test.go` | `TestAdvanceUpdatesHeadWithCAS`; depends 03-GREEN-003; `go test -run '^TestAdvanceUpdatesHeadWithCAS$' ./internal/storage`; 120s; context_store.go, context_store_test.go, contextstate/contracts.go |
| 03-GREEN-004 | 5 | GREEN | `internal/storage/context_store.go` | `Advance`; depends 03-RED-004; same command; 120s; context_store.go, context_store_test.go, contextstate/contracts.go |
| 03-RED-005 | 5 | RED | `internal/storage/context_store_test.go` | `TestRecoverySelectsCommittedPointer`; depends 03-GREEN-004; `go test -run '^TestRecoverySelectsCommittedPointer$' ./internal/storage`; 120s; context_store.go, context_store_test.go, sqlite.go |
| 03-GREEN-005 | 6 | GREEN | `internal/storage/context_store.go` | `recoverActive`; depends 03-RED-005; same command; 120s; context_store.go, context_store_test.go, sqlite.go |
| 03-REVIEW-001 | 7 | review | `internal/storage/context_store.go` | CAS/transaction review; depends 03-GREEN-005; `go test -race ./internal/storage`; 180s; context_store.go, context_store_test.go, sqlite.go, sqlite_failure_test.go |

Gate: `go test -race ./internal/storage ./internal/ledger`; repeated crash/failure
tests pass; durable revision never moves backward and source events are never
deleted by compaction.
