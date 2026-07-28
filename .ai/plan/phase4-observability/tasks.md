# Tasks: phase4-observability

## Wave 1 — MetricsAdapter skeleton + RED test (sequential)
### t1a: MetricsAdapter RED test (saved as pending)
- **Wave**: 1
- **File**: `internal/events/metrics_test.go` (create)
- **Type**: test
- **Depends on**: none (written first, won't compile until t1b provides types)
- **Verification**: Save the test file. It will NOT compile yet (expected — types don't exist). Orchestrator will verify the test content manually before t1b starts.
- **Timeout**: 60s
- **Context scope**: `internal/events/handler.go`, `internal/events/event.go`, `internal/events/bus.go`

### t1b: MetricsAdapter type skeleton
- **Wave**: 1
- **File**: `internal/events/metrics.go` (create)
- **Type**: prod (skeleton only)
- **Depends on**: t1a
- **API**: MetricsAdapter struct (minimal — empty fields or placeholder), NewMetricsAdapter returning zero-value struct, HandleEvent doing nothing, Subscribe empty, Snapshot returning zero, Reset empty, Close empty
- **Verification**: After t1b, run `go build ./internal/events/...`. Then `go test -run TestMetricsAdapter_ ./internal/events/...` — must FAIL with assertion errors (NOT compile errors)
- **Timeout**: 60s
- **Context scope**: `internal/events/handler.go`, `internal/events/event.go`

## Wave 2 — MetricsAdapter full implementation
### t2: MetricsAdapter full implementation
- **Wave**: 2
- **File**: `internal/events/metrics.go` (modify — fill in all stubs)
- **Type**: prod
- **Depends on**: t1b
- **API**: Full implementations: HandleEvent with per-kind counter + timing + panic recover; Subscribe hardcoding 17 Kind constants + storing kind list; Snapshot returning counts+timing; Reset clearing maps; Close iterating subscribed kinds and calling bus.Unsubscribe + Reset
- **Verification**: `go test -run TestMetricsAdapter_ ./internal/events/...` — all PASS
- **Timeout**: 120s
- **Context scope**: `internal/events/metrics.go`, `internal/events/metrics_test.go`, `internal/events/event.go`, `internal/events/bus.go`

### t3: Review t1a + t1b + t2
- **Wave**: 2
- **File**: review of `internal/events/metrics.go` and `internal/events/metrics_test.go`
- **Type**: review
- **Depends on**: t2
- **Verification**: Read all three files. Verify: Subscribe hardcodes correct 17 kinds, HandleEvent recovers panic, Close unsubscribes correctly, Snapshot returns consistent data, tests cover all functions including CloseAfterBusClose
- **Timeout**: 60s
- **Context scope**: `internal/events/metrics.go`, `internal/events/metrics_test.go`

## Wave 3 — Diagnostics skeleton + RED test (sequential)
### t4a: Diagnostics RED test (saved as pending)
- **Wave**: 3
- **File**: `internal/cli/diagnostics_test.go` (create)
- **Type**: test
- **Depends on**: none
- **API**: Tests for NewDiagnostics(repo, adapter), ListRuns, ActiveHandles, MetricsSnapshot
- **Verification**: Save the test file. Won't compile yet — types don't exist.
- **Timeout**: 60s
- **Context scope**: `internal/ledger/repository.go`, `internal/ledger/types.go`, `internal/events/metrics.go`

### t4b: Diagnostics type skeleton
- **Wave**: 3
- **File**: `internal/cli/diagnostics.go` (create)
- **Type**: prod (skeleton)
- **Depends on**: t4a
- **API**: Diagnostics struct, NewDiagnostics returning zero-value struct, ListRuns returning nil, ActiveHandles returning 0, MetricsSnapshot returning zero values
- **Verification**: After t4b, `go build ./internal/cli/...`. Then `go test -run TestDiagnostics_ ./internal/cli/...` — must FAIL with assertion errors
- **Timeout**: 60s
- **Context scope**: `internal/ledger/repository.go`, `internal/ledger/types.go`, `internal/events/metrics.go`

## Wave 4 — Diagnostics full implementation
### t5: Diagnostics full implementation
- **Wave**: 4
- **File**: `internal/cli/diagnostics.go` (modify — fill in stubs)
- **Type**: prod
- **Depends on**: t4b
- **API**: Full impl: ListRuns calls repo.ListRuns, maps to RunSummary with elapsed time; ActiveHandles calls ListRuns with Running+Queued+Created statuses, returns len; MetricsSnapshot delegates to adapter.Snapshot()
- **Verification**: `go test -run TestDiagnostics_ ./internal/cli/...` — all PASS
- **Timeout**: 120s
- **Context scope**: `internal/cli/diagnostics.go`, `internal/cli/diagnostics_test.go`, `internal/ledger/repository.go`

### t6: Review t4a + t4b + t5
- **Wave**: 4
- **File**: review of `internal/cli/diagnostics.go` and `internal/cli/diagnostics_test.go`
- **Type**: review
- **Depends on**: t5
- **Verification**: Read files. Verify: nil repo handled, ActiveHandles uses correct status filter, ListRuns respects limit, MetricsSnapshot delegates correctly
- **Timeout**: 60s
- **Context scope**: `internal/cli/diagnostics.go`, `internal/cli/diagnostics_test.go`

## Wave 5 — Benchmarks
### t7: Benchmarks
- **Wave**: 5
- **File**: `internal/ledger/bench_test.go` (create)
- **Type**: bench
- **Depends on**: none
- **API**: BenchmarkMemoryLedger_CreateRun, BenchmarkMemoryLedger_TaskLifecycle, BenchmarkStorageLedger_CreateRun, BenchmarkStorageLedger_TaskLifecycle
- **Verification**: `go test -bench=. -benchtime=1x ./internal/ledger/...` — compiles and runs without hanging
- **Timeout**: 120s
- **Context scope**: `internal/ledger/memory.go`, `internal/ledger/storage.go`, `internal/ledger/types.go`, `internal/storage/store.go`

## Wave 6 — Wiring
### t8: Wire MetricsAdapter + Diagnostics into runTUI
- **Wave**: 6
- **File**: `internal/cli/tui_run.go` (modify)
- **Type**: prod
- **Depends on**: t2, t5
- **Change**: After `bus := events.New()` (line ~32), insert:
  1. `metricsAdapter := events.NewMetricsAdapter()`
  2. `metricsAdapter.Subscribe(bus)` — subscribes to all 17 kinds
  3. `diag := cli.NewDiagnostics(repo, metricsAdapter)` — but repo is the orchestration repo, need to get it. Since runTUI doesn't currently have access to the orchestration repo, just wire MetricsAdapter for now. Diagnostics can be wired when the CLI has access to the ledger repo.
  4. On `bus.Close()` (line ~50), also call `metricsAdapter.Close()`
- **Verification**: `go build ./internal/cli/... && go test -count=1 ./internal/cli/...` — passes
- **Timeout**: 120s
- **Context scope**: `internal/cli/tui_run.go`, `internal/events/metrics.go`, `internal/cli/diagnostics.go`

### t9: Review t8
- **Wave**: 6
- **File**: review of `internal/cli/tui_run.go` changes
- **Type**: review
- **Depends on**: t8
- **Verification**: Verify MetricsAdapter is subscribed, Close is called, no dangling resources
- **Timeout**: 60s
- **Context scope**: `internal/cli/tui_run.go`

## Wave 7 — Final integration review
### t10: Full integration verification
- **Wave**: 7
- **Type**: review
- **Depends on**: t9
- **Verification**: `go build ./... && go vet ./... && go test -race ./...` — ALL PASS
- **Timeout**: 300s
- **Context scope**: all changed packages

## File Ownership Matrix
| File | Wave | Task |
|------|------|------|
| internal/events/metrics.go | 1 (skeleton), 2 (full) | t1b, t2 |
| internal/events/metrics_test.go | 1 | t1a |
| internal/cli/diagnostics.go | 3 (skeleton), 4 (full) | t4b, t5 |
| internal/cli/diagnostics_test.go | 3 | t4a |
| internal/ledger/bench_test.go | 5 | t7 |
| internal/cli/tui_run.go | 6 | t8 |
