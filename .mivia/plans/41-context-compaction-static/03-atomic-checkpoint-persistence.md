# 41.03 — Atomic checkpoint persistence and crash recovery

Status: ready after phase `02` review.

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
  `context_source_events`, `context_payloads`, `context_audits`,
  `context_tombstones`, and `context_checkpoints`; the existing generic
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

`EnsureSession` validates principal ownership and creates the initial
zero-revision session head. `Commit` validates expected session revision and binding generation, appends
source events, inserts checkpoint by idempotency key, updates the active pointer,
and advances durable revision in one transaction. `ErrStaleRevision`,
`ErrStaleBinding`, and `ErrCheckpointConflict` are the only expected conflict
outcomes. Recovery selects the newest complete committed pointer; incomplete
rows remain inspectable and are never treated as active.
The SQL update predicates compare session, durable, source, provider, model, and
binding generation together. `OperationID` and a canonical request fingerprint
make retries safe after cancellation; same-key/different-fingerprint retries
are conflicts. Two independent SQLite handles must observe the same CAS result.

Migration SQL is versioned and must enforce these constraints:

```sql
CREATE TABLE context_sessions(
  workspace_id TEXT NOT NULL, subject_id TEXT NOT NULL,
  session_id TEXT PRIMARY KEY, session_revision INTEGER NOT NULL,
  durable_revision INTEGER NOT NULL, source_sequence INTEGER NOT NULL,
  provider TEXT NOT NULL, model TEXT NOT NULL,
  binding_generation INTEGER NOT NULL, active_checkpoint_id TEXT,
  tombstoned INTEGER NOT NULL DEFAULT 0,
  UNIQUE(workspace_id, session_id)
);
CREATE TABLE context_payloads(
  ref TEXT PRIMARY KEY, namespace TEXT NOT NULL,
  workspace_id TEXT NOT NULL, session_id TEXT NOT NULL, subject_id TEXT NOT NULL,
  sha256 TEXT NOT NULL, size INTEGER NOT NULL,
  redaction_status TEXT NOT NULL, retention_class TEXT NOT NULL,
  revoked INTEGER NOT NULL DEFAULT 0, data BLOB, created_at TEXT NOT NULL,
  FOREIGN KEY(session_id) REFERENCES context_sessions(session_id)
);
CREATE TABLE context_audits(
  audit_id TEXT PRIMARY KEY, action TEXT NOT NULL, workspace_id TEXT NOT NULL,
  session_id TEXT NOT NULL, subject_id TEXT NOT NULL, revision INTEGER NOT NULL,
  size INTEGER NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE context_tombstones(
  session_id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL,
  subject_id TEXT NOT NULL, revision INTEGER NOT NULL, created_at TEXT NOT NULL
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
  summary_model TEXT NOT NULL, operation_id TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  session_revision INTEGER NOT NULL, durable_revision INTEGER NOT NULL,
  binding_generation INTEGER NOT NULL, turn_id INTEGER NOT NULL,
  summary_metadata BLOB NOT NULL, active_context BLOB NOT NULL,
  content_fingerprint TEXT NOT NULL, complete INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  FOREIGN KEY(session_id) REFERENCES context_sessions(session_id),
  UNIQUE(session_id, operation_id),
  UNIQUE(session_id, idempotency_key),
  CHECK(source_start <= source_end),
  CHECK(complete IN (0,1))
);
```

The migration also creates `context_schema_migrations(version INTEGER PRIMARY
KEY, dirty INTEGER NOT NULL)` and advances `PRAGMA user_version` only after
each migration transaction commits. Startup rejects a newer schema and refuses
a dirty schema until repaired. `complete` is written as the final transaction
step; only complete checkpoints can become active. A failed transaction rolls
back all source, payload, checkpoint, pointer, and revision changes, while
injected step failures are covered by close/reopen tests.

`active_checkpoint_id` must reference a complete checkpoint for the same session and
source range; every non-empty `payload_ref` must reference
`context_payloads(ref, namespace)` in `mivia.context.payload.v1` with matching
session/workspace/subject and SHA-256. `content_fingerprint` is SHA-256 over
canonical sanitized bytes. Authorization checks run before every read and
mutation, and tombstone status wins over payload revocation when both apply.
The existing raw `content` table is never used for context payload references.
Composite foreign keys, triggers, and transactional owner checks enforce
same-session/workspace/subject relationships. SQLite foreign keys are enabled
for every pooled connection, not only the first connection.

No in-place JSONL chunk deletion or rename loop is valid for checkpoint
correctness. Incomplete revisions remain inspectable and recovery selects the
newest complete committed revision.
Failure injection covers after session creation, source append, payload insert,
checkpoint insert, active-pointer update, completion mark, and revision update;
each case closes and reopens the database and verifies no partial active state,
no orphan payload, monotonic revisions, and successful idempotent retry.

ADLC micro-tasks:

