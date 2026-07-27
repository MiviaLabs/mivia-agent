# Phase 1 Complete: Fix the Poll Chain

**Date:** 2025-07-27

## Summary

Phase 1 fixes the TUI refresh bug where the UI would not update streaming tokens, tool calls, or thinking blocks without user interaction (key/mouse input). The root cause was a broken poll chain: `pollCmd()` was not started from `Init()`, and when it did fire it only re-queued itself conditionally (when data was present), causing the chain to die during quiet periods.

## Changes

### `internal/cli/tui.go`
- **`Init()`** — Added `m.pollCmd()` to the `tea.Batch` returned by `Init()`, so the polling chain starts immediately on model creation.

### `internal/cli/tui_message.go`
- **Added `case tuiTickMsg:`** as the **first** case in the `updateMessageImpl` switch. This handler:
  - Drains the bridge (only in chat mode and when the bridge matches)
  - Calls `updateFromDrain()` to apply data
  - Calls `finishStream()` if the bridge signals done
  - **Always re-queues `pollCmd()`** before returning early
  - Returns early so the tick does not fall through to textarea/viewport updates or the foot drain block
- **Simplified foot drain block** — Replaced the bridge drain/apply/finish logic with a single `updateFromDrain()` call. Removed conditional `pollCmd()` re-queues (the tuiTickMsg handler handles continuous polling now). The foot path still catches data on user interaction (KeyMsg/MouseMsg) for responsiveness.

### `internal/cli/tui_layout.go`
- **Added `updateFromDrain()`** — New helper that consumes bridge drain data into model state: sets `stepDetail`, applies tool events, writes stream/thinking buffers, calls `renderStreamVP()`, and checks stalled warnings.
- **Added tool panel rendering to `renderStreamVP()`** — Calls `renderToolPanelWindow()` between `buildViewportContent()` and the stream buffer insertion, so live tool rows are visible in the viewport during streaming.

## Test Coverage

### New tests (`internal/cli/tui_phase1_test.go`) — 9 tests

| Test | What it verifies |
|------|-----------------|
| `TestInitReturnsPollCmd` | `Init()` returns a non-nil cmd (includes pollCmd in batch) |
| `TestTuiTickMsgAlwaysRequeuesPoll` | tuiTickMsg returns non-nil cmd (pollCmd re-queued) |
| `TestTuiTickMsgDoesNotDependOnData` | Empty bridge still re-queues pollCmd (invariant) |
| `TestTuiTickMsgDrainsBridge` | Stream, thinking, and tools are drained from bridge |
| `TestTuiTickMsgFinishStream` | `done=true` triggers finishStream (assistant block created) |
| `TestStreamVPIncludesToolPanel` | renderStreamVP includes tool panel content in viewport |
| `TestBridgeDrainNotDoubleProcessed` | KeyMsg after tuiTickMsg doesn't double-drain |
| `TestTuiTickMsgIgnoresStaleBridge` | Stale bridge not drained; pollCmd still re-queued |
| `TestTuiTickMsgWelcomeModeNoDrain` | Welcome mode skips drain; pollCmd still re-queued |

### Updated test (`internal/cli/tui_bridge_test.go`)
- `TestTUIIgnoresStaleBridgeTick` — Updated expectation to match Phase 1 contract (pollCmd always re-queued).

## DOD Gate

- [x] TDD was followed (tests first → RED → implement → GREEN)
- [x] `go build ./cmd/mivia/` succeeds
- [x] `go test ./...` passes (zero failures)
- [x] `go test -race ./...` passes (no data races)
- [x] `go vet ./...` is clean
- [x] `countToolsDone` does NOT appear in codebase
- [x] `case tuiTickMsg:` exists and always re-queues `pollCmd()`
- [x] `pollCmd()` is returned from `Init()`
- [x] `renderToolPanelWindow()` is called from `renderStreamVP()`
- [x] 9 new tests added (≥3 required)
- [x] All tests are deterministic (no `time.Sleep`, no global state races)

## Deviations from Plan

None. Implementation follows §8 of the events-eventbus-refactor-plan.md exactly.

## Next Steps

1. Build and run: `go build -o mivia ./cmd/mivia/ && ./mivia --model <model>`
2. Verify UI refreshes without user interaction during agent runs
3. Proceed to Phase 2 (EventBus infrastructure)
