# Orchestration Completion Plan

**Status:** Active implementation plan
**Date:** 2026-07-28
**Supersedes:** `subagent-orchestration-phase-1-implementation.md`, `phase-2-implementation.md`, `phase-4-implementation.md`, `remaining-phases.md`
**Parent master plan:** `.ai/plans/subagent-orchestration-extensibility.md`
**Handoff:** `.ai/handoffs/subagent-orchestration-2026-07-28.md`

## Completed Foundation

- ✅ Phase 0: Architecture contract (LedgerRepository, Coordinator, types)
- ✅ Phase 1: Ledger repository + coordinator seams
- ✅ Phase 2: Unified spawn_agent, inspect_agents, join_run, cancel_run tools
- ✅ Active Run Recovery: In-memory run recovery, session-scoped handles
- ✅ EventBus: 17 event kinds, synchronous in-process bus, UIAdapter

## Bugs Fixed

| # | Severity | Bug | File | Fix | Commit |
|---|----------|-----|------|-----|--------|
| 1 | Medium | `dispatch_tasks` `partial_results` param accepted but **ignored** when coordinator singleton already created with `Partial=false` | `dispatch.go`, `coordinator.go`, `subagents.go` | Added `RunWithPartial` to Pool, wired `partial` override through Spawn → RunHandle → runDAG | `377881d` |
| 2 | Medium | `runThroughCoordinator` never called `storeOrchestrationHandle` — legacy runs invisible to inspect/join/cancel | `orchestrate.go` | Added `storeOrchestrationHandle` call in `runThroughCoordinator` | `377881d` |
| 3 | Low | `delegate` dropped errors — returned `{"status":"no_result"}` when `runResult` had zero results | `delegate.go` | Added result-reporting block before "no_result" fallthrough | `377881d` |
| 4 | Low | `QueuedWriter.Close()` panics on second call (close of closed channel) | `queue.go` | Guarded with `sync.Once`, cached close error | `2f844c0` |
| 5 | Low | `QueuedWriter.Submit()` can't be canceled while waiting for slow store append | `queue.go` | Added second `select` with `ctx.Done()` on result wait | `2f844c0` |

## Phase 3: Durable Persistence (planned)

### Goal
Wire `internal/storage.Store` (SQLite) as a durable `LedgerRepository` backend, replacing `MemoryLedgerRepository` for production use.

### Implementation Plan
1. **Define `StorageLedgerRepository`** in `internal/ledger/` that wraps `storage.Store` and implements `LedgerRepository`
2. **Map run/task/attempt state** to storage events with deterministic projection rebuild
3. **Add config option** to select storage backend (memory vs sqlite)
4. **Recovery:** Startup reconciliation reads durable state, marks orphaned active tasks as `interrupted_unrecoverable`
5. **Tests:** Kill/restart tests, crash recovery, duplicate prevention, projection rebuild

### Non-goals
- Process crash recovery for actively running tasks (requires leases/heartbeats)
- Multi-process coordination
- Removing the in-memory backend

## Phase 4: Observability (planned)

### Goal
Expose bounded, privacy-safe operator visibility using the EventBus.

### Implementation Plan
1. **Metrics adapter** for `events.Bus` — counts and timing per event kind
2. **Operator diagnostics** — run listing, task status summary, active handles
3. **Benchmarks** — ledger write latency, projection rebuild, pool throughput

### Non-goals
- OpenTelemetry export (correlation IDs only, no high-cardinality labels)
- Replacing the ledger/event stream as source of truth
- PII or prompt content in metrics/traces

## Sequence

1. **Fix 3 bugs** ✅ (completed, verified by audit agents)
2. **Implement Phase 3** — durable LedgerRepository via storage.Store (`.ai/plans/phase3-durable-persistence.md`)
3. **Implement Phase 4** — EventBus metrics + operator views (`.ai/plans/phase4-observability.md`)
4. **Final verification** — race tests, focused tests, `make verify`

## Gap Analysis Verification (2026-07-28)

Verified by codebase audit — all tests pass (`go test -race ./...` clean):

