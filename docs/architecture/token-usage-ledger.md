# Token usage ledger

Mivia records provider-reported token measurements, cache performance, and compaction events in the SQLite `token_usage_events` table (schema v12).

## Recorded events

`EmitTokenUsage`, `EmitCacheUsage`, and `EmitCompaction` in `internal/agent/emit.go` publish events to the session `events.Bus`. When `opts.UsageWriter` is configured, each function creates a `usage.UsageRecord` and invokes `Record`. `usage.UsageWriter` is a leaf interface defined in `internal/usage/usage.go`.

A `UsageRecord` contains:

- `Kind`: Event type (`token_usage`, `cache_usage`, or `compaction`).
- `SessionID` and `TurnID`: Session and turn identifiers.
- Provider and model names.
- Token counts specific to the event type.
- Subagent attribution fields (`AgentTask`, `AgentName`, `AgentDepth`).

## Write path

`internal/storage.usageWriter.Record` (`internal/storage/usage_events.go:67`) processes writes asynchronously in a separate goroutine using `context.Background()`. This prevents token recording from blocking session locks such as `contextPublishMu` during compaction or model switches.

`store.Close` waits on the internal `usageWriteWG` waitgroup (`internal/storage/sqlite.go:35`) so pending writes complete before the database closes.

`RecordUsageEvent` (`internal/storage/usage_events.go:16`) executes a single `INSERT` statement within an isolated transaction. The operation uses the `writeMu` mutex and `retrySQLiteBusy` handler. The writer logs and discards write errors without failing the active turn.

## Schema

`applyContextSchemaV12` (`internal/storage/context_schema_v12.go:12`) defines the `token_usage_events` table:

```sql
CREATE TABLE token_usage_events(
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  workspace_id        TEXT NOT NULL,
  session_id          TEXT NOT NULL,
  turn_id             TEXT NOT NULL,
  kind                TEXT NOT NULL CHECK(kind IN ('token_usage','cache_usage','compaction')),
  provider            TEXT,
  model               TEXT,
  input_tokens        INTEGER,
  output_tokens       INTEGER,
  estimated_tokens    INTEGER,
  calibration_ratio   REAL,
  cached_input_tokens INTEGER,
  cache_write_tokens  INTEGER,
  before_tokens       INTEGER,
  after_tokens        INTEGER,
  elided_messages     INTEGER,
  elided_bytes        INTEGER,
  summarized          INTEGER,
  reason              TEXT,
  agent_task          TEXT,
  agent_name          TEXT,
  agent_depth         INTEGER,
  created_at          INTEGER NOT NULL
);
CREATE INDEX idx_token_usage_events_session ON token_usage_events(workspace_id, session_id, created_at);
CREATE INDEX idx_token_usage_events_turn    ON token_usage_events(workspace_id, session_id, turn_id);
```

## Calibration feedback loop

`SQLite.CalibrationSeed` (`internal/storage/usage_events.go:107`) reads the most recent 50 `token_usage` records for a `(workspace_id, provider, model)` tuple. It computes the aggregate actual-to-estimated token ratio clamped to `[0.2, 3.0]`.

A newly started process uses this ratio to initialize its token estimation multiplier instead of defaulting to `1.0`. This correction avoids underestimating tokens on payloads containing dense code or tool schemas.

## Known limitations

- **Direct database access required**: The codebase provides `RecordUsageEvent` and `CalibrationSeed` without a dedicated query API or CLI inspection command.
- **Root agent turns only**: `internal/subagents` does not configure a `UsageWriter` on subagent options. Only turns from the primary agent loop record usage.
- **Tool-enabled loop requirement**: `EmitTokenUsage` and `EmitCacheUsage` execute inside the tool-enabled agent loop (`internal/agent/loop.go:188`). Direct provider calls in plain chat mode do not emit usage records.
- **No pruning or automatic deletion**: `DeleteSessionSnapshot` (`internal/storage/chat_sessions_delete.go:22`) does not delete associated rows from `token_usage_events`.
- **Token counts only**: The ledger records token quantities. It does not calculate financial costs.

## See also

- [Embedded persistence](embedded-persistence.md)
- [Configuration](../product/config.md)
