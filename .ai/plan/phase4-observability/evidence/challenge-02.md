# Challenge Report: Phase 4 Observability Plan

**Reviewer stance:** HOSTILE  
**Date:** 2025-07-29  
**Plan version:** v2 (in `.ai/plan/phase4-observability/plan.md`)

---

## Finding 1: Wave 1 RED Tests Cannot Compile (HIGH)

**What the plan says:**  
Wave 1 (`t1`) writes `metrics_test.go` as a RED test against `MetricsAdapter` — the test references `NewMetricsAdapter()`, `HandleEvent()`, `Snapshot()`, etc.

**Why it's wrong:**  
The ADLC rule for RED phase is: *tests must compile and fail an assertion*.  
In Go, referencing an undefined identifier (e.g., `events.NewMetricsAdapter`) is a **compilation error**, not a test failure. The compiler will reject the file with `undefined: NewMetricsAdapter`. The test binary never runs, no assertion fails, and the RED phase is violated.

The plan's Dependency Graph shows `Wave 1: [t1] — MetricsAdapter RED test` before `Wave 2: [t2] — MetricsAdapter GREEN implementation (depends on t1)`. This ordering is impossible — `t1` cannot compile until `t2` is at least partially implemented (the type declarations must exist).

**Required fix:** Either:
1. Merge Wave 1 and Wave 2 into a single wave where the type skeleton (struct + constructor) is written first, then tests are written against the skeleton and fail on assertions, or
2. Keep Wave 1 as "write compile-failing test" (but that violates the named ADLC rule), or
3. Rename to "RED-GREEN cycle within one wave" where the RED test is written, immediately followed by the minimal prod skeleton to make it compile, then the test is asserted to fail.

The plan currently pretends this isn't an issue.

---

## Finding 2: Wave 3 Tests Cannot Compile — Same RED Phase Violation (HIGH)

**What the plan says:**  
Wave 3 is `[t3, t4] — Diagnostics test + prod (parallel, test doesn't need prod first since it tests against interface)`.  
The table says test scenario `TestDiagnostics_ListRuns` references `NewDiagnostics(repo)`, which doesn't exist yet.

**Why it's wrong:**  
The rationale "tests against interface" is false comfort. `NewDiagnostics` is a constructor function in package `cli`, not an interface method. In Go, `cli.NewDiagnostics(repo)` can only compile if that function exists. The LedgerRepository interface being real doesn't help — the constructor itself is missing. The tests import the `cli` package (they're in a `_test.go` file) and call `cli.NewDiagnostics(...)`. If that function doesn't exist, compilation fails.

**Required fix:** Wave 3 must be split into two sub-waves: first the type skeleton (`Diagnostics` struct + `NewDiagnostics` constructor returning zero values), then the tests. Alternatively, merge the RED test with the minimal skeleton into one wave.

---

## Finding 3: `MetricsAdapter.Subscribe()` API Doesn't Match the Existing Bus API (HIGH)

**What the plan says:**
```go
func (m *MetricsAdapter) Subscribe(bus *Bus)
// Subscribe registers the adapter with the given Bus for ALL event kinds.
// Idempotent — safe to call multiple times (second call is no-op).
```

**Why it's wrong:**  
The existing `Bus` type only exposes:

```go
func (b *Bus) Subscribe(kind Kind, h Handler)     // subscribes to ONE kind
func (b *Bus) SubscribeMany(kinds []Kind, h Handler) // subscribes to a LIST of kinds
```

There is **no** `SubscribeAll(handler Handler)` method. There is **no** exported `AllKinds` slice or registry. `Kind` is just a `string` type alias — there is no enumeration to iterate over.

The plan's `MetricsAdapter.Subscribe(bus *Bus)` internally needs to subscribe to every possible event kind. But it **cannot know all possible kinds** because:
- `Kind` is `type Kind string` — any package can invent new kinds at any time.
- There is no `AllKinds()` function.
- There is no `SubscribeAll()` method on `Bus`.

To make this work, the plan would need to either:
1. Add a `SubscribeAll(Handler)` method to `Bus` (not specified in scope), or
2. Export an `AllKinds` variable (not specified), or
3. Hard-code a static list of kinds (fragile, misses any future kinds).

None of these are in the plan. The Subscribe method as specified **cannot be implemented** with the current Bus API.

---

## Finding 4: `MetricsAdapter.Close()` Cannot Unsubscribe from All Kinds (MEDIUM)

**What the plan says:**
```go
func (m *MetricsAdapter) Close()
// Close unsubscribes the adapter from the bus and resets counters.
```