| Layer | Status | Files Examined |
|-------|--------|---------------|
| LedgerRepository interface | ✅ Complete | `internal/ledger/repository.go` |
| MemoryLedgerRepository | ✅ Complete + tested | `internal/ledger/memory.go`, `ledger_test.go` |
| Types + transitions | ✅ Complete | `internal/ledger/types.go`, `transition.go` |
| Storage.Store (Memory + SQLite) | ✅ Complete + tested | `internal/storage/store.go`, `store_test.go` |
| Coordinator | ✅ Complete + tested | `internal/coordinator/*.go` |
| Subagents (Pool, multi_step) | ✅ Complete + tested | `internal/subagents/*.go` |
| CLI orchestration tools | ✅ Complete + tested | `internal/cli/orchestrate.go`, `orchestrate_lifecycle.go` |
| EventBus + UIAdapter | ✅ Complete + tested | `internal/events/*.go`, `internal/cli/ui_adapter.go` |
| Agent loop → EventBus | ✅ Complete | `internal/agent/emit.go` |
| **StorageLedgerRepository** | ✅ Implemented + tested | `internal/ledger/storage.go`, `storage_test.go` |
| **Storage event schema** | ✅ Implemented | `internal/ledger/storage_schema.go` |
| **Projection rebuild** | ✅ Implemented | `RebuildProjection()` in `storage_schema.go` |
| **Config (StoreBackend/StorePath)** | ✅ Implemented | `internal/config/types.go`, `defaults.go`, `load.go` |
| **Wiring in initCoordinator** | ✅ Implemented | `internal/cli/orchestrate.go` |
| **Startup recovery** | ✅ Implemented | `Recover()` in `storage.go` |
| **ListRunIDs on Store** | ✅ Implemented | `internal/storage/store.go` (Memory + SQLite) |
| **Metrics adapter** | **❌ NOT IMPLEMENTED** | **No file exists** |
| **Operator diagnostics** | **❌ NOT IMPLEMENTED** | **No file exists** |
| **Benchmarks** | **❌ NOT IMPLEMENTED** | **No file exists** |

**Key findings (updated):**
1. ✅ StorageLedgerRepository implemented with dual-write pattern + projection rebuild
2. ✅ Config fields added for backend selection
3. ✅ initCoordinator creates StorageLedgerRepository when StoreBackend="sqlite"
4. ✅ Startup recovery scans store, marks orphaned active runs
5. ❌ Phase 4 (metrics, diagnostics, benchmarks) still pending
6. ✅ All tests pass, race-clean, vet-clean

## DOD Gate

### ✅ Completed
- [x] Bug 1: PartialResults forwarded through coordinator to pool
- [x] Bug 2: Legacy tools store orchestration handles
- [x] Bug 3: Delegate returns real errors, not "no_result"
- [x] Bug 4: QueuedWriter.Close() is idempotent
- [x] Bug 5: QueuedWriter.Submit() responds to context cancellation
- [x] `go build ./cmd/mivia/` succeeds
- [x] `go test ./...` passes
- [x] `go test -race ./...` passes
- [x] `go vet ./...` clean
- [x] All old phase plan files deleted from git

### ✅ Phase 3: Durable Persistence (detailed plan: `.ai/plans/phase3-durable-persistence.md`)
- [x] Event schema defined (run/task/attempt ↔ storage events)
- [x] StorageLedgerRepository implemented in `internal/ledger/storage.go`
- [x] Projection rebuild: deterministic replay from stored events
- [x] Config option added (`store_backend`, `store_path` in `SubagentConfig`)
- [x] Wiring in `initCoordinator()` in `internal/cli/orchestrate.go`
- [x] Startup recovery: orphaned active tasks → `interrupted_unrecoverable`
- [x] All tests pass + race clean
- [x] `go build ./cmd/mivia/` succeeds

### ❌ Phase 4: Observability (detailed plan: `.ai/plans/phase4-observability.md`)
- [ ] MetricsAdapter for events.Bus (counts + timing per event kind)
- [ ] Operator diagnostics (run listing, task summary, active handles)
- [ ] Benchmarks (ledger write latency, projection rebuild, pool throughput)
- [ ] All tests pass + race clean
