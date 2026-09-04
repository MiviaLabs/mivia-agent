# Memory index reconciler — implementation plan (v3, review-converged)

Branch: `feat/markdown-memory-index`. Status: round 1 returned Block (7
findings); round 2 returned Block on one lifecycle defect plus
changes_requested on five wording/mechanism gaps; round 3 returned PASS
(plan-review) and approved (correctness review) with no required fixes —
their advisory precision fixes are folded in. The deep design (tier
interaction, no-op skip, deadlock freedom, retry contract, concurrency model)
was verified clean in round 2. The traceability table at the end covers all
rounds.

## Goal

Keep the derived SQLite memory index fresh without making every memory
operation pay a full Markdown rescan. A harness-owned reconciler watches the
memory directories, rescans a changed scope, and applies one transactional
`SyncMemoryIndex`. The read path keeps its rescan as a backstop but skips it
when the scope is known fresh.

## Drivers (why this is worth building)

1. `docs/product/memory.md` ("Markdown and the derived index") already states
   "It updates the shared SQLite index after a file changes". The code is
   pull-only today (`internal/cliagents/markdown_store.go` refreshes inside
   every operation). This plan makes the documented behavior true.
2. Every `Search`, `Count`, `PromoteToCore`, `CoreEntries` pays a full
   rescan + sync even when no file changed. With the reconciler marking scopes
   fresh, the common read becomes index-query-only.
3. Per-operation rescans scale with traffic; a debounced watcher scales with
   actual file changes.

## Scope

In scope:

- `internal/cliagents`: new reconciler type; store freshness tracking and
  read-path skip; session lifecycle wiring (start on the long-lived chat path
  only; stop before store close).
- `internal/memory`: exported dir accessors on `MarkdownSource` (leaf package;
  no new internal import edges).
- `internal/storage`: wrap `SyncMemoryIndex` in `retrySQLiteBusy`; no-op
  detection so an unchanged scan writes zero rows.
- `internal/config`: `index_refresh_interval_seconds` under `[memory]`.
- New external dependency: `github.com/fsnotify/fsnotify`.
- `docs/product/memory.md`: describe watcher, fallback, and freshness skip.

Out of scope (non-goals):

- Recursive watches. `MarkdownSource.Scan` is flat (`os.ReadDir`,
  `internal/memory/markdown_source.go:99-116`), so one watch per scope
  directory is complete.
- Single-watcher coordination (leadership) between sibling sessions on the
  shared org directory. Each session runs its own reconciler; see Concurrency
  model for why that is safe and cheap.
- Network-filesystem detection or warnings for the store path.
- A separate on/off toggle for the watcher. The reconciler runs only for the
  Markdown backend; the process-local `memory` backend never builds an index.
- Removing the read-path refresh. It is a permanent backstop; see
  Read-path interplay.

## Design

### Ownership and package placement

The reconciler lives in `internal/cliagents` (`memory_reconciler.go`).
`internal/cliagents` already imports `internal/memory`, `internal/storage`,
`internal/workspace`, and `internal/config`, so placement adds zero new
internal edges. `internal/memory` stays a leaf: it gains two exported
accessors (`ProjectDir()`, `OrgDir()`) and no imports. No
`.mivia/policy/import-layers.json` change is expected; if
`scripts/check_import_layers.py` disagrees, the edge is declared before the
code lands, never by weakening the check.

fsnotify is justified once, here: the standard library has no filesystem
event API; fsnotify is the maintained cross-platform choice; only
`internal/cliagents` imports it.

### Lifecycle (start seam, stop seam, one-shot paths)

Call-graph facts, verified: three paths reach `ConfigureChatWorkspace` — the
long-lived chat path `internal/clichat/chat_command.go:184` →
`ConfigureChatWorkspace` → `WireSessionMemory`, and two one-shot clichat
commands, `internal/clichat/compact_command.go:54` and
`internal/clichat/sessions_command.go:342`. A fourth path bypasses it
entirely: `mivia memory search`/`promote` (`internal/cli/memory_command.go:58,97`
→ `internal/cli/memory_store.go` → `OpenMemoryStoreWithReadOnly`). Starting
the reconciler inside `ConfigureChatWorkspace` would therefore leak watchers
into the one-shot clichat commands, so it does not start there. (Test callers
of `ConfigureChatWorkspace` also get no reconciler by design — do not move
the start seam back inside it to make tests exercise the watcher.)

