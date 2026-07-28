# Plan: phase4-observability (v3 — locked)
Template-Version: v1

## Goal
Implement EventBus metrics, operator diagnostics views, and ledger benchmarks.

## Scope
- **In scope**: MetricsAdapter for events.Bus (counts + timing), Diagnostics struct for operator views, benchmarks for ledger implementations, wiring into runTUI.
- **Out of scope**: CLI commands for diagnostics (future), OpenTelemetry export, PII in metrics, replacing the bridge drain with EventBus, modifying Bus interface.
- **Boundary**: `internal/events/`, `internal/cli/`, `internal/ledger/` only.

## Files to Create
- `internal/events/metrics.go` — MetricsAdapter: counts, timing, Subscribe, Close
- `internal/events/metrics_test.go` — Tests for MetricsAdapter  
- `internal/cli/diagnostics.go` — Diagnostics: ListRuns, ActiveHandles, MetricsSnapshot
- `internal/cli/diagnostics_test.go` — Tests for Diagnostics
- `internal/ledger/bench_test.go` — Benchmarks for MemoryLedgerRepository and StorageLedgerRepository

## Files to Modify
- `internal/cli/tui_run.go` — After bus creation, subscribe MetricsAdapter to bus (once). Create Diagnostics with both repo + adapter.

## API Surface

```go
// Package events

// MetricsAdapter collects per-kind event counts and handler timing.
// Implements events.Handler. Safe for concurrent use.
type MetricsAdapter struct { /* unexported: mu, counts map[Kind]*counter, bus *Bus, subscribedKinds []Kind */ }

// NewMetricsAdapter creates a MetricsAdapter with empty counters. Does NOT subscribe.
func NewMetricsAdapter() *MetricsAdapter

// HandleEvent implements events.Handler. Increments per-kind counter, accumulates
// handler processing time. Recovers from panics to avoid crashing the publisher.
func (m *MetricsAdapter) HandleEvent(ctx context.Context, ev Event)

// Subscribe subscribes the adapter to ALL 17 known event kinds on the given Bus.
// Idempotent — second call is no-op. Stores kind list for Close to unsubscribe.
func (m *MetricsAdapter) Subscribe(bus *Bus)

// Snapshot returns per-kind event counts, total event count, and total elapsed
// handler time across all events. Returns a new map each call — safe to iterate.
func (m *MetricsAdapter) Snapshot() (counts map[string]uint64, totalEvents uint64, totalElapsed time.Duration)

// Reset zeros all counters. Does NOT unsubscribe.
func (m *MetricsAdapter) Reset()

// Close unsubscribes from the bus (all previously subscribed kinds) and resets counters.
// Idempotent. Safe to call if bus was already closed.
func (m *MetricsAdapter) Close()
```

```go
// Package cli

// Diagnostics exposes bounded, privacy-safe operator views of the orchestration runtime.
type Diagnostics struct { /* unexported: repo ledger.LedgerRepository, adapter *events.MetricsAdapter */ }

// NewDiagnostics creates a Diagnostics backed by the given ledger repo and metrics adapter.
// If repo is nil, ListRuns and ActiveHandles return zero values. If adapter is nil,
// MetricsSnapshot returns zero values. Does not panic.
func NewDiagnostics(repo ledger.LedgerRepository, adapter *events.MetricsAdapter) *Diagnostics

// RunSummary is a bounded summary of an orchestration run.
type RunSummary struct {
    RunID       string    `json:"run_id"`
    DisplayName string    `json:"display_name"`
    Status      string    `json:"status"`
    TaskCount   int       `json:"task_count"`
    CreatedAt   time.Time `json:"created_at"`
    Elapsed     string    `json:"elapsed"`
}

// ListRuns returns runs from the ledger repository, ordered by creation (newest first).
// limit caps the response. limit <= 0 returns all runs.
func (d *Diagnostics) ListRuns(ctx context.Context, limit int) ([]RunSummary, error)

// ActiveHandles returns count of non-terminal runs by querying the repo for
// running + queued + created status runs. Returns 0 if no repo configured.
func (d *Diagnostics) ActiveHandles() int

// MetricsSnapshot returns current counts from MetricsAdapter via Snapshot().
// Returns zero values if no adapter configured.
func (d *Diagnostics) MetricsSnapshot() (counts map[string]uint64, totalEvents uint64, totalElapsed time.Duration)
```

## Dependency Graph

```
Wave 1: [t1a, t1b]    — MetricsAdapter. t1a writes test file (save as pending text).
                         t1b writes minimal type skeleton (struct + constructor + stub methods).
                         Then t1a's test compiles and fails on assertion. (Same wave, sequential)
Wave 2: [t2]           — MetricsAdapter full implementation (HandleEvent, Subscribe, Snapshot, Reset, Close)
Wave 3: [t3a, t3b]    — Diagnostics. Same pattern: t3a writes test, t3b writes skeleton.
Wave 4: [t4, t5]       — Benchmarks (t4) + review (t5)
Wave 5: [t6]           — Wire into runTUI (depends on t2 for MetricsAdapter, t3b for Diagnostics)
Wave 6: [t7]           — Integration review + verify
```

**Key design change from v2**: Waves 1 and 3 each have a RED test + skeleton pair in the same wave. The skeleton provides just enough type information to make the test compile but NOT enough to make assertions pass. This satisfies the ADLC RED requirement (compile + assertion failure).

