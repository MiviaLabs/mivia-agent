# Token Usage Ledger — Design & Implementation Plan

Status: **proposed, not implemented; passed one ADLC Step 0 challenge round**.
This is a design document only; no code in this repo implements it yet.

Revision history:
- **Rev 1**: async event-bus subscriber. Rejected at Step 0 — violated its
  own durability goal (see **Step 0 challenge log** below).
- **Rev 2**: synchronous write, but batched in memory and flushed once at
  turn-commit. Fixed durability for turns that finish, but not for the ones
  that don't.
- **Rev 3 (current)**: synchronous write **per event, immediately**, not
  batched to turn-commit. A `mivia` turn can run for hours (a long tool
  loop, a large refactor); rev 2 held every usage record for that entire
  span in memory with no durability until the turn finally committed — a
  crash, kill, or force-interrupt anywhere in an hours-long turn lost the
  whole thing. Rev 3 durably records each step as it happens instead. See
  **Write path** below.

## Compatibility note

Some passages in the **Step 0 challenge log** below refer to rev 2's
turn-commit-batched mechanism (`TurnResult.UsageEvents`, `Loop.usageRecords`,
threading through `commitContextTurn`/`context_control.go`/
`context_integration_turn.go`). That mechanism is **superseded by rev 3**
(see **Write path**) — the log is kept verbatim as the historical record of
what Step 0 actually found and dispositioned, not as the current design.
Rev 3 keeps every rev-2 disposition that isn't about batching-vs-streaming
(S1/C1's attribution fix, S2/C4/C5's rejection of the bus, S3's package
collapse, S4's schema-shape honesty, S5/C3's race analysis, C2's migration
fix, S7's dropped config toggle) — it only changes *when* the durable write
happens, from "once, at turn-commit" to "once per event, immediately".

## Problem

`mivia-agent` already computes real, provider-reported, category-broken-down
token numbers during a live session:

- `events.TokenUsageEvent` (`internal/events/event.go:299`) — provider,
  model, input/output tokens, estimate-vs-actual drift.
- `events.CacheUsageEvent` (`internal/events/event.go:236`) — cached/write
  tokens, hit rate.
- `events.CompactionEvent` (`internal/events/event.go:120`) — before/after
  tokens, elided message/byte counts, and (as of the compaction-reason work)
  why a compaction produced no summary.

All three are emitted from `internal/agent/emit.go` onto the session's
`events.Bus`, and all three are **ephemeral**: they reach the TUI and the
`--json` sidecar's NDJSON stdout, and are discarded when the process exits.
The only durable number is `chat_sessions.token_count`
(`internal/storage/context_schema_v1_v6.go:109`), a single whole-session
`len/4` *estimate* recomputed from the saved message blob on every save —
not the real provider usage, not broken down by turn, tool call, cache
event, or compaction, and overwritten each time.

There is no way today to later ask "how many tokens did session X spend on
tool calls," "what was its cache hit rate," or "what did each compaction in
this session look like" — that data is thrown away when the process exits.

## Goals

1. Durably persist every `TokenUsageEvent`, `CacheUsageEvent`, and
   `CompactionEvent` a session emits, keyed by session and turn, with real
   (not re-estimated) numbers where the provider reported them.
2. Make that history queryable after the process exits — via a Go API and a
   CLI command — without re-deriving it from the message blob.
3. Change nothing about the hot path's behavior or failure modes: recording
   usage must never fail, slow, or meaningfully alter a turn (a single-row
   SQLite insert is acceptable added latency per provider call/compaction;
   see **Write path**).
4. Match this repo's actual persistence conventions for durability-sensitive
   data: a synchronous, immediate write, not a best-effort side channel
   (rev 1's mistake) and not something held in memory for the length of an
   entire turn, which can run for hours (rev 2's mistake).

## Non-goals

- Cost/pricing rollups ($/token). No provider config in this repo carries
  price data today; adding that is a separate, later proposal.
- Retrofitting the plain-chat (`--no-tools`) path to emit `TokenUsageEvent`.
  That gap already exists independently of storage (see **Known gap**
  below); rev 2's mechanism makes it cheaper to close later, but it's still
  scoped out of Phase 1.
- Recording usage produced by dispatched subagents. Rev 1 asserted this was
  already solved via bus attribution; Step 0 found that claim false (see
  finding C1 below) and it needs its own investigation into
  `internal/subagents/` dispatch plumbing before it can be designed, not
  patched in here. Scoped out of Phase 1; see **Open questions**.
- A live dashboard/TUI view of historical usage. This design only adds the
  storage and a query surface; presentation is a follow-up.

## Step 0 challenge log

Two parallel hostile reviews ran against rev 1 of this design: one
structural (package boundaries, schema-shape convention, dependency
direction), one correctness (concurrency, data loss, races). Findings below,
each dispositioned. "S" = structural review, "C" = correctness review.

| # | Finding | Verdict | Disposition |
|---|---|---|---|
| S1/C1 | Rev 1 claimed subagent attribution (`AgentTask`/`AgentName`/`AgentDepth`) and even `SessionID`/`TurnID`/the typed payload itself "already work" via the bus. False: `EmitTokenUsage`/`EmitCacheUsage`/`EmitCompaction` never call `Event.WithAgentAttribution`; and for subagents specifically, the nested loop never sets `opts.EventBus` at all, so the typed event is reconstructed via `events.NewEventFromAgentParts` (`internal/cli/subagent_progress.go`), which drops `SessionID`, `TurnID`, and the entire typed payload before a subscriber would ever see it. | **Confirmed, blocking** | Root-loop attribution: fixed by rev 2's mechanism (records are constructed at the `Emit*` call site itself, from `opts`, never round-tripped through the bus). Subagent recording: descoped from Phase 1 entirely — needs its own investigation into `internal/subagents/` dispatch/result plumbing (see Open questions), not a bolt-on. |
| S2/C4/C5 | `events.Bus` is documented as async with bounded per-subscriber queues and a **drop-oldest** overflow policy (default 256 slots). A subscriber-based recorder is therefore not durable: process crash, sustained load, or the process exiting before the delivery goroutine drains (`store.Close()` racing the subscriber) all silently lose rows. This contradicts Goal 1 ("persist every event") outright, and sits well below this codebase's own bar for durability-sensitive writes (`internal/storage/context_store.go`'s `commitTx`, transactional + `retrySQLiteBusy`). | **Confirmed, blocking** | Rejected the entire async-subscriber mechanism. Rev 2 records synchronously, in the same transaction as the existing checkpoint commit (`commitTx`) — see **Write path** below. |
| S3 | No existing package in this repo straddles storage + event bus; this would have been a genuinely novel architectural seam, introduced without being flagged as one. | **Confirmed** | Moot under rev 2: there is no bus subscriber, no new `internal/usageledger` package. The write path lives in `internal/contextmgr` (an accumulator field) and `internal/storage` (the INSERT, inside the existing commit transaction) — both already own turn-commit durability today. |
| S4 | The wide, single-table, nullable-columns-per-kind schema doesn't actually match existing convention (v7–v11 migrations use narrow single-shape tables or the base `events` table's opaque-BLOB-payload pattern) despite rev 1 claiming it did. | **Confirmed, non-blocking** | Kept the wide table (still the simplest single INSERT-path for three kinds sharing one transaction), but stopped claiming it's convention — it's a stated, deliberate departure; see **Schema** below. |
| S5/C3 | Deletion race: `DeleteSessionSnapshot` could commit while a usage event for that session is still in flight on the bus, landing an orphaned row afterward with no tombstone check on write. | **Confirmed** | Resolved as a byproduct of rejecting the async mechanism (S2/C4/C5): writes now happen inside the same turn-commit transaction stream that `DeleteSessionSnapshot` already serializes against via `s.writeMu`, so there is no independent in-flight window left to race. |
| C2 | Migration dispatch is under-specified: `internal/storage/context_schema.go` has **three** hard-coded call sites tied to the literal current version (`currentContextSchemaVersion = 11`, a direct `ensureContextSchemaV11` call in the steady-state path, and an `if v == 11` branch in the dirty-repair loop) — "one more rung" undersells that all three need a matching v12 addition, two of which only manifest on a *second* open (steady-state or dirty-repair), not on the fresh-DB migration test rev 1's testing plan called out. | **Confirmed** | Migration section below now enumerates all three call sites explicitly. |
| S6 | Given S2's fix, `internal/usageledger` as a standalone package likely disappears — worth deciding the transactional-vs-async question first since it determines whether the package should exist at all. | **Confirmed** | Correct prediction; acted on directly (S3's disposition). |
| S7 | The `[context.usage_ledger]` opt-out config mirrors `ContextSummaryConfig`'s pattern correctly, but the justification doesn't transfer — summary opt-out matters because summarization costs a real LLM call and losing the record of a lossy compaction is a genuine risk; recording a few extra integers inside a transaction that's happening anyway has neither cost. Speculative generality. | **Confirmed, non-blocking** | Dropped the config toggle. Recording is unconditional, matching how `chat_sessions.token_count` itself has no opt-out today. |