- Start seam: an exported helper in `internal/cliagents`,
  `StartMemoryIndexReconciler(store memory.Store, fallback time.Duration)
  (stop func(), ok bool)`. It type-asserts the store to the Markdown-backed
  implementation through `ownedMarkdownStore` and starts the reconciler;
  `ok` is false for the process-local backend or an already-started store.
  Exactly one call site invokes it: `internal/clichat/chat_command.go`,
  after `ConfigureChatWorkspace` returns successfully.
- Stop-before-close ordering at that call site: `defer cleanup()` is
  registered first, the reconciler stop second; defers run LIFO, so `stop()`
  executes before the cleanup that closes the store, and no sync can be in
  flight at `index.Close()`.
- Ownership direction: the start helper hands the stop func to the call site
  and records it on the store; `ownedMarkdownStore.Close`
  (`internal/cliagents/memory_support.go:80-87`) invokes it defensively.
  `Stop` is idempotent. There is no package global.
- One-shot paths (`mivia memory search`/`promote`, `mivia chat compact`,
  `mivia sessions usage`) never call the helper, never set
  `reconcilerAttached`, and keep byte-for-byte today's pull-only behavior.
- Stop mechanics: there is no session context to inherit
  (`ConfigureChatWorkspace` takes none), so the reconciler owns
  `context.WithCancel(context.Background())`. `Stop()` cancels, closes the
  watcher, and waits for the loop goroutine on a done channel. `Stop` never
  takes `s.mu`: an in-flight sync settles (completes, or aborts with
  rollback — `retrySQLiteBusy` selects on ctx done) before `Stop` returns,
  bounded by one scan plus the ~18 s busy-retry budget. The load-bearing
  invariant is that nothing is in flight when `Stop` returns, so store close
  never races a sync.

### Watching

- Watch directories, not files. fsnotify's own FAQ: atomic temp+rename saves
  lose the watch on the original file. The repo's own `atomicWrite`
  (`internal/memory/markdown_source.go`) saves exactly this way, so the
  directory watch also catches the store's own saves; the resulting sync is a
  no-op thanks to unchanged-scan skip.
- Watch the two scope directories only: `<project>/.agents/memories` and
  `~/.mivia/memories` (the latter only when `org_id` is configured).
- A directory may not exist at start (created on first
  `Save`). A missing directory is not an error: record it as missing, and
  retry `Add` on every fallback tick until it exists. `Scan` already treats a
  missing directory as empty (`markdown_source.go:108-110`), so a sync of a
  missing scope correctly empties that scope's index rows.
- Watcher errors (`Errors()` channel) degrade the affected scope: log once per
  consecutive failure streak, mark the scope degraded, and keep running on the
  fallback timer. fsnotify auto-removes a watch when the watched path is
  deleted or renamed, so a delete-and-recreate of a memory directory silently
  drops its watch (the Windows backend is the documented exception — it does
  not remove the watcher on rename — which only changes how quickly the loss
  is noticed, not the recovery). The fallback tick therefore consults the
  watcher's watch
  list (or per-scope tracked state) and re-`Add`s any configured scope whose
  watch is gone; re-`Add` of an already-watched path is a documented no-op
  returning nil, so per-tick re-`Add` cannot spuriously error. The freshness
  TTL independently bounds how long a lost watch can go unnoticed.

### Reconcile loop

- Events map to a scope by watched directory. Debounce per scope (250 ms
  timer, reset on each event), so a burst coalesces into one sync.
- Every sync — event-driven or timer-driven — goes through the store:
  `syncScope(ctx, scope)` takes `s.mu`, runs `Source.Scan`, maps docs, calls
  `storage.SyncMemoryIndex`, and on success stamps `lastSync[scope]`. The
  reconciler never touches `storage.SQLite` directly. This serializes
  background syncs with `Save`/`Delete` under the same mutex that today
  serializes the read-path refresh, so the stale-snapshot interleaving
  (scan before a concurrent `Delete` lands, commit after) cannot happen.
- Fallback tick every `index_refresh_interval_seconds` (default 30 s):
  attempt re-`Add` of missing or degraded watches, then sync every configured
  scope — project always, org only when `org_id` is configured. An
  unconfigured org scope must never produce a per-tick validation error
  (`validateMemoryIndexDocuments` rejects an empty org id,
  `internal/storage/memory_index.go:87-89`). The timer runs regardless of
  watcher health; watcher events are an optimization, the timer is the
  correctness bound. This is also the honest justification for the fallback:
  fsnotify documents buffer overflow (`ErrEventOverflow`, inotify
  `IN_Q_OVERFLOW`) and lost watches, not generic event coalescing.
