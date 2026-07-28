# Phase 3: Task Breakdown — Small Parallel Units

## Dependency Graph

```
Wave 1 (parallel, no deps):
  T1: Schema layer (schema.go) — 1 SP
  T2: Config changes (types.go, mivia.toml) — 1 SP

Wave 2 (parallel, deps on T1+T2):
  T3: StorageLedgerRepository CRUD (storage.go) — 2 SP
  T4: Recovery + Wiring (orchestrate.go) — 1 SP

Wave 3 (depends on T3):
  T5: Tests (storage_test.go) — 2 SP

Wave 4 (loop):
  T6: Bug audit + fix loop (all files)
```

## T1: Schema Layer (`internal/ledger/storage_schema.go`)
- Define 8 storage event kind constants
- Write `marshalRunSnapshot()`, `unmarshalRunSnapshot()` JSON round-trip
- Write `marshalTaskSnapshot()`, `unmarshalTaskSnapshot()`
- Write `marshalLifecycleEvent()`, `unmarshalLifecycleEvent()`
- Write `RebuildProjection(events []storage.Event) → (RunSnapshot, []TaskSnapshot, error)`
- Pure functions, no runtime deps beyond `ledger` + `storage` types

## T2: Config Changes
- Add `StoreBackend string`, `StorePath string` to `SubagentConfig` in `types.go`
- Add `defaultStorePath()` in `internal/config/defaults.go`
- Add `StoreBackend` / `StorePath` to `Resolved` struct in `types.go`
- Resolve in `load.go` (read from TOML, fallback memory)
- Update `mivia.toml.example` with commented-out options
- Pure config, no runtime deps

## T3: StorageLedgerRepository CRUD (`internal/ledger/storage.go`)
- Struct definition wrapping `storage.Store` + `MemoryLedgerRepository` projection
- Constructor `NewStorageLedgerRepository(store)`
- All 14 LedgerRepository methods:
  - CreateRun → write `run_created` event + dual-write to mem
  - GetRun → read from mem projection
  - GetRunByIdempotencyKey → iterate mem projection
  - ListRuns → from mem projection
  - CreateTask → write `task_created` event + dual-write
  - GetTask / ListTasks → from mem projection
  - AppendEvent → write `lifecycle_event` event
  - ListEvents → from mem projection
  - CompareAndSetTaskStatus → write `task_status_changed` event
  - SetTaskOutput → write `task_output_set` event
  - SetTaskAttempt → write `task_attempt` event
  - CloseRun → write `run_closed` event
  - DeleteRun → ??? (maybe clear events?)
- On construction: rebuild projection from store events
- Close() → close underlying store

## T4: Recovery + Wiring
- `Recover(ctx) → []RecoveredRun` method on StorageLedgerRepository
- Scan all events, rebuild projection, mark running/queued runs as `interrupted_unrecoverable`
- Add `ListRunIDs()` to `storage.Store` interface (or use event scan)
- Modify `initCoordinator()` in `orchestrate.go`:
  - If `cfg.StoreBackend == "sqlite"`, open store, create StorageLedgerRepository
  - Call Recover() on startup
  - Register cleanup via `d.OnClose()`
- Add cleanup: close store on dispatcher close

## T5: Tests (`internal/ledger/storage_test.go`)
- Unit tests using `storage.NewMemory()` for fast in-process testing
- Integration tests using `storage.OpenSQLite(":memory:")` for SQLite path
- Test scenarios:
  - Create + read run
  - Full task lifecycle
  - Projection rebuild (simulate restart)
  - Duplicate event prevention
  - Concurrent access (race tests)
  - Close/Delete
  - Recovery: interrupted run marking
  - Empty store recovery

## T6: Bug Audit Loop
- After wave 1-5 complete: dispatch code review agents
- Fix all High/Medium findings
- Re-run tests, verify `go test -race ./...` clean
- Loop until zero findings
