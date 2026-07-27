# Phase 3 Complete: Wire Agent Loop → EventBus

**Date:** 2025-07-27

## Summary

Phase 3 wires the EventBus (Phase 2) into the agent loop, TUI, and subagent progress system. Events are now delivered to both the legacy `OnEvent` callback and the EventBus, enabling extensible delivery without breaking existing behaviour.

## Changes

### New Files

| File | Purpose |
|------|---------|
| `internal/events/adapter.go` | `NewEventFromAgentParts()` — converts agent.Event fields to events.Event without importing agent package |
| `internal/agent/emit.go` | `emit()` function — delivers events to both `OnEvent` callback and `EventBus`, replaces all direct `opts.OnEvent()` calls |
| `internal/cli/ui_adapter.go` | `UIAdapter` — subscribes to EventBus, forwards events via buffered channel to Bubble Tea via `PollCmd()` |

### Modified Files

| File | Change |
|------|--------|
| `internal/agent/loop.go` | Added `EventBus *events.Bus` to `Options`. All 6 `opts.OnEvent()` call sites replaced with `emit()` |
| `internal/agent/loop_tools.go` | All 4 `opts.OnEvent()` call sites (tool parallel, tool start, tool batch heartbeat, tool end) replaced with `emit()` |
| `internal/cli/tui.go` | Added `eventBus` and `uiAdapter` fields to `tuiModel`. `runTUI()` creates EventBus + UIAdapter. `startAI()` publishes `KindTurnStart`/`KindTurnEnd` |
| `internal/cli/subagent_progress.go` | Added `globalBus *events.Bus` package var, `SetGlobalBus()` setter. `emitSubagentProgress()` now also publishes to EventBus alongside legacy callback |

### Test Files

| File | Tests |
|------|-------|
| `internal/events/adapter_test.go` | `TestNewEventFromAgentParts`, `TestNewEventFromAgentParts_AllKinds` (2 tests) |
| `internal/cli/ui_adapter_test.go` | `TestUIAdapterPollCmdReturnsMsg`, `TestUIAdapterPollCmdSelfPerpetuates`, `TestUIAdapterHandlesEvent`, `TestUIAdapterMultipleEvents`, `TestUIAdapterDropsOnFullChannel`, `TestUIAdapterHandleEventNonBlocking` (6 tests) |
| `internal/agent/loop_test.go` | `TestLoopPublishesToEventBus` — end-to-end test verifying EventBus delivery from loop |

## Design Decisions

### `emit()` helper
Rather than duplicating the `if OnEvent { ... } if EventBus { ... }` pattern at every call site, a single `emit(opts, e)` function handles dual delivery. This makes it impossible to forget EventBus when adding new event emissions.

### No import cycles
- `events` package does NOT import `agent`, `cli`, or `chat`
- `agent` imports `events` for the `EventBus` field type
- `cli` imports both `agent` and `events` (bridge layer)

### Backward compat preserved
- All legacy `OnEvent` callbacks continue to work unchanged
- `streamBridge` drain in KeyMsg/MouseMsg fallthrough paths still active
- `SetSubagentProgress` global still functional
- All 15 existing event bus tests + 2 adapter tests + 6 UIAdapter tests + 1 loop test pass

## DOD Gate

- [x] TDD followed (tests written first → RED → implement → GREEN)
- [x] `go build ./cmd/mivia/` succeeds
- [x] `go test ./...` passes (zero failures)
- [x] `go test -race ./...` passes (no data races)
- [x] `go vet ./...` is clean
- [x] `FromAgentEventParts()` handles all 9 agent.EventKind values
- [x] Package `events` does NOT import agent, cli, or chat
- [x] No import cycles (`go vet ./...` already verified)
- [x] `UIAdapter.PollCmd()` returns a non-nil tea.Cmd that always produces a message
- [x] Agent loop fires BOTH `OnEvent` AND `EventBus.Publish()`
- [x] `startAI()` publishes `KindTurnStart`/`KindTurnEnd` to the bus
- [x] `emitSubagentProgress()` publishes to EventBus alongside legacy callback
