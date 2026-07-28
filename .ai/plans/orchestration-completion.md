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

1. **Fix 3 bugs** (immediate, verified by audit agents)
2. **Implement Phase 3** — durable LedgerRepository via storage.Store
3. **Implement Phase 4** — EventBus metrics + operator views
4. **Final verification** — race tests, focused tests, `make verify`

## DOD Gate

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
- [ ] Phase 3: StorageLedgerRepository implemented + tested
- [ ] Phase 4: Metrics adapter + operator diagnostics