- Error policy: scan and sync errors are logged (standard `log` package, one
  line per failure streak, not per retry) and retried with backoff capped at
  the fallback interval. Errors never propagate to session startup, tool
  results, or the read path. A failed sync leaves the previous index contents
  in place — `SyncMemoryIndex` is one transaction — and the failed scope's
  `lastSync` simply ages out, so the read path resumes rescanning within one
  fallback interval. Recovery is bounded by the TTL, not immediate at the
  failed sync.

### Read-path interplay (the driver made concrete)

`markdownStore` gains per-scope freshness state: `lastSync[scope]` (stamped
under `s.mu` after every successful sync, whichever path drove it),
`degraded[scope]`, and `reconcilerAttached` (set by
`StartMemoryIndexReconciler`).

- Read operations (`Search`, `Count`, `PromoteToCore`, `CoreEntries`) skip
  the pre-read refresh for a scope when `reconcilerAttached && !degraded &&
  time.Since(lastSync) < fallbackInterval`. Otherwise they refresh exactly as
  today. On any doubt, rescan — watcher events are hints, the TTL is the
  bound.
- Mutating operations (`Save`, `Delete`) always sync their scope after the
  mutation, exactly as today, and stamp `lastSync` on success.
- One-shot paths never set `reconcilerAttached`, so their behavior is
  byte-for-byte today's.
- The read-path refresh is a permanent invariant, not a temporary
  implementation detail: degraded watchers, failed syncs, and process pauses
  longer than the TTL all heal through it. A later optimization may not remove
  it while the reconciler is best-effort.

### Storage changes

1. Busy retry: `SyncMemoryIndex` becomes
   `return retrySQLiteBusy(ctx, func() error { return s.syncMemoryIndexOnce(...) })`
   (`internal/storage/memory_index.go:23`). This matches every other
   multi-process writer in the package (`context_store.go:27,116`,
   `chat_sessions.go:84`). The retry contract holds: the body is one full
   transaction, re-reading current state on each attempt. Note `beginWrite`
   uses the `_txlock=immediate` pool, so `SQLITE_BUSY_SNAPSHOT` cannot arise
   here; plain `SQLITE_BUSY` after `busy_timeout=5000` can, and today fails
   with zero retries.
2. No-op detection, inside the same transaction: load the scope's
   `memory_sources` rows (path → `source_hash`) and `memory_entries` rows
   (path → id, `source_hash`). A doc is skipped only when its `SourcePath`
   is present in BOTH maps with identical `SourceHash` and identical id, and
   exactly one `memory_entries` row matches the path — the schema does not
   enforce path uniqueness (`internal/storage/context_schema_v16.go:21-38`),
   so more than one match is treated as "changed" and healed by the full
   upsert. The scanned-path bookkeeping (`delete(old, path)`) runs for every
   scanned doc whether skipped or not, keeping removed-path deletion correct;
   this also self-heals the `DeleteMemoryIndexEntry` shape where an entries
   row is gone but its `memory_sources` row remains
   (`internal/storage/memory_index_tier.go:79-99`). Changed and added docs go
   through the existing upsert unchanged, including its
   same-path-different-id `DELETE` (`internal/storage/memory_index.go:136`),
   which stays the enforcer of at most one entries row per path. A skipped
   doc's tier column is untouched, preserving an operator promotion — the
   same preservation `SyncMemoryIndex` already promises. (For a fixed path
   the id is a deterministic function of the file bytes, so identical hash
   implies identical id; the id condition is defense in depth.)
3. No explicit checkpoint management: rely on auto-checkpoint (default 1000
   pages) and the WAL fold at close that `internal/storage` already performs.

### Concurrency model (stated, not implied)

N sibling sessions may run N reconcilers against one shared
`~/.mivia/context.db` and one machine-global org directory; the repo already
documents this deployment (`internal/storage/sqlite_busy_retry.go:36-38`).
Safety: every sync is one short transaction; cross-process contention is
cleared by `retrySQLiteBusy`; an unchanged scan writes nothing after (2), so
idle sessions cause no write storm. The cost model is stated honestly: a
fallback tick runs a full `Scan` per configured scope — `ReadDir` plus read,
parse, and sha256 of every `.md` file
(`internal/memory/markdown_source.go:99-146`) — under `s.mu`, per session,
and takes the write transaction even when the no-op skip then writes nothing.
The no-op skip removes write amplification, not scan I/O; the interval is the
knob that bounds this, and the `docs/product/memory.md` rewrite states the
same cost. Worst case under contention: a sync fails, is logged, the scope
stays not-fresh, and the next read or tick heals it. A per-scope debounce key
keeps a project event from syncing the org scope and vice versa.