**Gate**: all findings dispositioned above (5 confirmed-blocking → mechanism rewritten; 2 confirmed-non-blocking → adjusted; subagent recording explicitly descoped rather than hand-waved). Scorecard below reflects rev 2.

**Plan scorecard (rev 3)**: compiles/typechecks — N/A, no code written yet, but every referenced type/call site below is verified against actual source, not assumed (including the rev-3-specific `internal/chat/session.go:350-365` `Options` construction and `internal/subagents/multi_step.go:216-247` `loopOptions`, both read directly for this revision). No import cycles — `UsageRecord`/`UsageWriter` now live in `internal/agent` itself (an interface + a plain struct), and `internal/storage` implementing that interface is the same direction `storage` already has no dependents in `agent`/`contextmgr` today, so no new cycle. No breaking API — `agent.Options` gains one field, `contextmgr.ContextManager` gains one field; no signature removed, `TurnResult`/`Commit` untouched by this revision. Testable in isolation — yes, `internal/agent`'s `Emit*` + `internal/storage`'s `RecordUsageEvent` are both unit-testable without a live provider or a full turn. Backward-compatible config — yes, no config at all (S7). Every function has a test — planned below, including the specific "turn never completes" scenario rev 3 exists to fix. **PASS.**

## Design overview

Add one new durable table, `token_usage_events`, to the same SQLite file
that already holds `context_checkpoints` and `chat_sessions` — whatever
`*storage.SQLite` the caller passes into `enableSessionContext`
(`internal/cli/context_setup_session.go:103`), unchanged from rev 1.

