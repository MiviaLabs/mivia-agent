# Phase 2 Complete + Status/Stalled Fixes

**Date:** 2025-07-27

## Phase 2: EventBus Infrastructure

### New Package: `internal/events/`

Four files implementing a synchronous in-process event bus:

| File | Contents |
|------|----------|
| `event.go` | `Kind` type (string) with 17 constants, `Event` struct with full metadata fields, `NewEvent()` constructor |
| `handler.go` | `Handler` interface (`HandleEvent(ctx, Event)`), `HandlerFunc` adapter |
| `bus.go` | `Bus` struct with mutex-protected `map[Kind][]Handler`, methods: `New()`, `Publish()`, `Subscribe()`, `SubscribeMany()`, `Unsubscribe()`, `Close()` (idempotent via `sync.Once`) |
| `bus_test.go` | **13 tests** written FIRST (TDD RED phase) covering all contracts |

### Design Decisions
- **Synchronous** — `Publish()` calls handlers inline in the publisher's goroutine. Adapters buffer internally for async.
- **Read lock on Publish** — handlers run under RLock, so multiple publishers don't contend on writes.
- **No import cycles** — package does NOT import `agent`, `cli`, or `chat`.

### Test Coverage (13 tests)

| Test | What it verifies |
|------|-----------------|
| `TestBusPublishDeliversToSubscriber` | Core contract: subscribe → publish → receive |
| `TestBusMultipleSubscribers` | Two handlers on same kind both receive |
| `TestBusKindFiltering` | Event not delivered to wrong kind subscriber |
| `TestBusUnsubscribe` | Handler removed after Unsubscribe |
| `TestBusCloseMultipleTimes` | Close() idempotent (sync.Once) |
| `TestBusSubscribeMany` | SubscribeMany to 3 kinds works |
| `TestHandlerFuncAdapter` | HandlerFunc adapter works |
| `TestEventConstruction` | NewEvent sets Kind + non-zero Timestamp |
| `TestBusPublishConcurrentSafe` | 8 goroutines × 100 events, no data loss |
| `TestBusSubscribeConcurrentSafe` | Concurrent subscribe+unsubscribe while publishing, no races |
| `TestUnsubscribeNonexistent` | No panic on unsubscribing never-registered handler |
| `TestSubscribeNilHandler` | No panic on subscribing nil |
| `TestBusPublishEmptyBus` | No panic publishing with zero subscribers |

## Status & Stalled Warning Fixes

### Stalled Warning Fix
**Problem:** The stalled warning (`⚠ stalled` in the composer border) was triggered during normal model thinking — whenever no data arrived for 5+ seconds after turn start.

**Fix:** Stalled warning now only triggers when the model has already produced **some** data (stream tokens, thinking content, or tool rows) and then gone quiet for 5+ seconds. The initial wait (no data yet) correctly shows "thinking" or "awaiting" instead.

Changed in `tui_layout.go` `updateFromDrain()`:
- Added `hasData := m.streamBuf.Len() > 0 || m.thinkingBuf.Len() > 0 || len(m.toolRows) > 0`
- Stalled warning only fires when `hasData && elapsed > 5s`

### Better Status Differentiation
Added `phaseAwaiting` brand phase — shown during the first ~2 seconds after sending a message, before any data arrives. This gives users clear progression:

| Phase | Label | When |
|-------|-------|------|
| `phaseAwaiting` | "awaiting" | First ~2s, waiting for first response |
| `phaseThinking` | "thinking" | 2s+, no data yet — model reasoning |
| `phaseStreaming` | "streaming" | Tokens flowing |
| `phaseTools` / `phaseMulti` | "tools" / "parallel" | Tool calls in progress |

Changes across `brand.go`:
- Added `phaseAwaiting` constant with cyan color, "awaiting" label, and gentle pulse animation
- Updated `deriveBrandPhase()` to accept `elapsed time.Duration` and return `phaseAwaiting` for `elapsed < 2s` with no data
- Updated `brandColor()`, `brandLabel()`, `navAnims` map

### "25 tools calls" notes
This is expected behavior — parallel tool dispatch with many simultaneous calls is legitimate. Before Phase 1, tool rows were invisible during streaming. Now they appear live via the tool panel in `renderStreamVP()`. The count via `countTools()` is correct.

### Build & Test
- `go build ./cmd/mivia/` — ✅
- `go test ./...` — ✅ (zero failures)
- `go test -race ./internal/cli/... ./internal/events/...` — ✅ (no data races)
- `go vet ./...` — ✅
