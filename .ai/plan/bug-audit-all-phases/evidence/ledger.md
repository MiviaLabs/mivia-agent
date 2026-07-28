# Bug Audit: All Phases (0-4) — Evidence Ledger

## Scope
Comprehensive hostile code audit across ALL code written in Phases 0-4.

## Packages and Files to Audit

### Phase 0: Contracts & Types
- internal/ledger/repository.go — LedgerRepository interface
- internal/ledger/types.go — RunSnapshot, TaskSnapshot, types
- internal/ledger/transition.go — State transition validation
- internal/ledger/displayname.go — DisplayNameGenerator

### Phase 1: Ledger Repository + Coordinator Seams
- internal/ledger/memory.go — MemoryLedgerRepository
- internal/ledger/ledger_test.go — Tests
- internal/coordinator/coordinator.go — Coordinator
- internal/coordinator/cancel.go — Cancel logic
- internal/coordinator/handle_lifecycle.go — Handle lifecycle
- internal/coordinator/record_results.go — Result recording
- internal/coordinator/recovery.go — Recovery logic
- internal/coordinator/validation.go — Validation
- internal/coordinator/coordinator_test.go — Tests
- internal/coordinator/integration_test.go — Integration tests

### Phase 2: Orchestration Tools
- internal/cli/orchestrate.go — initCoordinator, runThroughCoordinator
- internal/cli/orchestrate_lifecycle.go — spawn_agent, join_run, etc.
- internal/cli/orchestrate_lifecycle_test.go — Tests

### Phase 3: Durable Persistence
- internal/ledger/storage.go — StorageLedgerRepository
- internal/ledger/storage_schema.go — Schema + RebuildProjection
- internal/ledger/storage_test.go — Tests
- internal/storage/store.go — Store interface + Memory + SQLite

### Phase 4: Observability
- internal/events/metrics.go — MetricsAdapter
- internal/events/metrics_test.go — Tests
- internal/cli/diagnostics.go — Diagnostics
- internal/cli/diagnostics_test.go — Tests
- internal/cli/tui_run.go — Wiring
- internal/ledger/bench_test.go — Benchmarks

### Subagents (cross-cutting)
- internal/subagents/subagents.go — Pool
- internal/subagents/multi_step.go — Multi-step execution
- internal/subagents/oneshot.go — One-shot execution
