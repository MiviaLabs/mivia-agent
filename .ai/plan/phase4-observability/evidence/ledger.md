# Phase 4: Observability — Evidence Ledger

## Existing State

### EventBus (internal/events/)
- `bus.go` — Bus struct with Subscribe/Publish/Unsubscribe/Close. Synchronous, in-process.
- `event.go` — 17 event Kind constants. Event struct with Kind, Timestamp, SessionID, TurnID, etc.
- `handler.go` — Handler interface (`HandleEvent(ctx, Event)`), HandlerFunc adapter.
- `bus_test.go` — 13 tests covering all bus contracts.
- `adapter.go` — NewEventFromAgentParts converter.
- No metrics adapter exists.

### TUI Wiring (internal/cli/)
- `tui_run.go` — `runTUI()` creates `events.New()` Bus at line 32, assigns to `model.eventBus` and `sess.EventBus`, creates `UIAdapter`, calls `SetGlobalBus(bus)`. Bus is closed at end.
- `tui.go` — `tuiModel` has `eventBus *events.Bus` field. Used in `applyEvent()` for step/error/turn-end events. Bridge drain is the primary content source.
- `tui_events.go` — `applyEvent()` handles KindStep, KindSubagentHeartbeat, KindError, KindTurnEnd from bus.
- No diagnostics exist.

### Storage (internal/storage/)
- `store_bench_test.go` — Has `BenchmarkSQLiteLogicalAgents`. Uses concurrent append benchmark.
- No new benchmarks needed for storage — Phase 4 wants ledger benchmarks.

### Ledger (internal/ledger/)
- `storage.go` — StorageLedgerRepository exists (Phase 3).
- `memory.go` — MemoryLedgerRepository exists.
- No benchmarks exist for either.

### Orchestrate (internal/cli/)
- `orchestrate.go` — Coordinator singleton, initCoordinator, runThroughCoordinator.
- `orchestrate_lifecycle.go` — spawn_agent, inspect_agents, join_run, cancel_run tools.
- No diagnostics tool.