### Configuration

- `MemoryConfig.IndexRefreshIntervalSeconds int` with
  `toml:"index_refresh_interval_seconds"` (`internal/config/types_memory.go`).
- Resolve in `resolveMemoryConfig` (`internal/config/memory.go`) following the
  house pattern: absent or `<= 0` means the default (30); values above 86400
  are a hard resolve error. The interval is only the fallback and freshness
  TTL; the watcher has no separate interval.
- Documented in `docs/product/memory.md` (owner per `docs/OWNERS.yaml`),
  alongside the existing "Markdown and the derived index" section. No new doc
  files.

## Plan (ordered steps)

1. `internal/memory`: add `ProjectDir()` / `OrgDir()` accessors; unit test.
2. `internal/storage`: split `SyncMemoryIndex` into the `retrySQLiteBusy`
   wrapper + `syncMemoryIndexOnce`; add no-op skip inside the transaction;
   tests (busy retry under a held second-connection write; unchanged second
   sync writes zero rows — assert via `indexed_at` and row identity; concurrent
   sync from two handles on one database).
3. `internal/cliagents`: freshness state + `syncScope` on `markdownStore`;
   read-path skip in `Search`/`Count`/`PromoteToCore`/`CoreEntries`; tests with
   a spy `MarkdownSource` counting scans (skip when fresh; rescan when
   degraded, when TTL expired, and when no reconciler is attached).
4. `internal/cliagents`: `memory_reconciler.go` — watcher, per-scope debounce,
   fallback tick, missing-dir retry, degrade/log-once policy, idempotent
   `Stop()`. Tests with a real fsnotify watcher on `t.TempDir()`: atomic
   temp+rename produces exactly one debounced sync; ten rapid events coalesce;
   malformed file → log once, index keeps previous contents, bounded retries;
   missing dir at start then created mid-run → picked up; `Stop()` before
   close under `-race`.
5. Wire the start at the single call site: `internal/clichat/chat_command.go`
   calls `StartMemoryIndexReconciler` after `ConfigureChatWorkspace`
   succeeds and registers the stop defer after the cleanup defer (LIFO gives
   stop-before-close). `ConfigureChatWorkspace` itself is untouched, so its
   one-shot callers (`internal/clichat/compact_command.go:54`,
   `internal/clichat/sessions_command.go:342`) structurally cannot start a
   watcher. Test all three one-shot paths (those two plus
   `internal/cli/memory_store.go`) start nothing and keep pull-only reads.
6. `internal/config`: field, default, bound, tests (`0` → 30; `> 86400` →
   resolve error).
7. `docs/product/memory.md`: rewrite the derived-index paragraph (watcher +
   fallback + freshness skip + TTL bound + the honest per-tick scan cost).
   STE wording.

New files target ≤ 500 LOC and functions ≤ 80 — the policy's soft lines
(hard gates are 800/120, `.mivia/policy/go-structure.json`;
`scripts/check_go_structure.py`).

## Tests (invariant-first)

- T1 Busy retry: hold a write transaction on a second connection beyond
  `busy_timeout`, call `SyncMemoryIndex`, assert success via retry, no error
  surfaces.
- T2 No-op sync: sync twice with unchanged files; assert the second sync
  performs zero row writes (unchanged `indexed_at`, same row identity) and
  preserved tier.
- T3 Two handles, one database: concurrent `SyncMemoryIndex` loops from two
  `*SQLite` handles complete with no escaped busy errors; final state equals
  one full scan.
- T4 Serialization: interleave `syncScope` with `Delete`; after `Delete`
  returns, the deleted id never reappears in `SearchMemoryIndex` (guards the
  resurrected-entry interleaving; run under `-race`).
- T5 Watcher: real fsnotify on `t.TempDir()`; atomic temp+rename save yields
  exactly one debounced sync; N rapid events coalesce to one. Assertions poll
  with a deadline (never fixed sleeps); the watcher is closed before the
  temp directory is cleaned up (matters on Windows).
