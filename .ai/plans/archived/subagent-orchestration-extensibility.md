# Subagent orchestration and live ledger

Status: ✅ All phases implemented and verified
Current phase: Phases 0-4 complete — durable, observable orchestration runtime
Last verified: 2026-07-29
Next action: Future work tracked in `.ai/plans/` (currently: cli-mvp, composer-autocomplete, tui-chat-ux)

## Objective

Refactor Mivia's subagent execution into an extensible orchestration boundary with one clear model-facing spawn tool, unique human-readable agent names, explicit parent/child identity, dependency-aware DAG execution, and a live queryable ledger that the root orchestrator can use to understand current application state.

✅ **All phases of this plan are now implemented and verified.**

---

## Implementation Status

### Phase 0 — Contract and architecture validation (✅ COMPLETE)
- `LedgerRepository` interface defined in `internal/ledger/repository.go`
- Coordinator, RunHandle, lifecycle types in `internal/coordinator/`
- Subagent Pool, multi-step, oneshot in `internal/subagents/`
- DisplayNameGenerator in `internal/ledger/displayname.go`
- State transitions in `internal/ledger/transition.go`

### Phase 1 — Ledger repository + coordinator seams (✅ COMPLETE)
- `MemoryLedgerRepository` in `internal/ledger/memory.go`
- Coordinator with Spawn/Join/Inspect/Cancel in `internal/coordinator/coordinator.go`
- Handle lifecycle, record_results, recovery, validation in `internal/coordinator/*.go`
- Full test coverage with race tests

### Phase 2 — Unified spawn and compatibility adapters (✅ COMPLETE)
- `spawn_agent` tool in `internal/cli/orchestrate.go` — supports one task and DAG, returns run handle
- `inspect_agents` tool for read-only status inspection
- `join_run` tool for blocking wait with result retrieval
- `cancel_run` tool for state-changing cancellation
- Legacy `delegate` and `dispatch_tasks` routed through the same coordinator
- Tool descriptions are project/language-generic

### Phase 3 — Durable persistence and recovery (✅ COMPLETE)
- `StorageLedgerRepository` wrapping `storage.Store` (SQLite) in `internal/ledger/storage.go`
- Event schema and `RebuildProjection` in `internal/ledger/storage_schema.go`
- Config option: `store_backend` (memory|sqlite) in `internal/config/types.go`
- Startup recovery: orphaned active tasks marked as interrupted_unrecoverable
- Kill/restart tests, crash recovery, duplicate prevention, projection rebuild tests

### Phase 4 — Observability and scale gates (✅ COMPLETE)
- `MetricsAdapter` in `internal/events/metrics.go` — per-kind counts and handler timing
- `Diagnostics` in `internal/cli/diagnostics.go` — ListRuns, ActiveHandles, MetricsSnapshot
- Benchmarks in `internal/ledger/bench_test.go` for both Memory and Storage ledger
- Wiring into TUI startup in `internal/cli/tui_run.go`

---

## Deviations from Original Plan

| Original Plan | What Actually Happened | Rationale |
|--------------|------------------------|-----------|
| Leases required for multi-process recovery | Deferred — not implemented | Strictly in-process scope confirmed |
| OpenTelemetry export for correlation | Not implemented | Sync bus makes timing = handler time; OTEL adds complexity without benefit |
| Native Mivia MCP contracts | Not implemented | Out of scope for current phase |
| Provider throughput benchmarks | Not implemented | Deferred to future observability work |
| QueuedWriter integration for persistence | Direct Store interface instead | Simpler, fewer moving parts |
| `allKnownKinds` hardcoded (17 kinds) | Implemented in metrics.go | Bus has no SubscribeAll; static list is stable |

---

## Verification

```
go build ./...       → PASS
go vet ./...         → PASS
go test -race ./...  → PASS (18/18 packages, 0 failures)
```