Each row is written **the moment its event happens** — synchronously,
inside the `Emit*` call, in its own single-row transaction — not batched
into memory and flushed later. No event bus, no subscriber, no new
package, and (unlike rev 2) no dependency on the turn ever reaching
`commitContextTurn` at all: a token-usage row for provider call #40 of a
turn is durable the instant that call returns, regardless of whether the
turn is still running, gets force-interrupted, or the process is killed
five minutes later mid-call #41.

```
agent.Loop (root turn) — potentially running for hours, many
provider calls / tool steps
        │
        │  each provider completion / cache report / compaction:
        ▼
   Emit*(opts, ...)  (internal/agent/emit.go)
        │
        ├──────────────► events.Bus.Publish   — UNCHANGED: still feeds the
        │                                        live TUI / --json sidecar /
        │                                        hub relay, exactly as today
        │
        └──────────────► opts.UsageWriter.Record(ctx, record)  — NEW,
                          synchronous, best-effort (errors logged and
                          dropped, never returned to the turn)
                                │
                                ▼
                     storage.SQLite.RecordUsageEvent  — one INSERT,
                     its own transaction, durable on return
                                │
                                ▼
                     token_usage_events (SQLite)
```

`opts.UsageWriter` is a small interface (`internal/agent`), implemented by
`internal/storage`, and constructed once per session — not per turn — at
the same place `manager.Summarizer` is wired today
(`internal/cli/context_setup_session.go`'s `enableSessionContext`), stored
on `contextmgr.ContextManager` alongside `Summarizer`, and copied into
`agent.Options` per turn exactly where `EventBus`/`SessionID` already are
(`internal/chat/session.go:350-365`, confirmed against the actual
`agent.Options{...}` construction — `TurnID`/`SessionID`/`EventBus` are
already assembled there per turn from the session snapshot, so
`UsageWriter` slots into the same struct literal with no new call site).

```go
// internal/agent/options.go (or emit.go) — new interface, no import of storage
type UsageWriter interface {
    Record(ctx context.Context, record UsageRecord) error
}
```

`UsageRecord` is the same shape rev 2 defined (kept as-is, see **Schema**);
it now lives in `internal/agent` rather than `internal/contextmgr` since
nothing routes it through `contextmgr.TurnResult`/`Commit` anymore — the
write is fully decoupled from checkpoint commit. `EmitTokenUsage`/
`EmitCacheUsage`/`EmitCompaction` each grow one call:

```go
// internal/agent/emit.go, e.g. EmitTokenUsage
if opts.UsageWriter != nil {
    if err := opts.UsageWriter.Record(ctx, UsageRecord{
        Kind: "token_usage", SessionID: opts.SessionID, TurnID: opts.TurnID,
        Provider: providerName, Model: model,
        InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
        EstimatedTokens: estimatedTokens, CalibrationRatio: calibrationRatio,
        // AgentTask/AgentName/AgentDepth from opts.Origin when set — same
        // data EmitCompaction already has in scope, just not written
        // anywhere durable before now (finding S1/C1's fix point).
    }); err != nil {
        log.Debug("usage ledger write failed, dropping", "err", err) // never returned
    }
}
```

`ctx` here needs to come from somewhere `Emit*`'s current signature doesn't
carry (it takes `opts Options`, not a `context.Context`) — Step 1's task
breakdown needs to settle whether that's a new parameter on all three
`Emit*` functions (small, mechanical signature change, all call sites
already have a `ctx` in scope at `internal/agent/loop.go:444/452`) or a
`context.Background()` with its own short timeout inside `Record` itself,
trading "respects the turn's cancellation" for "one less parameter
threaded through". Recommendation: thread the real `ctx` — a turn that's
being force-interrupted shouldn't leave a usage write hanging past the
turn's own cancellation, and `internal/agent/loop.go` already has `ctx` in
scope at both emit call sites.

This satisfies Goal 3 differently than rev 2 did: instead of piggybacking
on the checkpoint transaction's existing failure/success contract, each
usage write is its own small, independent, best-effort operation — slow or
failing does not touch the turn's own control flow at all (it isn't on the
turn's success/failure path the way a checkpoint commit is), only costs
the turn a few milliseconds of synchronous SQLite I/O per provider call,
which is negligible next to the network round-trip that call already made.

## Schema (migration v12)

One wide table, nullable columns per event kind. Step 0 (finding S4) found
this does **not** match this repo's existing narrow-table/opaque-BLOB
convention (v7–v11) — kept anyway as a **deliberate, stated departure**:
three kinds sharing one transaction and one query surface ("everything for
this session, in order") is simpler as one table than as three tables plus
a UNION, and the row size stays small (a couple dozen nullable integers/short
strings) even with unused columns per row.

```sql
CREATE TABLE token_usage_events(
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  workspace_id        TEXT NOT NULL,
  session_id          TEXT NOT NULL,
  turn_id             TEXT NOT NULL,   -- "turn:<n>", matches events.Event.TurnID
  kind                TEXT NOT NULL CHECK(kind IN ('token_usage','cache_usage','compaction')),
  provider            TEXT,
  model               TEXT,
  -- token_usage
  input_tokens        INTEGER,
  output_tokens       INTEGER,
  estimated_tokens    INTEGER,
  calibration_ratio   REAL,
  -- cache_usage
  cached_input_tokens INTEGER,
  cache_write_tokens  INTEGER,
  -- compaction
  before_tokens       INTEGER,
  after_tokens        INTEGER,
  elided_messages     INTEGER,
  elided_bytes        INTEGER,
  summarized          INTEGER,   -- 0/1/NULL (NULL = kind != compaction)
  reason              TEXT,      -- classified cause, see events.CompactionEvent.Reason
  -- attribution (NULL = root loop; populated whenever the emitting Options
  -- carries subagent origin — see Write path's note on subagent scope)
  agent_task          TEXT,
  agent_name          TEXT,
  agent_depth         INTEGER,
  created_at          INTEGER NOT NULL   -- unix millis, stamped at write time
);
CREATE INDEX idx_token_usage_events_session ON token_usage_events(workspace_id, session_id, created_at);
CREATE INDEX idx_token_usage_events_turn    ON token_usage_events(workspace_id, session_id, turn_id);
```

### Migration wiring — three call sites, not one (finding C2)

`internal/storage/context_schema.go` dispatches by a literal current
version in three separate places; all three need a v12 addition or the
migration silently doesn't run in two of the three cases:

1. `const currentContextSchemaVersion = 11` → bump to `12`.
2. The steady-state open path (`context_schema.go:30-31`) hard-calls
   `ensureContextSchemaV11(db)` when `version == currentContextSchemaVersion`
   — this literal call must become `ensureContextSchemaV12(db)`, or every
   ordinary open of an already-migrated store keeps running v11's narrower
   repair check and **never** runs v12's, even though `currentContextSchemaVersion`
   itself was bumped.
3. `repairContextSchema`'s dirty-row loop (`context_schema.go:158-182`) has
   one `if v == N { ensureContextSchemaVN(db) }` branch per version, no
   generic fallthrough — needs its own `if v == 12 { ensureContextSchemaV12(db) }`
   branch, or a store left dirty mid-v12-migration reaches
   `finalizeContextVersion` (which only checks the table witness) but never
   runs v12's actual repair.

Otherwise follows `internal/storage/context_schema_v11.go`'s two-phase
pattern exactly: `applyContextSchemaV12` creates the table + indexes in one
migration transaction (`inMigrationTx`, so a crash mid-apply can't leave a
table without its indexes — verified against `context_schema_v11.go`'s
`inMigrationTx` usage, not assumed), a second transaction sets
`PRAGMA user_version = 12` and clears `dirty`; `ensureContextSchemaV12`
mirrors `ensureContextSchemaV11Tx`'s idempotent existence check for repair.

## Write path implementation notes

`UsageRecord` (`internal/agent`, shown above in **Design overview**) is a
plain struct — `Kind`, `Provider`/`Model`, the token-count fields per kind,
`Summarized *bool`/`Reason` for compaction, `AgentTask`/`AgentName`/
`AgentDepth`, `SessionID`/`TurnID`, `CreatedAt` stamped by the writer at
write time (not by the caller, so clock skew across a long turn doesn't
matter — each row gets its own real timestamp).

`internal/storage` implements `agent.UsageWriter`:

```go
func (s *SQLite) RecordUsageEvent(ctx context.Context, workspaceID string, record agent.UsageRecord) error {
    // one INSERT, its own transaction (or no explicit transaction at all —
    // a single statement is already atomic), guarded by the same
    // s.writeMu/retrySQLiteBusy pattern every other write in this file uses
}
```

No config toggle (finding S7): this runs unconditionally, the same way
`chat_sessions.token_count` is written unconditionally today.

### Subagent scope — reopened, not necessarily descoped

Rev 2 descoped subagent recording to Phase 3 because finding C1 showed
subagent turns never set `opts.EventBus`, so a bus-routed recorder would
never see them. Rev 3's `UsageWriter` is a field independent of
`EventBus` — and `internal/subagents/multi_step.go:216`'s `loopOptions`
(verified directly, not assumed) already builds its `agent.Options` with
`SessionID: req.SessionID`, `TurnID: req.TurnID`, `Role: req.Role`,
`Depth: req.Depth + 1`, `ParentID: req.ID` — every piece of attribution
data `token_usage_events` wants is already sitting right there at subagent
`Options` construction, just never wired anywhere durable.

This means subagent recording may cost only "thread `opts.UsageWriter`
through `MultiStepHandler` the same way `h.ContextPreparationManager`
already is" (`multi_step.go:239-247` shows that exact pattern for a
different session-scoped dependency) — not a separate design effort. Kept
as a distinct, smaller Phase 2 item rather than folded into Phase 1's
initial cut, because two things still need settling in Step 1's task
breakdown rather than assumed here: (1) the exact `Role`/`Depth`/`ParentID`
→ `agent_name`/`agent_depth`/`agent_task` field mapping, and (2) whether
`MultiStepHandler` is constructed once per session (cheap to thread a
writer through once) or per dispatch (need to confirm before claiming
"no new call sites").

## Read path

Unchanged from rev 1 — this part of the design held up under both reviews.

### Go API (`internal/storage`)

```go
// QueryTokenUsage returns raw rows for a session, newest first, optionally
// filtered by kind. Callers needing "just the totals" should prefer
// SummarizeTokenUsage instead of summing this client-side.
func (s *SQLite) QueryTokenUsage(ctx context.Context, principal contextstate.Principal, sessionID string, opts UsageQueryOptions) ([]TokenUsageRow, error)

// SummarizeTokenUsage aggregates one session's ledger into per-kind totals:
// sum(input_tokens), sum(output_tokens), sum(cached_input_tokens),
// cache hit rate, compaction count, sum(elided_bytes), etc.
func (s *SQLite) SummarizeTokenUsage(ctx context.Context, principal contextstate.Principal, sessionID string) (TokenUsageSummary, error)
```

### CLI: `mivia sessions usage-history <name>`

A sibling to the existing `mivia sessions usage <name>`
(`internal/cli/sessions_command.go:319`), which only re-estimates the
current message blob's size — kept as-is, since it answers a different
question ("how big is this session's context right now"). The new command
answers "what actually happened, over time":

```
$ mivia sessions usage-history my-session
TURN     KIND          PROVIDER   MODEL              IN     OUT    CACHED  ELIDED  SUMMARIZED
turn:3   token_usage   deepseek   deepseek-v4-flash  12000  850    -       -       -
turn:3   cache_usage   deepseek   deepseek-v4-flash  12000  -      9800    -       -
turn:7   compaction    -          -                  -      -      -       42000   false (no summarizer configured)
...

$ mivia sessions usage-history my-session --json
```

Follows the existing subcommand shape (`runSessionsUsage` at
`sessions_command.go:319` is the closest template): `--workspace`, `--json`,
positional session name, text table via `writeSessionsTable`-style helper
or JSON via `writeSessionsJSON`.

## Retention

`DeleteSessionSnapshot` (`internal/storage/chat_sessions.go`) gains a
`DELETE FROM token_usage_events WHERE workspace_id = ? AND session_id = ?`
in its existing transaction. Finding S5/C3's original orphan-row scenario
(an async, queued write landing after a delete already committed) cannot
recur — rev 3 has no queue, every write is synchronous and immediate.

A narrower, honestly-stated residual case remains: `mivia sessions delete
<name>` runs as its own short-lived process against the shared SQLite file
(`newCatalogSession`, `internal/cli/sessions_command.go`), independent of
any *other* long-running `mivia chat` process that might still be mid-turn
on that same session. Nothing in this design (or, as far as this review
went, anywhere else in the codebase) coordinates "don't delete a session
that's actively mid-turn in another process" — a usage row for turn N could
land microseconds after another process's delete of that same session_id
committed. This is the same category of gap as deleting a session out from
under any other in-flight write to it, not something specific to this
design, and is accepted as out of scope for Phase 1 rather than solved
here.

New operator knob, following the existing "everything is 0 = uncapped"
convention in `[context]`: `max_usage_ledger_rows` (0 = uncapped). A
maintenance function (`PruneTokenUsageEvents`, mirroring
`PruneSessionSnapshots`) trims the oldest rows past the cap; called from the
same places session pruning already runs, not on a new timer.

## Known gap this design does not close

`EmitTokenUsage`/`EmitCacheUsage` only fire from `internal/agent/loop.go`
(`internal/agent/loop.go:444,452`) — i.e. only for tool-enabled turns that
reach the agent loop. A plain `--no-tools` chat session calls the provider
directly via `Completer.ChatStream` and never emits either event, so the
ledger will be blind for that path on day one. Closing it means calling the
same `Emit*` functions (now writing via `UsageWriter`, not just the bus)
from the plain-context call sites in `internal/chat`, passing the
`ChatStream`/`ChatTurn` response's usage — mechanical, but still its own
change. Scoped as a follow-up rather than folded in, to keep Phase 1's diff
to one mechanism plus its wiring.

## Testing plan

- `internal/agent`: unit test that `EmitTokenUsage`/`EmitCacheUsage`/
  `EmitCompaction` call `opts.UsageWriter.Record` in addition to publishing
  to the bus (both behaviors, one test each, asserting neither regresses
  the other); a nil `UsageWriter` is a silent no-op (subagent loops and any
  caller that doesn't wire one keep working unchanged).
- `internal/agent`: a `UsageWriter` that returns an error must not
  propagate it to the turn — the exact "never fail a turn" contract from
  Goal 3, tested directly rather than assumed.
- **The scenario that motivated rev 3**: a test that records several
  `UsageRecord`s across simulated "steps" of one long turn, then simulates
  the turn never completing (no commit, no `InjectedSummary`, nothing) —
  assert every already-recorded row is still present. This is the test rev
  2 could not have passed.
- `internal/storage`: migration test mirroring the v11 pattern, explicitly
  covering all three dispatch sites from finding C2 — fresh DB reaches v12
  cleanly; a store already at v12 opened a second time still runs
  `ensureContextSchemaV12` (not just v11's); a DB left dirty mid-v12-migration
  heals via the repair-loop's new `v == 12` branch.
- `internal/storage`: `RecordUsageEvent`/`QueryTokenUsage`/
  `SummarizeTokenUsage` tests over a seeded table.
- `internal/cli`: integration test extending the
  `context_summary_integration_test.go` scripted-completer harness — run a
  turn, assert a matching `token_usage_events` row lands; test for
  `sessions usage-history` text and `--json` output.
- Regression: confirm `DeleteSessionSnapshot` removes the session's ledger
  rows.

## Rollout

1. **Phase 1 (this design)**: schema v12 (all three dispatch sites),
   `agent.UsageWriter`/`UsageRecord`, `storage.RecordUsageEvent`, wiring
   into `contextmgr.ContextManager` + per-turn `agent.Options`
   (`internal/chat/session.go`), `QueryTokenUsage`/`SummarizeTokenUsage`,
   `mivia sessions usage-history`. Root-loop (tool-enabled) turns only. No
   behavior change for existing users beyond new rows accumulating.
2. **Phase 2 (follow-up, believed small)**: thread `opts.UsageWriter`
   through `MultiStepHandler`/`loopOptions` (`internal/subagents/multi_step.go`)
   so subagent turns record too — see **Write path**'s "Subagent scope"
   note. Kept out of Phase 1 only because the field-mapping and
   handler-lifetime questions there are unconfirmed, not because it's
   believed to need new architecture.
3. **Phase 3 (follow-up)**: close the plain-chat emission gap (**Known
   gap**).
4. **Phase 4 (follow-up, needs new provider config)**: cost/pricing
   rollups once a $/token table exists somewhere in provider config.

## Open questions

- **Subagent field mapping and handler lifetime** (Phase 2): does
  `Role`/`Depth`/`ParentID` on `agent.Options` map 1:1 onto
  `agent_name`/`agent_depth`/`agent_task`, and is `MultiStepHandler`
  constructed once per session (cheap to thread a writer through once) or
  per dispatch? Needs a closer read of `internal/subagents/` construction
  sites before Phase 2 is scoped, not assumed here.
- **Per-workspace vs. global store**: unchanged from rev 1 — uses whichever
  `*storage.SQLite` the caller already resolved for
  `context_checkpoints`/`chat_sessions`, no new resolution logic.
- **Row volume**: a long-running turn with many provider/tool steps, or a
  long-lived session overall, can accumulate many rows — more so under rev
  3 than rev 2, since nothing batches anymore. The two indexes above keep
  session/turn lookups cheap; `max_usage_ledger_rows` exists for operators
  who want a hard ceiling regardless. Worth a rough sanity check in Step 1
  of realistic row counts (rows per provider call × calls per turn × turns
  per session) against SQLite's comfortable range, though nothing here
  suggests it's a real concern at this row size.
