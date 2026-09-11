# Embedded persistence

SQLite is the durable backend for orchestration state, chat sessions, and context compaction.

## Storage layout

Mivia uses a CGO-free Go driver (`modernc.org/sqlite`) and stores data across three SQLite database files:

- **Per-workspace orchestration store**: Stored at the path from `DefaultStorePathForWorkspace` (`internal/config/defaults.go:374`), scoped to the workspace directory.
- **Global context store**: Stored at the path from `GlobalContextStorePath` (`internal/workspace/namespace.go:98`), shared across workspaces on the machine.
- **Memory store**: Stored at `internal/memory/store.go` for project and organization memory records.

Each store manages its own connection pool, schema, and configuration. Every store sets the following PRAGMA settings on initialization (`internal/storage/sqlite.go:68`):

- `PRAGMA journal_mode=WAL`
- `PRAGMA synchronous=NORMAL`
- `PRAGMA foreign_keys=ON`
- `PRAGMA busy_timeout=5000`

Write operations execute inside explicit transactions serialized by internal mutexes.

## Context compaction and retention

Context compaction uses the `mivia.context.payload.v1` namespace.

`context_source_events` records structural metadata and points to sanitized payload rows in `context_payloads`. Context reads verify a session, subject, and capability tuple before returning data. Unconfigured redaction policies record hash and size metadata only.

Context payloads support explicit retention controls:

- **Session deletion**: Writes a tombstone, increments the session revision, revokes payload rows, and inserts an audit record.
- **Audit and tombstone retention**: Compliance records in `context_audits` and `context_tombstones` persist across payload garbage collection cycles.
- **Context export**: Exports enforce principal scoping and size limits (`max_export_bytes` in `internal/config`, `ExportBytes` in `internal/contextstate/limits.go`). Exports fail without truncation when output exceeds the configured limit.

## Data model

Persistence spans two database schemas:

### Orchestration store

Defined in `internal/storage/sqlite.go:121`:

- `events`: Append-only event log for workflow runs and agent execution.
- `run_claims`: Fenced lease records (`holder`, `fence`) ensuring single-executor ownership of runs (`ClaimRun`, `TakeoverExpiredRunClaim`).
- `fenced_tokens`: Fencing tokens registered for active run leases.
- `content`: Content-addressed binary storage for orchestration artifacts.
- `spool_grants`: Principal-scoped authorization records for the remainder spool.
- `automation_runs`: Execution history for scheduled and triggered automations (`internal/storage/automation_schema.go:20`).

### Context store

Defined in `internal/storage/context_schema.go:197` and versioned migration files:

- `context_sessions`, `context_payloads`, `context_source_events`, `context_checkpoints`: Core context compaction records.
- `context_audits`, `context_tombstones`: Compliance audit trails and deletion markers.
- `context_operations`, `context_imports`: Idempotency keys for deduplicated writes.
- `context_payload_chunks`: Chunked storage for large context payloads.
- `chat_sessions`, `chat_session_admissions`, `chat_session_dirs`, `worktree_routes`, `worktree_instances`, `worktree_catalog_keys`: Chat session state, directory mappings, and worktree routing.
- `token_usage_events`: Detailed token usage, cache performance, and compaction measurements (schema v12, `internal/storage/context_schema_v12.go:12`).
- `memory_sources`, `memory_entries`: Project memory index and source records (schema v16, `internal/storage/context_schema_v16.go:11`).
- `context_schema_migrations`: Versioned migration ledger (`applyContextMigration`).

## Remainder storage contract

The `internal/remainder` package manages truncated tool outputs and visibility grants:

- **Content-addressing**: Truncated bodies are stored using keys minted by `sdkadapter.Mint` (`"ref:output:<sha256>"`). Identical outputs produce matching references and prevent duplicate writes.
- **Idempotency**: `StoreContent` operations are idempotent.
- **Principal access control**: `Spool` restricts `Load` operations to the principal associated with the session (`principal.SessionID`). On in-memory grant cache misses, the spool checks durable grants in `SpoolGrantStore` (`internal/remainder/spool.go:142`).

Sentinel load errors defined in `internal/remainder/spool.go:27`:

| Error | Meaning |
|-------|---------|
| `ErrNotFound` | The reference was never stored or spooled. |
| `ErrDenied` | The content exists, but the calling principal does not hold an access grant. |
| `ErrExpired` | The principal held a grant, but the content expired or was explicitly revoked. |

## See also

- [Subagent orchestration](overview.md#subagent-orchestration)
- [Concurrency model](concurrency.md)
- [Configuration](../product/config.md)
- [Token usage ledger](token-usage-ledger.md)
