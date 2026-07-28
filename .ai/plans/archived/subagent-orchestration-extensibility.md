# Subagent orchestration and live ledger

Status: ✅ All phases complete — Retry, Durable Resume, TUI dashboard pending integration
Current phase: All phases implemented; remaining: TUI run dashboard panel (Phase D2)
Last verified: 2026-07-29
Next action: Integrate TUI dashboard panel (Phase D2)

## Objective

Refactor Mivia's subagent execution into an extensible orchestration boundary with one clear model-facing spawn tool, unique human-readable agent names, explicit parent/child identity, dependency-aware DAG execution, and a live queryable ledger that the root orchestrator can use to understand current application state.

---

## ✅ FULLY IMPLEMENTED

| Requirement | Location | Notes |
|---|---|---|
| LedgerRepository interface | `internal/ledger/repository.go` | All 14 methods |
| Run/Task/Attempt types with Clone() | `internal/ledger/types.go` | Defensive copies |
| State transitions (valid/invalid) | `internal/ledger/transition.go` | Full state machine; running→retry_pending added |
| MemoryLedgerRepository | `internal/ledger/memory.go` | In-memory with RWMutex |
| DisplayNameGenerator | `internal/ledger/displayname.go` | Unique, concurrent-safe |
| StorageLedgerRepository (SQLite) | `internal/ledger/storage.go` | Dual-write, projection rebuild |
| RebuildProjection | `internal/ledger/storage_schema.go` | Deterministic event replay |
| Recover() | `internal/ledger/storage.go` | Marks interrupted runs |
| Coordinator interface + implementation | `internal/coordinator/` | Extracted Coordinator interface, unexported struct |
| Coordinator.Spawn/Join/Inspect/Cancel | `internal/coordinator/` | DAG, idempotency, handles |
| Coordinator.WithRetryPolicy | `internal/coordinator/` | Enables automatic retry for failed/timed-out tasks |
| Coordinator.ResumeInterruptedRun | `internal/coordinator/recovery.go` | Marks running tasks as interrupted, re-runs DAG |
| Coordinator.ListInterruptedRuns | `internal/coordinator/recovery.go` | Lists recovered interrupted runs |
| Coordinator.SubscribeLifecycle | `internal/coordinator/` | Pub/sub for lifecycle events |
| Retry execution engine | `internal/coordinator/retry.go` | RetryPolicy, RetryState, exponential backoff with jitter |
| Auto-retry in runDAG | `internal/coordinator/coordinator.go` | `runDAG` transitions failed/timed_out→retry_pending→queued |
| Crash-recovery tests | `internal/ledger/storage_test.go` | Kill/restart detection, task state preservation |
| Durable resume tests | `internal/coordinator/integration_test.go` | Full resume flow with and without retry |
| Stale-attempt fencing test (post-cancel) | `internal/coordinator/coordinator_test.go` | TestCoordinator_StaleAttemptRejectedAfterCancel |
| Storage oracle equivalence tests | `internal/ledger/storage_test.go` | TestStorageOracleEquivalence (order-independent) |
| Configurable handle retention | `internal/config/types.go` + `internal/cli/orchestrate.go` | HandleRetentionSeconds config field |
| CloseRun documentation | `internal/ledger/storage.go` | Dual-event semantics documented |
| Lifecycle event listener bus | `internal/coordinator/` | SubscribeLifecycle + emitLifecycleEvent |
| spawn_agent tool | `internal/cli/orchestrate.go` | Tasks + DAG + wait modes |
| inspect_agents tool | `internal/cli/orchestrate.go` | Run snapshots |
| join_run tool | `internal/cli/orchestrate_lifecycle.go` | Blocking wait + results |
| cancel_run tool | `internal/cli/orchestrate_lifecycle.go` | Two-phase cancel |
| delegate + dispatch_tasks compatibility | `internal/cli/delegate.go`, `dispatch.go` | Routed through coordinator |
| MetricsAdapter | `internal/events/metrics.go` | Per-kind counts + timing |
| Diagnostics | `internal/cli/diagnostics.go` | ListRuns, ActiveHandles, MetricsSnapshot |
| Benchmarks | `internal/ledger/bench_test.go` | Memory + Storage ledger |
| Config (store_backend, handle_retention_seconds) | `internal/config/types.go` | memory/sqlite selection + retention |

## 📋 REMAINING (Phase D2-D3 — LOW priority)

| Item | Priority | Notes |
|------|----------|-------|
| **TUI run dashboard panel** | LOW | Would use SubscribeLifecycle for live updates. No dedicated TUI panel showing active runs, task DAGs, cancellation controls. Requires TUI work. |
| **Human security/privacy review gate** | LOW | Documentation/process item. |
| **OpenTelemetry export** | LOW | Explicitly deferred — not needed for sync bus. MetricsAdapter provides in-process alternative. |

## Verification

```
go build ./...       → PASS
go vet ./...         → PASS
go test -race ./...  → PASS (20/20 packages, 0 failures)
```
