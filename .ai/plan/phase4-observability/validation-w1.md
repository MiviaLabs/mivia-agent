# Validation Report — Wave 1 (t1a & t1b)

## Q1: Can t1b write a minimal MetricsAdapter skeleton that makes t1a's test COMPILE?

**PASS** — Yes, a minimal skeleton is sufficient.

### What the test references
From the API Surface in `plan.md`, t1a's test will call:
- `events.NewMetricsAdapter()` → returns `*MetricsAdapter`
- `adapter.HandleEvent(ctx, ev)` → must implement `events.Handler`
- `adapter.Snapshot()` → returns `(map[string]uint64, uint64, time.Duration)`
- `adapter.Subscribe(bus)` → takes `*events.Bus`
- `adapter.Reset()`
- `adapter.Close()`

### Why it works
1. **`HandleEvent(ctx context.Context, ev Event)`** has the exact signature required by the `Handler` interface defined in `handler.go:10` — so a stub body is enough to satisfy the interface.
2. All other methods return zero values:
   - `Snapshot()` → `return nil, 0, 0`
   - All others → empty bodies or `return nil`/`return 0`
3. The constructor `NewMetricsAdapter()` returns `&MetricsAdapter{}` — no fields needed.
4. `Subscribe(bus *Bus)` accepts a `*Bus` pointer, which is the type produced by `events.New()`.

### Minimal skeleton needed
```go
package events

import (
    "context"
    "time"
)

type MetricsAdapter struct{}

func NewMetricsAdapter() *MetricsAdapter { return &MetricsAdapter{} }

func (m *MetricsAdapter) HandleEvent(ctx context.Context, ev Event) {}

func (m *MetricsAdapter) Subscribe(bus *Bus) {}

func (m *MetricsAdapter) Snapshot() (map[string]uint64, uint64, time.Duration) {
    return nil, 0, 0
}

func (m *MetricsAdapter) Reset() {}

func (m *MetricsAdapter) Close() {}
```

This provides all required types and method signatures for the test to compile.

---

## Q2: Will `go build ./internal/events/...` succeed after t1b?

**PASS** — Yes, it will succeed.

### Analysis
- `metrics.go` is in the same `package events` — it has full access to `Event`, `Kind`, `Bus`, `Handler` without importing anything.
- The `events` package imports only stdlib packages: `context` (from `handler.go`), `time` (from `event.go` and `adapter.go`), and `sync` (from `bus.go`).
- No internal imports exist in the package — confirmed by grep.
- The skeleton methods return only zero values / have empty bodies, so no logic errors can cause build failures.
- A `MetricsAdapter` struct with empty fields is a valid Go type.

---

## Q3: Will t1a's test FAIL with assertion errors (not compile errors) after t1b?

**PASS** — Yes, the test will compile and fail on *assertion* errors.

### Why
1. After t1b provides the type skeleton, the test file compiles successfully (all types and methods exist).
2. All stub methods return zero values: `Snapshot()` returns `nil, 0, 0`; `HandleEvent` is a no-op.
3. The test assertions (e.g. "count == 3", "kind count mismatch", "elapsed > 0") will fail because the actual values are always zero/nil.
4. This is the intended RED state per the plan: "compiles + assertion failure."

### Concrete example of expected failure
If `TestMetricsAdapter_CountsEvents` publishes 3 events and asserts `count == 3`, the stub's `Snapshot()` returns 0, so `assert.Equal(t, 3, count)` fails with:
```
Expected: 3
Actual  : 0
```

---

## Q4: Is there any import cycle risk?

**PASS** — No import cycle risk whatsoever.

### Evidence
- `grep` of `"internal/"` in `internal/events/*` returned **zero matches** — the package imports **only stdlib** (`context`, `time`, `sync`).
- `metrics.go` is in the same `events` package, so it can reference `Bus`, `Handler`, `Event`, `Kind` directly — no cross-package imports needed.
- The package graph is acyclic:
  ```
  internal/events → (stdlib only; no internal deps)
  ```
- No new dependency edges are introduced by `metrics.go`.

---

## Summary

| Question | Verdict | Rationale |
|----------|---------|-----------|
| Q1: Skeleton sufficient for compilation | **PASS** | All method signatures match the test's expectations; Handler interface satisfied by stub. |
| Q2: `go build ./internal/events/...` succeeds | **PASS** | Package has no internal imports; skeleton produces valid Go. |
| Q3: Test fails on assertion (not compile) | **PASS** | Stubs return zero values; assertions expecting non-zero values will fail. |
| Q4: Import cycle risk | **PASS** | `internal/events` imports stdlib only; no new edges added. |