- T6 Missing dir: reconciler starts with a nonexistent project dir (no error,
  empty scope); dir + file created mid-run are picked up by the tick's
  watch-list re-`Add` within one interval.
- T7 Poisoned input: malformed and symlinked `.md` files produce exactly one
  log line per failure streak, bounded retry attempts (no hot loop), and the
  previous index contents keep being served. The staleness contract is
  bounded by the TTL, not identical-to-today: between the poisoned write and
  the freshness expiry, a read may serve pre-change results through the
  freshness skip; a read after the TTL expires rescans and surfaces the error
  as today. Assert that bounded window; do not assert immediate failure.
- T8 Degradation: force a watcher error → scope marked degraded → read path
  rescans (spy source called) → recovery clears the mark.
- T9 Lifecycle: the `chat_command.go` call site stops the reconciler before
  the cleanup closes the store; `Stop` twice is a no-op; and all three
  one-shot paths (`internal/clichat/compact_command.go:54`,
  `internal/clichat/sessions_command.go:342`,
  `internal/cli/memory_store.go`) start no watcher and keep pull-only reads.
- T10 Config: default, boundary, and error cases for the new key.

## Verification

- `go test ./internal/storage/... ./internal/cliagents/... ./internal/memory/...
  ./internal/config/... -race` (targeted; no fuzzing — the repo forbids
  default-parallelism fuzz runs).
- `make test-changed`, then `make verify` before commit (offline gates,
  including `scripts/check_import_layers.py` and `scripts/check_go_structure.py`).
- Manual smoke: run `mivia` in a scratch workspace, edit a memory file
  externally, confirm the next `memory_search` reflects it without a rescan
  (log line or scan-counter in a debug build).

## Prior-findings traceability

| Prior finding | Where addressed |
|---|---|
| Plan-review 1: driver unstated, watcher redundant | Drivers; Read-path interplay |
| Plan-review 2: `SyncMemoryIndex` lacks busy retry | Storage changes (1); T1 |
| Plan-review 3: per-session fan-out on shared DB | Concurrency model |
| Plan-review 4: package ownership, import policy | Ownership and package placement |
| Plan-review 5: builder trip hazards | Watching; Lifecycle; Plan steps 4-5 |
| Plan-review 6: fallback justification misattributed | Reconcile loop (overflow/lost watches, not coalescing) |
| Plan-review 7: no tests/verification | Tests; Verification |
| Review: lifecycle-no-stop-seam (high) | Lifecycle (no context exists; cleanup-func stop; one-shot exclusion); T9 |
| Review: sync-no-busy-retry (medium) | Storage changes (1); Concurrency model; T1, T3 |
| Review: reconciler-mu-race (medium) | Reconcile loop (syncs under `s.mu`); T4 |
| Review: write-amplification-no-noop (medium) | Storage changes (2); Concurrency model; T2 |
| Review: reconciler-error-policy-missing (medium) | Reconcile loop error policy; T7 |
| Review: watch-degradation-security (medium) | Watching (degrade, re-add, symlink path); T6, T8 |
| Review: backend-exclusion-clean (low) | Scope (non-goals): reconciler only for Markdown backend |
| Review: config-key-needs-validation-and-docs (low) | Configuration; Plan step 6; T10 |
| Review: keep-read-path-backstop (low) | Read-path interplay (permanent invariant) |
| Advisory: checkpoint "management" overstated | Storage changes (3) |
| Advisory: network-fs fallback path | Out of scope (documented non-goal) |
| Advisory: per-scope debounce key | Reconcile loop; Concurrency model |
| R2 plan-review: one-shot start seam contradicted the call graph (compact/sessions also call `ConfigureChatWorkspace`) | Lifecycle (call-graph facts; exported start helper; single call site); Plan step 5; T9 |
| R2 plan-review: T7 asserted a false "identical to today" invariant | Tests T7 (bounded-staleness contract) |
| R2 review: noop-skip map presence / multi-row path | Storage changes (2) |
| R2 review: watch auto-remove mechanism misstated | Watching (auto-remove + watch-list re-`Add`) |
| R2 review: fallback tick cost understated | Concurrency model (honest cost); Plan step 7 |
| R2 review: fallback tick ignored org gating | Reconcile loop ("every configured scope") |
| R2 advisories: ownership direction, T5/T6 flakiness, LOC soft lines | Lifecycle; Tests T5/T6; Plan (LOC note) |
