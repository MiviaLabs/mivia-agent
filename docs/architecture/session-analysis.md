# Session analysis

The `session-analysis` skill (`.agents/skills/session-analysis/`) provides read-only, metadata-only analysis of chat sessions in the durable SQLite chat ledger. It evaluates process quality and session metrics without inspecting message contents.

## Ledger resolution and principal scoping

The analysis tool resolves the database path through two steps (`.agents/skills/session-analysis/queries.py:72`):

1. **Workspace store**: Checks `[subagents].store_path` in `.mivia/mivia.toml` (expands `~` and resolves relative paths to the workspace root).
2. **Global context store**: Defaults to `~/.mivia/context.db` (`internal/workspace/namespace.go:98`), shared by workspaces on the machine.

Every query enforces principal scoping to isolate workspace data:

- `workspace_id`: `"workspace-"` prefix followed by the 16-character hex encoding of the first 8 bytes of the workspace root's SHA-256 digest (`internal/clichat/context_setup_session.go:93`).
- `subject_id`: Fixed to `"local-user"`.

Ledger execution stops with a `NOT_RUN` status if Python is older than version 3.11, the ledger file is missing, opening the database fails, or `user_version` is below 11.

## Query parity with the harness

`queries.py` mirrors the harness catalog query in `ListSessions` (`internal/storage/chat_sessions.go:285`). It executes a three-arm union across session types:

| Arm | Source | Identity | Window anchor |
|---|---|---|---|
| 1. Snapshots | `chat_sessions` ⋈ `context_sessions` ⋈ `chat_session_dirs` | Snapshot name | `chat_sessions.updated_at` |
| 2. Live sessions | `context_sessions` ⋈ `context_checkpoints` (`complete=1`), deduped against snapshots | `session_id` | `MAX(context_checkpoints.created_at)` |
| 3. Worktree routes | `worktree_routes` filtered by active instances in `worktree_instances` | `worktree:<name>` | `worktree_routes.updated_at` |

Query predicates maintain exact catalog behavior:

- **Arm 2 window anchor**: Uses `MAX(context_checkpoints.created_at)` because `context_sessions` does not contain an `updated_at` column.
- **Checkpoints**: `context_checkpoints` joins omit `instance_id` because the column does not exist on that table.
- **Session copies**: Distinguishes copies from live sessions using `session_id IS NULL OR NOT EXISTS (...)` (`internal/storage/chat_sessions.go:170`).
- **Orphan directories**: Left joins `chat_session_dirs` against both `chat_sessions` by name and `context_sessions` by session ID (`internal/storage/chat_sessions.go:333`).

## Privacy perimeter

The skill enforces strict data isolation boundaries:

- **Message values**: The query never selects the `messages` column value from `chat_sessions`. It selects only `LENGTH(messages)` as a size proxy.
- **Excluded columns**: The tool never reads `context_payloads.data`, `context_payload_chunks.data`, `context_source_events.payload_ref`, `context_checkpoints.summary_metadata`, `context_checkpoints.active_context`, `chat_session_admissions.agent / digest / names`, or `context_sessions.title`.
- **Read-only access**: Opens connections using URI `mode=ro` and executes `PRAGMA query_only`.
- **Tool boundaries**: Does not invoke the `mivia` executable or access legacy file-based session stores in `.mivia/sessions/`.

## Analysis metrics

- **Stalled live sessions**: Identifies live sessions with zero completed checkpoints (`session_type='live' AND checkpoint_count=0`).
- **Staleness markers**: Treats `token_count` and `turn_count` from `chat_sessions` as save-time estimates invalidated by compaction (`internal/clichat/sessions_command.go:321`). It treats `payload_bytes` as current post-compaction size.
- **Timestamp span**: Measures elapsed time between first and last saves (`updated_at - created_at`), rather than active interaction duration.
- **Outlier calculation**: Applies Tukey Interquartile Range (IQR) filtering when sample size $n \ge 5$, and adds standard score thresholds ($z > 2$, $z > 3$) when $n \ge 10$.
- **Measured absence**: Reports an empty session window as a valid finding with store calibration metrics rather than an error.

## Validation

The skill validates analysis accuracy through multiple consistency checks:

- **Metric cross-checks**: Verifies aggregate counts against sums across union arms (`COUNT` vs `SUM`).
- **Hermetic self-test**: `queries.py --selftest` constructs an in-memory SQLite database matching the schema DDL, populates test rows, runs queries, and asserts expected results. The test confirms that message payloads and session titles never appear in output records.

## See also

- [Embedded persistence](embedded-persistence.md)
- [Token usage ledger](token-usage-ledger.md)
- [Configuration](../product/config.md)
