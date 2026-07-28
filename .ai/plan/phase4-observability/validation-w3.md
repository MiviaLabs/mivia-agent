# Validation: Wave 3 (t4a, t4b)

## Questions

### 1. Can cli/diagnostics.go import both `ledger` and `events`?
**PASS** — Both packages already imported by other files in `internal/cli/`:
- `ledger` → delegate.go, dispatch.go, dispatcher.go, orchestrate.go, orchestrate_lifecycle.go
- `events` → tui_run.go, tui_events.go, tui.go, ui_adapter.go, subagent_progress.go, etc.
No cycle risk (neither package imports `cli`). A new file can import both.

### 2. Will t4a's test compile after t4b provides the skeleton?
**PASS** — By design (RED-test-first with skeleton in same wave). After t4b provides `Diagnostics`, `NewDiagnostics`, `ListRuns`, `ActiveHandles`, `MetricsSnapshot`, and `RunSummary` types, the test will compile (types exist) but fail on assertions (zero/nil return values). Verified: plan.md explicitly calls this out.

### 3. Is `repo ledger.LedgerRepository` accessible?
**PASS** — `ledger` package is already imported in cli. `LedgerRepository` is a public interface in `internal/ledger/repository.go`. Already used in `cli/orchestrate.go` etc.

### 4. Is `*events.MetricsAdapter` accessible?
**PASS** — `events` package is already imported in cli. `MetricsAdapter` is created in Wave 1 (t1b in `internal/events/metrics.go`) which executes before Wave 3 (t4b). By the time diagnostics.go is written, the type exists.

## Verdict

| Task | Status | Rationale |
|------|--------|-----------|
| **t4a** — Diagnostics RED test | **PASS** | Test file saved as pending, won't compile until t4b skeleton exists. Same-wave skeleton pattern resolves compilation gap. |
| **t4b** — Diagnostics type skeleton | **PASS** | All required import paths (`ledger`, `events`) are accessible in the `cli` package. No cycle risk. Must include `RunSummary` type in skeleton for test compilation. |
