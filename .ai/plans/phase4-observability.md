# Phase 4: Observability — Implementation Plan

**Status:** Draft implementation plan
**Date:** 2026-07-28
**Depends on:** Phase 3 (durable persistence) in `.ai/plans/phase3-durable-persistence.md`
**Parent:** `.ai/plans/orchestration-completion.md`

---

## 1. Current State

```
Completed:
  ✅ EventBus — 17 event kinds, sync in-process bus
  ✅ UIAdapter — bridges EventBus to Bubble Tea via PollCmd
  ✅ Agent loop → EventBus — emit() delivers events

Gaps (Phase 4 — this plan):
  ❌ Metrics adapter for events.Bus — no counting/timing per event kind
  ❌ Operator diagnostics — no run listing, task status summary, active handles views
  ❌ Benchmarks — no ledger write latency, projection rebuild, or pool throughput benchmarks
```

## 2. Implementation Steps

### Step 1: Metrics Adapter for EventBus

**File:** `internal/events/metrics.go`

Create a `MetricsAdapter` that wraps or subscribes to the `events.Bus` and counts/times events by kind.

```go
package events

import (
    "sync/atomic"
    "time"
)

// MetricsAdapter collects per-kind counts and timing for EventBus events.
// It implements events.Handler and can be subscribed to any Bus.
type MetricsAdapter struct {
    counts   map[Kind]*atomic.Uint64
    timing   map[Kind]*timingAccumulator
    // ...
}

// NewMetricsAdapter creates a MetricsAdapter and subscribes it to the given bus.
func NewMetricsAdapter(bus *Bus) *MetricsAdapter
```

**Metrics collected:**

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `event_total` | Counter | `kind` | Total events published per kind |
| `event_bytes_total` | Counter | `kind` | Total content bytes per kind |
| `event_process_duration` | Histogram (ms) | `kind` | Handler processing time |
| `active_runs` | Gauge | — | Currently active (non-terminal) runs |

**Integration:**
- Created in `runTUI()` alongside EventBus
- Exposed via a `Diagnostics` struct accessible from CLI commands or TUI debug panel

### Step 2: Operator Diagnostics

**File:** `internal/cli/diagnostics.go` (new) or extend `internal/cli/orchestrate.go`

Provide operator visibility into orchestration state:

```go
// Diagnostics exposes bounded, privacy-safe operator views of the
// orchestration runtime, sourced from the ledger repository.
type Diagnostics struct {
    repo ledger.LedgerRepository
    bus  *events.Bus
    // ...
}

// RunSummary is a bounded summary of an orchestration run.
type RunSummary struct {
    RunID       string    `json:"run_id"`
    DisplayName string    `json:"display_name"`
    Status      string    `json:"status"`
    TaskCount   int       `json:"task_count"`
    CreatedAt   time.Time `json:"created_at"`
    Elapsed     string    `json:"elapsed"`
}

// ListRuns returns a bounded list of recent runs.
func (d *Diagnostics) ListRuns(ctx context.Context, limit int) ([]RunSummary, error)

// ActiveHandles returns count of currently active (non-terminal) handles.
func (d *Diagnostics) ActiveHandles() int

// MetricsSnapshot returns the current metrics counters.
func (d *Diagnostics) MetricsSnapshot() map[string]uint64
```

**Exposure paths:**
1. **TUI status bar** — Add an orchestration health indicator (active runs count) to the TUI composer border
2. **CLI command** — `mivia diagnostics` or `mivia runs list` subcommand (future)
3. **Debug endpoint** — Accessible through diagnostics tool for the model when debugging

### Step 3: Benchmarks

**File:** `internal/ledger/bench_test.go` (or extend `internal/storage/store_bench_test.go`)

| Benchmark | What it measures |
|-----------|-----------------|
| `BenchmarkMemoryLedger_CreateRun` | MemoryLedgerRepository.CreateRun latency |
| `BenchmarkSQLiteLedger_CreateRun` | StorageLedgerRepository (SQLite) CreateRun latency |
| `BenchmarkMemoryLedger_TaskLifecycle` | Full task lifecycle (create → run → complete) |
| `BenchmarkSQLiteLedger_TaskLifecycle` | Same on SQLite backend |
| `BenchmarkProjectionRebuild/10tasks` | Rebuild from 10 stored events |
| `BenchmarkProjectionRebuild/1000tasks` | Rebuild from 1000 stored events |
| `BenchmarkPoolThroughput/1worker` | Pool throughput with 1 worker |
| `BenchmarkPoolThroughput/8workers` | Pool throughput with 8 workers |
| `BenchmarkEventBusPublish` | EventBus publish latency per event kind |

**Design:**
- Use `testing.B` standard Go benchmarks
- SQLite benchmarks use `:memory:` path
- Report ns/op and allocs/op
- For pool throughput, measure tasks/second and memory per task

### Step 4: Wire Everything Together

**In `runTUI()` or `main.go`:**
1. Create `MetricsAdapter` and subscribe to EventBus
2. Create `Diagnostics` instance with repository + bus references
3. Optionally expose via TUI (extend model state) or CLI flag

## 3. Files to Create

| File | Purpose |
|------|---------|
| `internal/events/metrics.go` | MetricsAdapter implementation |
| `internal/events/metrics_test.go` | Tests for metrics collection |
| `internal/cli/diagnostics.go` | Operator diagnostics views |
| `internal/cli/diagnostics_test.go` | Tests for diagnostics |

## 4. Files to Modify

| File | Change |
|------|--------|
| `internal/cli/tui.go` | Wire MetricsAdapter, add active-runs indicator |
| `internal/cli/tui_layout.go` | Optional: show orchestration health in border |
| `internal/ledger/bench_test.go` | Add persistent-ledger benchmarks |

## 5. Dependencies

- **Phase 3 must be complete first** — diagnostics reads from `LedgerRepository`, and meaningful benchmarks need the `StorageLedgerRepository` implementation
- SQLite path resolution depends on config changes from Phase 3

## 6. Risk Assessment

| Risk | Likelihood | Mitigation |
|------|-----------|------------|
| Metrics add overhead to hot path | Low | Atomic counters only; no allocations in handler |
| PII exposure in diagnostics | Low | Diagnostics uses bounded refs, never raw I/O |
| Benchmark variance from SQLite WAL | Medium | Run benchmarks with `-benchtime=5s` for stable averages |