**Why it's wrong:**  
Following from Finding 3, `Bus.Unsubscribe(kind Kind, target Handler)` requires a `Kind`. Since the adapter subscribed to "all" kinds, it needs to know every kind it subscribed to in order to unsubscribe. But the Subscribe method (as proposed) doesn't track individual kinds, and there's no way to enumerate them from the Bus.

Additionally, `Bus.Close()` sets `b.subs = nil`. If `MetricsAdapter.Close()` is called after `Bus.Close()`, calling `bus.Unsubscribe(...)` will panic with a nil map assignment. The plan mentions no guard for this.

---

## Finding 5: `Diagnostics.MetricsSnapshot()` Has No Way to Access `MetricsAdapter` (HIGH)

**What the plan says:**
```go
func NewDiagnostics(repo ledger.LedgerRepository) *Diagnostics
// ...
func (d *Diagnostics) MetricsSnapshot() (counts map[string]uint64, totalEvents uint64, totalElapsed time.Duration)
// MetricsSnapshot returns current counts from MetricsAdapter via Snapshot().
```

**Why it's wrong:**  
`NewDiagnostics` only accepts a `LedgerRepository`. It has no parameter for a `MetricsAdapter`. Yet `MetricsSnapshot()` is supposed to call `adapter.Snapshot()`. Where does the adapter reference come from?

The plan's API surface section never shows how `Diagnostics` gets a reference to `MetricsAdapter`. The options are:
- Add a `SetMetricsAdapter(*MetricsAdapter)` setter (not in plan).
- Add `adapter` to `NewDiagnostics` params (not in plan).
- Pass it in `MetricsSnapshot(adapter *MetricsAdapter)` (not in plan — signature differs).
- Use a global variable (not documented, and would break testability).

The plan describes a **non-functional method** — it references a type it cannot access.

---

## Finding 6: Wave 5 `tui_run.go` Changes Are Dangerously Underspecified (MEDIUM)

**What the plan says:**  
"After bus creation, subscribe MetricsAdapter to both model.eventBus and global bus. Create Diagnostics. No new model fields needed (metrics adapter is standalone subscriber; diagnostics is created as local var or can be stored if needed later)."

**Why it's wrong:**  

1. **Same-bus double-subscribe:** In `tui_run.go`, `model.eventBus`, `sess.EventBus`, and the bus passed to `SetGlobalBus(bus)` are **all the same `*events.Bus` object** (line 30-35 of tui_run.go). Subscribing to both "model.eventBus" and "global bus" subscribes to the same bus twice. The plan says "subscribe to both" which is either redundant (same bus) or confused about architecture.

2. **Where does Diagnostics get MetricsAdapter?** `NewDiagnostics(repo)` doesn't accept a MetricsAdapter, but MetricsSnapshot needs one. The plan hand-waves "created as local var or stored if needed later" without resolving this.

3. **No concrete code diff.** The plan should specify: which line after which, what variables to create, what the exact subscription calls look like. Without this, Wave 5 is guesswork.

4. **Import impact.** If `tui_run.go` needs to reference `events.NewMetricsAdapter`, it already imports `events`. If it references `cli.NewDiagnostics`, it's already in `cli` package. But if `Diagnostics` needs `ledger.LedgerRepository` — which concrete implementation should be passed? The plan doesn't say where the `LedgerRepository` instance comes from in the TUI context.

---

## Finding 7: `ActiveHandles()` Derives from Repo — Known Drift from Coordinator State (MEDIUM)

**What the plan says:**
```go
// ActiveHandles returns count of non-terminal runs (running + queued + created).
// Derived from LedgerRepository.ListRuns with status filter.
```

**Why it's wrong:**  
The coordinator maintains an in-memory handle map that is the **source of truth** for active runs. The ledger repository is a **persistence layer** that may lag behind:

- A run may be "running" in the coordinator but the repo still shows "created" (race between state transition and persistence).
- A run may exist in the coordinator's handle map but the repo `CreateRun` hasn't been called yet (or failed).
- A run may be canceled in the coordinator but the repo `CloseRun` hasn't persisted yet.

This means `ActiveHandles()` can return:
- **False negatives:** A run is actively running but repo still shows "created" so status filter misses it.
- **False positives:** A run was canceled but repo not yet updated.

The plan accepts this by saying "Derived from LedgerRepository" but doesn't:
1. Document this limitation in the API surface.
2. Consider adding a `ActiveHandles()` method to the coordinator interface (if one exists).
3. Add a test that verifies the sematics are acceptable.

For an operator diagnostic tool, publishing **incorrect** active-handle counts is worse than publishing none. The operator may take action based on wrong data.