| ID | Wave | Type | File | Test/function, dependency, command, timeout, context |
|---|---|---|---|---|
| 03-RED-001 | 1 | RED | `internal/storage/context_store_test.go` | `TestCommitRejectsStaleRevision`; depends 02-REVIEW-006; `go test -run '^TestCommitRejectsStaleRevision$' ./internal/storage`; 120s; `internal/storage/context_store.go`, `internal/storage/context_store_test.go`, `internal/storage/sqlite.go`, `internal/contextstate/contracts.go` |
| 03-GREEN-001 | 2 | GREEN | `internal/storage/context_store.go` | `Commit`; depends 03-RED-001; `go test -run '^TestCommitRejectsStaleRevision$' ./internal/storage`; 120s; `internal/storage/context_store.go`, `internal/storage/context_store_test.go`, `internal/storage/sqlite.go`, `internal/contextstate/contracts.go` |
| 03-RED-002 | 3 | RED | `internal/storage/context_store_test.go` | `TestCommitIdempotencyAndConflict`; depends 03-GREEN-001; `go test -run '^TestCommitIdempotencyAndConflict$' ./internal/storage`; 120s; `internal/storage/context_store.go`, `internal/storage/context_store_test.go`, `internal/storage/sqlite.go`, `internal/contextstate/contracts.go` |
| 03-GREEN-002 | 4 | GREEN | `internal/storage/context_store.go` | `insertCheckpoint`; depends 03-RED-002; `go test -run '^TestCommitIdempotencyAndConflict$' ./internal/storage`; 120s; `internal/storage/context_store.go`, `internal/storage/context_store_test.go`, `internal/storage/sqlite.go`, `internal/contextstate/contracts.go` |
| 03-REVIEW-001 | 5 | review | `internal/storage/context_store.go` | commit/idempotency review; depends 03-GREEN-002; `go test ./internal/storage`; 180s; `internal/storage/context_store.go`, `internal/storage/context_store_test.go`, `internal/storage/sqlite.go`, `internal/contextstate/contracts.go` |
| 03-RED-003 | 5 | RED | `internal/storage/sqlite_failure_test.go` | `TestCheckpointRecoveryAfterInjectedFailure`; depends 03-REVIEW-001; `go test -run '^TestCheckpointRecoveryAfterInjectedFailure$' ./internal/storage`; 180s; `internal/storage/sqlite_failure_test.go`, `internal/storage/context_store.go`, `internal/storage/sqlite.go`, `internal/contextstate/contracts.go` |
| 03-GREEN-003 | 6 | GREEN | `internal/storage/context_store.go` | `Load`; depends 03-RED-003; `go test -run '^TestCheckpointRecoveryAfterInjectedFailure$' ./internal/storage`; 180s; `internal/storage/context_store.go`, `internal/storage/sqlite_failure_test.go`, `internal/storage/sqlite.go`, `internal/contextstate/contracts.go` |
| 03-RED-004 | 7 | RED | `internal/storage/context_store_test.go` | `TestAdvanceUpdatesHeadWithCAS`; depends 03-GREEN-003; `go test -run '^TestAdvanceUpdatesHeadWithCAS$' ./internal/storage`; 120s; `internal/storage/context_store.go`, `internal/storage/context_store_test.go`, `internal/contextstate/contracts.go` |
| 03-GREEN-004 | 8 | GREEN | `internal/storage/context_store.go` | `Advance`; depends 03-RED-004; `go test -run '^TestAdvanceUpdatesHeadWithCAS$' ./internal/storage`; 120s; `internal/storage/context_store.go`, `internal/storage/context_store_test.go`, `internal/contextstate/contracts.go` |
| 03-REVIEW-002 | 9 | review | `internal/storage/context_store.go` | CAS/transaction review; depends 03-GREEN-004; `go test -race ./internal/storage`; 180s; `internal/storage/context_store.go`, `internal/storage/context_store_test.go`, `internal/storage/sqlite.go`, `internal/storage/sqlite_failure_test.go` |
| 03-RED-005 | 10 | RED | `internal/storage/context_store_test.go` | `TestRecoverySelectsCommittedPointer`; depends 03-REVIEW-002; `go test -run '^TestRecoverySelectsCommittedPointer$' ./internal/storage`; 120s; `internal/storage/context_store.go`, `internal/storage/context_store_test.go`, `internal/storage/sqlite.go` |
| 03-GREEN-005 | 11 | GREEN | `internal/storage/context_store.go` | `recoverActive`; depends 03-RED-005; `go test -run '^TestRecoverySelectsCommittedPointer$' ./internal/storage`; 120s; `internal/storage/context_store.go`, `internal/storage/context_store_test.go`, `internal/storage/sqlite.go` |
| 03-REVIEW-003 | 12 | review | `internal/storage/context_store.go` | recovery/failure review; depends 03-GREEN-005; `go test -race ./internal/storage`; 180s; `internal/storage/context_store.go`, `internal/storage/context_store_test.go`, `internal/storage/sqlite.go`, `internal/storage/sqlite_failure_test.go` |

Gate: `go test -race ./internal/storage ./internal/ledger`; repeated crash/failure
tests pass; durable revision never moves backward and source events are never
deleted by compaction.
