# Embedded Persistence

SQLite is the default durable backend for orchestration state. See
[configuration](../product/config.md#redaction-and-persisted-orchestration-history)
for the `[subagents] store_backend` setting.

## Storage layout

Mivia uses a CGO-free Go driver (`modernc.org/sqlite`), and splits persistence
into three separate SQLite files rather than one database per data root:

- A per-workspace orchestration store (`DefaultStorePathForWorkspace` in
  `internal/config/defaults.go`), keyed by the current workspace.
- One machine-global context store (`GlobalContextStorePath` in
  `internal/workspace/namespace.go`), shared by every workspace on the
  machine.
- A separate memory store (`internal/memory/store.go`) for project/org
  memory records.

Each store owns its own connection, PRAGMAs, and schema; there is no shared
writer path across the three files. SQLite supports concurrent reads but
only one concurrent writer per file; every store enables WAL mode, foreign
keys, a busy timeout, and explicit transactions, and keeps write
transactions short and batched. WAL improves reader/writer overlap but does
not remove the single-writer-per-file limit, and long-lived readers can
prevent checkpoints and grow the WAL.

`internal/chat/persistence.go` is a separate, file-based JSONL boundary: it
rewrites chunk files and is graceful-exit persistence, not a transactional
store for concurrent session, turn, tool-call, and subagent lifecycle
records.

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
versioned, principal-scoped, sanitized-only, and fail without truncation when
a configured cap would be exceeded. The export byte cap (`max_export_bytes` in
`internal/config`, backing `ExportBytes` in `internal/contextstate/limits.go`)
is operator-set and defaults to `0` (uncapped). Legacy JSONL is an authorized
import/export and rollback compatibility surface; it cannot publish a
checkpoint.

## Data model

The append-oriented, versioned event model spans two schemas:

The orchestration store (`internal/storage/sqlite.go`):

- `events`: append-only orchestration event log.
- `run_claims`: fenced-lease rows (`holder`, fence columns) that guarantee
  only one executor drives a given run at a time; the basis for the
  coordinator's execution claim/lease mechanism (`ClaimRun`,
  `TakeoverExpiredRunClaim`; see
  [Subagent Orchestration](overview.md#subagent-orchestration)).
- `content`: the orchestration-side content store, distinct from the context
  store's `context_payloads`.
- `spool_grants`: principal-scoped visibility grants for the remainder spool
  (see [Remainder storage contract](#remainder-storage-contract) below).

The context store (`internal/storage/context_schema.go`):

- `context_sessions`, `context_payloads`, `context_source_events`,
  `context_checkpoints`: the context-compaction namespace described above.
- `context_audits`, `context_tombstones`: the compliance retention rows
  described above.
- `context_operations`, `context_imports`: idempotency-key tables covering
  duplicate/idempotent context writes.
- `context_payload_chunks`: chunked storage backing large context payloads.
- `chat_sessions`, `chat_session_admissions`, `chat_session_dirs`,
  `worktree_routes`: chat-session and worktree-routing bookkeeping.
- `context_schema_migrations`: the versioned migration ledger
  (`applyContextMigration`) that governs schema evolution for the context
  store.

Typed metadata is persisted relationally. Payloads are persisted only after
redaction and size policy checks. Raw credentials, hidden reasoning,
unbounded tool output, and provider secrets are never persisted.

Context caches and context packs are derived artifacts, keyed by workspace
revision, source event range, tool/config version, pack algorithm, and
model/embedding version. Compaction emits a checkpoint referencing an exact
event range; compaction is not deletion.

## Remainder storage contract

The `internal/remainder` package owns the truncated-result spool and its
principal-scoped visibility grants. When a tool result is shortened, the full
body is stored under a content-addressed ref and the principal that received
the truncation notice is granted read access. The model pages that body via
the host's `read_output` tool.

Content-addressed refs use `sdkadapter.Mint`, which produces
`"ref:<kind>:<64-hex-sha256>"` strings when called with a non-empty kind
(the CLI shape) and `"sha256:<64-hex>"` when called with an empty kind
(the SDK shape). The kind for tool-result bodies is `output`. The same
body always mints the same ref, so re-storing a duplicate lands on the
same key and never grows the store.

`StoreContent` is idempotent: storing the same data twice returns the same
ref and the store does not grow unboundedly for duplicates. `MemoryStore` is
the idempotent in-memory implementation used for tests and host wiring that
does not share the ledger repository.

Visibility is caller-scoped: only the principal that received the grant may
`Load` the ref. The `Spool` grants access by session ID
(`principal.SessionID`). After a process restart, in-memory grants are empty
even though durable grants and bytes survive, so the spool consults the
durable grant store (`SpoolGrantStore`) on the in-memory grant miss path.

Sentinel load failures:

| Error | Meaning |
|-------|---------|
| `ErrNotFound` | The ref is unknown (never spooled, or never stored). |
| `ErrDenied` | Content exists (or was granted to someone else) but the calling principal was not the recipient of the remainder ref. |
| `ErrExpired` | The principal once held a grant but the remainder is no longer available (retention expiry or explicit expiry). Distinct from not-found so the model does not treat a timed-out ref as a corrupt key. |

A nil spool, empty principal, nil store, or store failure yields `""` and the
plain notice: a failed spool must never invent a ref (INV-AG-10 / INV-CE-07-A/C).

## Design rationale

SQLite was chosen over bbolt, Badger, Pebble, DuckDB, and an external
database for the first structured history store: it gives ACID transactions,
inspectable queries, schema evolution via the migration ledger above, and
portability without adding a deployment, auth, or networking dependency.
bbolt remains a viable KV-only fallback if relational query needs shrink;
Badger and Pebble were passed over because their LSM/value-log or
ordered-KV operational tuning is unnecessary for this workload; DuckDB fits
analytics better than operational event history and cache lookup.

No paid SQLite extensions are used or required. SQLite Encryption Extension,
ZIPVFS, and other commercial SQLite extensions are out of scope for the
default design; encryption, where required, is handled by an approved
open-source layer or encrypted artifact strategy, reviewed separately.

SQLite is not the right backend if Mivia later requires sustained high write
contention, multiple processes writing the same file, shared
network-filesystem ownership, or service-scale replication. The storage
interface keeps that future backend change possible.

## See also

- Subagent orchestration and run-claim leasing: `docs/architecture/overview.md#subagent-orchestration`
- Concurrency model: `docs/architecture/concurrency.md`
- Configuration: `docs/product/config.md`