---

## Finding 8: Benchmarks Lack a Verification Step (LOW)

**What the plan says:**  
Wave 4 Benchmarks: "compiles and runs" but N/A for RED phase.

**Why it's wrong:**  
Benchmarks are listed in the Test Strategy table but marked `N/A` for expected RED failure. The plan says "compiles and runs" as verification but:

1. Doesn't specify **which** benchmark function to run. The bench file will have 4 benchmark functions. "Compiles and runs" is ambiguous — does the reviewer run `go test -bench=.` and verify all 4 execute without panic?
2. No assertion that benchmarks produce non-zero results (e.g., `BenchmarkMemoryLedger_CreateRun` should report at least 1 ns/op — a 0 ns/op result would indicate a dead-code-elimination bug).
3. No mention of `-benchtime` or `-count` flags needed for stable measurements.
4. No race-detection requirement (`-race` flag) for concurrent benchmarks.

The verification step needs to be: "Run `go test -bench=BenchmarkMemoryLedger -benchmem -count=3 ./internal/ledger/` and confirm all 4 benchmark functions execute, report >0 ns/op, and produce consistent results."

---

## Finding 9: MetricsAdapter.Subscribe to `model.eventBus` + Global Bus Is Redundant (LOW)

**What the plan says (Disposition Log #10):**  
"Global bus subscription needed" — "FIXED: runTUI subscribes to both model.eventBus and SetGlobalBus's bus."

**Why it's wrong:**  
Looking at `tui_run.go`:

```go
bus := events.New()
model.eventBus = bus
sess.EventBus = bus
model.uiAdapter = NewUIAdapter(bus, model.bridge)
SetGlobalBus(bus)
```

`SetGlobalBus(bus)` assigns the **same** `*events.Bus` pointer to a package-level variable. So `model.eventBus == globalBus` — they are literally the same bus. Subscribing MetricsAdapter to both is **double-subscribing to the same bus**, which means every event will be counted twice.

The disposition log entry #10 claims this was "FIXED" but the fix is incorrect — it introduces double-counting. The correct fix is either:
- Subscribe only to `model.eventBus` (the local reference).
- Or subscribe only via `SetGlobalBus` (the global reference).
- But not both.

---

## Finding 10: Metrics Uses `events.Bus` Concrete Type, Not Interface — Testability Impact (LOW)

**What the plan says:**
```go
func (m *MetricsAdapter) Subscribe(bus *Bus)
```

**Why it's wrong:**  
The method takes a concrete `*Bus` pointer rather than an interface. This means:

1. MetricsAdapter can only be tested with a real `*events.Bus`. No mock bus possible.
2. If future implementations of the bus concept appear, MetricsAdapter cannot be reused with them.
3. The `Subscribe` method cannot be independently unit-tested for edge cases like closed bus, nil bus, etc. without constructing a real Bus.

Alternative: define a `BusSubscriber` interface (e.g., `SubscribeAll(Handler)`) that the existing Bus implements. The plan doesn't consider this.

---

## Summary Table

| # | Finding | Severity | Category |
|---|---------|----------|----------|
| 1 | Wave 1 RED tests cannot compile (undefined: NewMetricsAdapter) | HIGH | TDD violation |
| 2 | Wave 3 RED tests cannot compile (undefined: NewDiagnostics) | HIGH | TDD violation |
| 3 | Subscribe() uses "all kinds" but Bus has no SubscribeAll() | HIGH | API mismatch |
| 4 | Close() can't unsubscribe from unknown kinds + nil-map panic risk | MEDIUM | Missing implementation |
| 5 | Diagnostics.MetricsSnapshot() has no MetricsAdapter reference | HIGH | Missing parameter |
| 6 | tui_run.go changes underspecified — double-subscribe, missing repo | MEDIUM | Missing detail |
| 7 | ActiveHandles() drifts from coordinator truth | MEDIUM | Design gap |
| 8 | Benchmarks lack precise verification criteria | LOW | Missing detail |
| 9 | "Subscribe to both" model.eventBus + global bus = double-count | LOW | Logical error |
| 10 | Subscribe takes concrete *Bus, not interface — hurts testability | LOW | Design choice |

## Verdict

**The plan has 4 HIGH-severity issues** (Findings 1, 2, 3, 5) that would cause the implementation to either fail to compile, violate the agreed development process, or produce non-functional code. These must be resolved before implementation begins. Findings 4, 6, 7 are MEDIUM issues that will cause subtle bugs or incomplete behavior. Findings 8-10 are LOW design concerns but should still be documented.

The plan's current API surface and dependency graph are **not implementable as written**.
