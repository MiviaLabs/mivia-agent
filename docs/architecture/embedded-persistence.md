# Embedded persistence recommendation

Status: Implemented

SQLite persistence shipped as the default durable backend for orchestration state. See [configuration](../product/config.md#redaction-and-persisted-orchestration-history) for the `[subagents] store_backend` setting.

## Recommendation

Use SQLite as the first embedded persistence layer behind a small Mivia-owned storage interface. Use a CGO-free Go driver initially (`modernc.org/sqlite`) after a focused compatibility and workload benchmark. Use one database file per Mivia data root. This is a conditional recommendation: it is suitable for 100–200 logical agents only when writes are short, batched, and serialized through a bounded writer path.

Enable WAL mode, foreign keys, a busy timeout, explicit transactions, and a bounded writer path. SQLite supports concurrent reads but only one concurrent writer; WAL improves reader/writer overlap but does not remove the single-writer limit. Keep write transactions short and batch event appends. Do not claim 100–200 concurrent-writer capacity until the workload benchmark passes. Long-lived readers can prevent checkpoints and grow the WAL.

Do not make JSONL snapshots the long-term source of truth. The current boundary (`internal/chat/persistence.go`) rewrites chunk files and is primarily graceful-exit persistence. It is not a transactional store for concurrent session, turn, tool-call, and subagent lifecycle records.

## Context source namespace

Context compaction uses the SQLite-owned `mivia.context.payload.v1` namespace.
`context_source_events` stores bounded structural metadata and references
sanitized payload rows in `context_payloads`; it never dereferences the legacy
orchestration `content` table. A session/subject/capability tuple is checked
before every context read. The unconfigured context redaction policy stores
hash and size metadata only, so source content remains ephemeral until a host
classifier is explicitly configured.

Context payloads have an explicit retention class. Session deletion writes a
tombstone, advances the session revision, revokes all context payload rows,
and records one bounded audit row. Tombstones and audits use the compliance
retention class and are not removed by payload garbage collection. Exports are
versioned, principal-scoped, sanitized-only, capped at 8 MiB, and fail without
truncation when the cap would be exceeded. Legacy JSONL is an authorized
import/export and rollback compatibility surface; it cannot publish a
checkpoint.

## Data model

Start with an append-oriented, versioned event model:

- `sessions`: workspace scope, timestamps, model/provider metadata, lifecycle and retention state.
- `runs`: one user/agent execution, parent run/subagent relationship, status, cancellation/error metadata.
- `events`: ordered immutable user, assistant, tool-call, tool-result, and subagent lifecycle events; unique run sequence and idempotency key.
- `artifacts`: large outputs and context material referenced by events; content hash, size, type, redaction status, retention class.
- `cache_entries`: namespace/key, value reference, policy/version, expiry, and provenance. Cache misses must never change correctness.

Persist typed metadata relationally. Persist payloads only after redaction and size policy checks. Never persist raw credentials, hidden reasoning, unbounded tool output, or provider secrets. Add explicit retention, deletion, export, and permission behavior before enabling full-history persistence.

Context caches and context packs must be derived artifacts, keyed by workspace revision, source event range, tool/config version, pack algorithm, and model/embedding version. Compaction must emit a checkpoint referencing an exact event range; compaction is not deletion.

## Alternatives

| Store | Decision | Reason |
|---|---|---|
| SQLite | Recommended | Best balance of ACID transactions, inspectable queries, schema evolution, portability, and maintenance. |
| bbolt | Keep as KV-only alternative | Very small and strong, but relationships, indexes, retention, and migrations become application-managed buckets. |
| Badger | Defer | Strong pure-Go concurrent KV, but LSM/value-log compaction and operational tuning are unnecessary for the first structured history store. |
| Pebble | Defer | Excellent ordered KV engine, but no general SQL-style transactions; application transaction semantics would be rebuilt. |
| DuckDB | Defer | Better for analytics than operational event history and cache lookup. |
| External database | Defer | Adds deployment, auth, networking, and offline failure modes before the local contract is stable. |

## License and package review

The candidates below are permissively usable for an open-source Mivia binary, subject to normal dependency notices and a final legal review:

| Package | License evidence | Fit |
|---|---|---|
| SQLite core | Public domain | Best engine fit; no paid license required. |
| `modernc.org/sqlite` | BSD-3-Clause | Recommended Go driver when CGO-free builds matter; audit its transitive dependencies. |
| `mattn/go-sqlite3` | MIT; requires CGO | Viable if the build pipeline accepts CGO and compiler/toolchain requirements. |
| `go.etcd.io/bbolt` | MIT | Good small KV alternative; one writer and application-managed indexes/relationships. |
| `github.com/dgraph-io/badger/v4` | Apache-2.0 | Strong pure-Go high-write KV fallback; adds LSM/value-log/GC operations and no relational query model. |
| `github.com/cockroachdb/pebble` | Apache-2.0 | Strong storage engine/cache candidate; not the first system of record because general transactions are absent. |

No paid SQLite extensions are required. Do not use SQLite Encryption Extension, ZIPVFS, or other commercial SQLite extensions for the default design. Encryption, if required, must be handled by an approved open-source layer or encrypted artifact strategy and separately reviewed.

SQLite is not the right default if Mivia later requires sustained high write contention, multiple processes writing the same file, shared network-filesystem ownership, or service-scale replication. The storage interface must keep that future backend change possible.

## Remainder storage contract

The `internal/remainder` package owns the truncated-result spool and its principal-scoped visibility grants. When a tool result is shortened, the full body is stored under a content-addressed ref and the principal that received the truncation notice is granted read access. The model pages that body via the host's `read_output` tool.

Content-addressed refs use `contentref.Reference`, which produces `"ref:<kind>:<64-hex-sha256>"` strings. The kind for tool-result bodies is `output`. The same body always mints the same ref, so re-storing a duplicate lands on the same key and never grows the store.

`StoreContent` must be idempotent: storing the same data twice returns the same ref and the store must not grow unboundedly for duplicates. `MemoryStore` is the idempotent in-memory implementation used for tests and host wiring that does not share the ledger repository.

Visibility is caller-scoped: only the principal that received the grant may `Load` the ref. The `Spool` grants access by session ID (`principal.SessionID`). After a process restart, in-memory grants are empty even though durable grants and bytes survive, so the spool consults the durable grant store (`SpoolGrantStore`) on the in-memory grant miss path.

Sentinel load failures:

| Error | Meaning |
|-------|---------|
| `ErrNotFound` | The ref is unknown (never spooled, or never stored). |
| `ErrDenied` | Content exists (or was granted to someone else) but the calling principal was not the recipient of the remainder ref. |
| `ErrExpired` | The principal once held a grant but the remainder is no longer available (retention expiry or explicit expiry). Distinct from not-found so the model does not treat a timed-out ref as a corrupt key. |

A nil spool, empty principal, nil store, or store failure yields `""` and the plain notice: a failed spool must never invent a ref (INV-AG-10 / INV-CE-07-A/C).

## Required validation before implementation

1. Benchmark representative event sizes and concurrent readers/writers; record throughput, p95 latency, database growth, and recovery time.
2. Test transaction rollback, duplicate/idempotent append, ordering, WAL checkpointing, disk-full behavior, permissions, backup/restore, and process interruption.
3. Test privacy controls: secret/PII fixtures, redaction, size limits, deletion verification, retention pruning, and export boundaries.
4. Test overlapping turns, cancellation, late completion, subagent parentage, and rebuildable projections with `go test -race`.
5. Keep the current JSONL format only as an explicit import/export or rollback utility if product needs it; do not make a migration a prerequisite for this new store.
6. Require a SQLite build at or after the 2026 WAL-reset fix (SQLite 3.51.3 or later, or a driver bundling that fix).

## Sources checked 2026-07-27

- [SQLite transactions](https://www.sqlite.org/lang_transaction.html)
- [SQLite WAL](https://www.sqlite.org/wal.html)
- [SQLite documentation](https://www.sqlite.org/docs.html)
- [bbolt](https://github.com/etcd-io/bbolt)
- [Badger](https://github.com/dgraph-io/badger)
- [Pebble](https://github.com/cockroachdb/pebble)
- [DuckDB concurrency](https://duckdb.org/docs/stable/connect/concurrency)
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite)
- [SQLite public-domain notice](https://www.sqlite.org/copyright.html)
- [mattn/go-sqlite3](https://github.com/mattn/go-sqlite3)

This is a source-informed recommendation, not a benchmark result. The repository has not yet measured the proposed workload.
