# Subagent orchestration and live ledger

Status: ⚠️ Partially implemented — Phases 0-4 foundational code done, several items still pending
Current phase: Phase 0-4 core complete; remaining: retry execution, durable resume, TUI dashboard, tests
Last verified: 2026-07-29
Next action: Address PARTIALLY-IMPLEMENTED items below before archiving

## Objective

Refactor Mivia's subagent execution into an extensible orchestration boundary with one clear model-facing spawn tool, unique human-readable agent names, explicit parent/child identity, dependency-aware DAG execution, and a live queryable ledger that the root orchestrator can use to understand current application state.

---

## ✅ FULLY IMPLEMENTED

| Requirement | Location | Notes |
|---|---|---|
| LedgerRepository interface | `internal/ledger/repository.go` | All 14 methods |
| Run/Task/Attempt types with Clone() | `internal/ledger/types.go` | Defensive copies |
| State transitions (valid/invalid) | `internal/ledger/transition.go` | Full state machine |
| MemoryLedgerRepository | `internal/ledger/memory.go` | In-memory with RWMutex |
| DisplayNameGenerator | `internal/ledger/displayname.go` | Unique, concurrent-safe |
| StorageLedgerRepository (SQLite) | `internal/ledger/storage.go` | Dual-write, projection rebuild |
| RebuildProjection | `internal/ledger/storage_schema.go` | Deterministic event replay |
| Recover() | `internal/ledger/storage.go` | Marks interrupted runs |
| Coordinator (Spawn/Join/Inspect/Cancel) | `internal/coordinator/` | DAG, idempotency, handles |
| spawn_agent tool | `internal/cli/orchestrate.go` | Tasks + DAG + wait modes |
| inspect_agents tool | `internal/cli/orchestrate.go` | Run snapshots |
| join_run tool | `internal/cli/orchestrate_lifecycle.go` | Blocking wait + results |
| cancel_run tool | `internal/cli/orchestrate_lifecycle.go` | Two-phase cancel |
| delegate + dispatch_tasks compatibility | `internal/cli/delegate.go`, `dispatch.go` | Routed through coordinator |
| MetricsAdapter | `internal/events/metrics.go` | Per-kind counts + timing |
| Diagnostics | `internal/cli/diagnostics.go` | ListRuns, ActiveHandles, MetricsSnapshot |
| Benchmarks | `internal/ledger/bench_test.go` | Memory + Storage ledger |
| Config (store_backend) | `internal/config/types.go` | memory/sqlite selection |

## ⚠️ PARTIALLY IMPLEMENTED

| Item | What's Done | What's Missing |
|------|-----------|---------------|
| **Retry scheduling** | `TaskStatusRetryPending` state + valid transitions exist | No retry execution in `runDAG`. Failed tasks go terminal. No backoff, max-retries, or retry scheduler. |
| **Durable restart/resume** | `Recover()` detects interrupted runs, marks `WasInterrupted` | Recovery explicitly refuses to resume running tasks. No checkpoint-based resume. Non-terminal joins fail closed. |
| **TUI/live dashboard** | Event forwarding works for subagent heartbeats | No dedicated TUI panel for active runs, task DAGs, cancellation controls |
| **Storage backend as contract oracle** | Both Memory and SQLite implement same interface | No formal equivalence test proving both backends produce identical results under identical event sequences |
| **Stale-attempt fencing** | `CompareAndSetTaskStatus` version check provides mechanism | No explicit test proving expired/stale attempt cannot publish terminal result after cancellation |

## ❌ NOT IMPLEMENTED

| Item | Priority | Notes |
|------|----------|-------|
| Retry backoff/scheduling execution | MEDIUM | Would go in `internal/coordinator/retry.go` |
| Auto-retry of failed/timed_out tasks | MEDIUM | `runDAG` never retries |
| Crash-recovery tests (kill/restart) | MEDIUM | Plan required proving which tasks resume/skip/retry/block |
| Coordinator as interface (not concrete struct) | LOW | Callers depend on concrete type |
| Retention/deletion policy config | LOW | Currently hardcoded 10min handle retention |
| OpenTelemetry export | LOW | Explicitly deferred — not needed for sync bus |
| Multi-process durability | LOW | Explicit non-goal for in-process scope |

## Verification

```
go build ./...       → PASS
go vet ./...         → PASS
go test -race ./...  → PASS (18/18 packages, 0 failures)
```
