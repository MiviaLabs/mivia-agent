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

- `internal/storage/checkpoints.go` and `_test.go`: checkpoint structs, CAS
  append/publish/read APIs, uniqueness, and bounded serialization.
- `internal/storage/transactions.go` and `_test.go`: transaction ordering,
  rollback, disk-full/error injection, and recovery.
- `internal/ledger/` adapters and tests: exact range reads and projection rebuild.

No in-place JSONL chunk deletion or rename loop is valid for checkpoint
correctness. Incomplete revisions remain inspectable and recovery selects the
newest complete committed revision.

ADLC micro-tasks:

| Wave | Type | File | Task / verification |
|---|---|---|---|
| 1 | RED | `internal/storage/checkpoints_test.go` | CAS, stale-write, duplicate, and same-key conflict assertions. |
| 2 | GREEN | `internal/storage/checkpoints.go` | Implement checkpoint record and atomic CAS publication. |
| 2 | RED | `internal/storage/transactions_test.go` | Failure injection at every commit boundary and recovery assertions. |
| 3 | GREEN | `internal/storage/transactions.go` | Implement rollback/recovery protocol. |
| 4 | review | storage/ledger files | Review source-range exactness, retention, permissions, and migration compatibility. |

Gate: `go test -race ./internal/storage ./internal/ledger`; repeated crash/failure
tests pass; durable revision never moves backward and source events are never
deleted by compaction.
