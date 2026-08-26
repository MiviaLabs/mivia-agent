# Token Usage Ledger

`mivia` durably records every provider-reported token measurement a session
produces — token usage, cache usage, and compaction events — in a SQLite
table, `token_usage_events` (schema v12). This closes the gap left by
`chat_sessions.token_count`, which only ever holds a single re-estimated
whole-session number, overwritten on every save.

## What is recorded

`internal/agent/emit.go`'s `EmitTokenUsage`, `EmitCacheUsage`, and
`EmitCompaction` each publish to the session's `events.Bus` (unchanged,
feeds the TUI and `--json` sidecar) and, when `opts.UsageWriter` is set,
build a `usage.UsageRecord` and call `Record`. `usage.UsageWriter` is a
zero-dependency leaf interface (`internal/usage`) that both
`internal/agent` and `internal/storage` import directly, so neither package
depends on the other's domain type.

A `UsageRecord` carries `Kind` (`token_usage` | `cache_usage` | `compaction`),
`SessionID`/`TurnID`, provider/model, the kind-specific token counts, and
subagent attribution (`AgentTask`/`AgentName`/`AgentDepth`) when the turn
belongs to a subagent.

## Write path

`internal/storage.usageWriter.Record` (constructed by `NewUsageWriter`,
scoped to one workspace) is fire-and-forget: it spawns a goroutine that
calls `RecordUsageEvent` with `context.Background()`, not the caller's
context, and returns `nil` immediately. This is deliberate — `EmitCompaction`
runs while `internal/chat` still holds `contextPublishMu`, a session-wide
lock also taken by `/compact`, session reset, and model switch. A synchronous
write sharing `writeMu` with a contended checkpoint commit would extend that
lock's hold time. `store.Close` waits on the same `WaitGroup` the write
registers into before spawning, so a short-lived process (`mivia compact`, a
single non-interactive turn) cannot exit while a write is still in flight.

`RecordUsageEvent` (`internal/storage/usage_events.go`) is one INSERT in its
own transaction, serialized through the same `writeMu`/`retrySQLiteBusy`
pattern every other durable write in this package uses. A write failure is
logged and dropped — it never fails the turn it describes.

## Schema (migration v12)

One wide table, nullable columns per event kind, added by
`applyContextSchemaV12` (`internal/storage/context_schema_v12.go`):

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

The table's presence is its own migration witness: a plain `CREATE TABLE IF
NOT EXISTS` with no `ALTER` on an existing table and no invariant to repair,
so v12 needs no bespoke `ensureContextSchemaV12` repair branch, unlike
migrations that alter existing tables.

## Calibration feedback loop

`SQLite.CalibrationSeed` (`internal/storage/usage_events.go`) reads back the
last 50 `token_usage` rows for a `(workspace, provider, model)` binding and
returns the aggregate actual-over-estimated ratio, clamped to `[0.2, 3.0]`.
A freshly started process seeds its token-estimate correction from this
ratio instead of starting blind at `1.0` — without it, every new process
assumed the `len(s)/4` estimate was exact, which for code- and
JSON-tool-schema-heavy payloads runs about 1.7x low and can push a session
past its compaction trigger before the estimate corrects.

## Known gaps

- **No query API or CLI surface yet.** `RecordUsageEvent` and
  `CalibrationSeed` are the only reads/writes implemented. A
  `QueryTokenUsage`/`SummarizeTokenUsage` Go API and a `mivia sessions
  usage-history` CLI command do not exist yet — the ledger accumulates data
  today with no way to inspect it other than direct SQLite access.
- **Subagent turns are not recorded.** `internal/subagents` does not wire a
  `UsageWriter` into subagent `agent.Options`, even though the attribution
  fields (`AgentTask`/`AgentName`/`AgentDepth`) already exist on
  `UsageRecord`. Only root-loop turns record today.
- **Plain-chat (`--no-tools`) turns are not recorded.** `EmitTokenUsage`/
  `EmitCacheUsage` only fire from `internal/agent/loop.go`, reached by the
  tool-enabled agent loop. A plain chat session that calls the provider
  directly never emits either event.
- **No retention/pruning.** Rows accumulate with no cap and no delete-on-
  session-delete wiring confirmed in `DeleteSessionSnapshot`.
- **No cost/pricing rollups.** No provider config in this repo carries
  price data, so the ledger reports token counts only, not cost.

## See also

- [Configuration](../product/config.md)
- [Embedded persistence](embedded-persistence.md)