## Test Strategy
| Test Name | Type | Scenario | Expected RED Failure |
|-----------|------|----------|---------------------|
| TestMetricsAdapter_CountsEvents | unit | Subscribe, publish 3, verify count = 3 | got != 3 |
| TestMetricsAdapter_MultipleKinds | unit | Publish 2 kinds, verify per-kind counts | kind count mismatch |
| TestMetricsAdapter_SnapshotConsistency | unit | Publish, Snapshot, publish more, verify frozen | first snapshot changed |
| TestMetricsAdapter_Reset | unit | Publish, Reset, verify zero | not zero |
| TestMetricsAdapter_ConcurrentSafe | unit | 10 goroutines publishing, no data loss | count != total |
| TestMetricsAdapter_Timing | unit | Publish with delay, verify elapsed > 0 | elapsed == 0 |
| TestMetricsAdapter_Close | unit | Close, verify Snapshot returns zero | not zero |
| TestMetricsAdapter_SubscribeIdempotent | unit | Subscribe twice, publish once, count = 1 | count != 1 |
| TestMetricsAdapter_PanicSafety | unit | HandleEvent panics, verify recover doesn't crash | test panics |
| TestMetricsAdapter_CloseAfterBusClose | unit | Close bus, then Close adapter, verify no panic | panic |
| TestDiagnostics_ListRuns | unit | Create runs in repo, list, verify returned | len != N |
| TestDiagnostics_ActiveHandles | unit | Create running+completed, verify count=1 | count != 1 |
| TestDiagnostics_ActiveHandlesQueriesRepo | unit | Verify ActiveHandles calls ListRuns with correct statuses | wrong status filter |
| TestDiagnostics_MetricsSnapshot | unit | Create adapter, publish, snapshot via diag, verify non-zero | snap == zero |
| TestDiagnostics_NilRepo | unit | NewDiagnostics(nil, nil), methods return zero values | not zero |
| BenchmarkMemoryLedger_CreateRun | bench | MemoryLedgerRepository.CreateRun latency | N/A |
| BenchmarkMemoryLedger_TaskLifecycle | bench | Full lifecycle on MemoryLedger | N/A |
| BenchmarkStorageLedger_CreateRun | bench | StorageLedgerRepository (SQLite) CreateRun latency | N/A |
| BenchmarkStorageLedger_TaskLifecycle | bench | Full lifecycle on StorageLedger | N/A |

## Plan Scorecard

| Criterion | Score | Notes |
|-----------|-------|-------|
| 1. All existing tests will still pass | PASS | New files only. tui_run.go change is additive. |
| 2. No new import cycles | PASS | events/metrics.go imports only events. cli/diagnostics.go imports ledger + events (both already imported in cli). |
| 3. No breaking changes to existing public API | PASS | All new types/functions. No existing API modified. |
| 4. New code is testable in isolation | PASS | MetricsAdapter takes no deps. Diagnostics takes interfaces. Benchmarks use :memory: SQLite. |
| 5. Config changes are backward-compatible | PASS | No config changes. |
| 6. Every new public function has ≥1 named test scenario | PASS | 15 test scenarios for all functions. |
| 7. Integration test path identified | PASS | Existing smoke tests cover runTUI path. MetricsAdapter is passive subscriber. |
| 8. No file touched by >1 wave | PASS | Events: waves 1-2. CLI/diagnostics: wave 3. Ledger/bench: wave 4. Tui_run.go: wave 5. Unique. |

## Rollback Criterion
If MetricsAdapter imposes measurable overhead on hot Publish path (>100ns per event), strip timing and use atomic counters only.

## Disposition Log
| # | Source | Finding | Severity | Verdict | Rationale |
|---|--------|---------|----------|---------|-----------|
| 1 | C1, C2 | RED test can't compile against non-existent API | HIGH | FIXED | Waves 1 and 3 restructured: each is a pair (test + skeleton) in same wave. Skeleton provides type info first so test compiles. |
| 2 | C2 | MetricsAdapter.Subscribe can't subscribe to ALL 17 kinds — Bus has no SubscribeAll | HIGH | FIXED | Subscribe hardcodes the 17 Kind constants from event.go (they're stable — defined in same package). Stores kind list for Close to unsubscribe. |
| 3 | C2 | Diagnostics.MetricsSnapshot has no MetricsAdapter reference | HIGH | FIXED | NewDiagnostics now takes `(repo, adapter *events.MetricsAdapter)`. MetricsSnapshot delegates to adapter.Snapshot(). |
| 4 | C2 | Close() can't unsubscribe — Bus has no bulk unsubscribe | MEDIUM | FIXED | Subscribe stores subscribed kinds. Close iterates them calling bus.Unsubscribe(kind, h). Protects against nil/closed bus. |
| 5 | C1 | ActiveHandles via repo ListRuns needs explicit status filter logic | MEDIUM | FIXED | Documented: ListRuns with RunStatusRunning, RunStatusQueued, RunStatusCreated. Sum counts. |
| 6 | C1, C2 | Wave 3 test+prod parallel — same RED compilation problem | MEDIUM | FIXED | Same fix as #1: t3a (test) + t3b (skeleton) in same sequential wave. |
| 7 | C2 | runTUI wiring underspecified | MEDIUM | FIXED | Specified: subscribe to bus once (model.eventBus == global bus). Create Diagnostics(repo, adapter). |
| 8 | C2 | Double-subscribe concern (model.eventBus vs global bus) | LOW | INFO | They're the same bus object. Subscribe once. |
| 9 | C1 | No integration test for StorageLedgerRepository + Diagnostics | LOW | DEFERRED | Low risk since Diagnostics delegates to interface. Can add later if issues arise. |
| 10 | C1 | Timing measures handler duration, not end-to-end | LOW | ACCEPTED | For sync bus, handler time == end-to-end. No change needed. |
