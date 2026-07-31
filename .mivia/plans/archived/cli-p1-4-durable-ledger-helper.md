# P1.4 — Extract `openDurableLedgerRepo`

**Status:** DONE — implemented and archived on master. Shared openDurableLedgerRepo extract.
**Date:** 2026-07-31
**Depends on:** nothing (independent of the other P1 plans; may land before or after P1.5).
**Blocks:** nothing directly. P2.3 (dispatcher constructor collapse) benefits if this lands first.
**Blast radius:** LOW — pure extraction of an identical block; behavior is byte-for-byte
preserved. Touches `dispatcher.go` and `orchestration_state.go` only.

---

## 1. Problem

The SQLite-open + recover + interrupted-runs-report block is **triplicated**. All three
sites are byte-for-byte identical in their core (the warning string is duplicated verbatim):

| # | Site | Entry function |
|---|---|---|
| 1 | `dispatcher.go:44-55` | `NewSessionDispatcherWithContext` |
| 2 | `dispatcher.go:76-87` | `NewSessionDispatcherWithBudgetProvider` |
| 3 | `orchestration_state.go:196-207` | `initCoordinator` (the `else if cfg.StoreBackend == "sqlite"` branch) |

Each repeats:

```go
repo := defaultOrchestrationRepo
var ownedStore *ledger.StorageLedgerRepository
if cfg.StoreBackend == "sqlite" {
    sqlStore, err := storage.OpenSQLite(cfg.StorePath)
    if err != nil {
        fmt.Fprintf(os.Stderr, "warning: failed to open SQLite store %q: %v; falling back to memory backend\n", cfg.StorePath, err)
    } else {
        storageRepo := ledger.NewStorageLedgerRepository(sqlStore)
        recovered, recErr := storageRepo.Recover(context.Background())
        reportInterruptedRuns(os.Stderr, recovered, recErr)
        repo = storageRepo
        ownedStore = storageRepo   // site 3 omits this line — see §3
    }
}
```

The cost is drift risk: a fix to the warning wording, the recovery call, or the fallback
logic must be applied in three places. Site 3 has **already diverged** — it does not assign
`ownedStore`, so its SQLite store is never closed on dispatcher shutdown (sites 1 and 2 wire
`d.OnClose(func() { _ = ownedStore.Close() })`). This is a latent resource leak, not just
a style issue.

## 2. Goals and non-goals

### Goals
- Collapse the three identical SQLite-open blocks into one helper.
- Remove the duplicated warning string (one source of truth).
- **Fix the site-3 store-close gap** as part of the extraction (the helper returns the
  owned store so `initCoordinator` can wire `OnClose` like the other two sites already do).
- Preserve exact behavior at sites 1 and 2 (byte-identical output, identical close wiring).

### Non-goals
- Do not change the SQLite backend choice, the recovery algorithm, or the warning message.
- Do not change `defaultOrchestrationRepo` semantics.
- Do not consolidate the `NewSessionDispatcher*` constructors themselves (that is P2.3).
- Do not add new config keys or validation.

## 3. Architecture / Approach

Extract one unexported helper in `orchestration_state.go` (it already owns the
orchestration repo singletons and `reportInterruptedRuns`):

```go
// openDurableLedgerRepo opens a SQLite-backed ledger repository when configured,
// runs startup recovery, reports interrupted runs, and returns the owned store
// (if any) so the caller can close it on shutdown. On any open failure it falls
// back to the in-memory default repo and writes a warning to w; it never returns
// an error for an open failure.
func openDurableLedgerRepo(cfg config.SubagentConfig, w io.Writer) (repo ledger.LedgerRepository, ownedStore *ledger.StorageLedgerRepository) {
    repo = defaultOrchestrationRepo
    if cfg.StoreBackend != "sqlite" {
        return repo, nil
    }
    sqlStore, err := storage.OpenSQLite(cfg.StorePath)
    if err != nil {
        fmt.Fprintf(w, "warning: failed to open SQLite store %q: %v; falling back to memory backend\n", cfg.StorePath, err)
        return repo, nil
    }
    storageRepo := ledger.NewStorageLedgerRepository(sqlStore)
    recovered, recErr := storageRepo.Recover(context.Background())
    reportInterruptedRuns(w, recovered, recErr)
    return storageRepo, storageRepo
}
```

**Per-site wiring (the reason the store handle is returned):**

- **Sites 1 & 2** (`dispatcher.go`): `repo, ownedStore := openDurableLedgerRepo(cfg, os.Stderr)`,
  then on error `if ownedStore != nil { _ = ownedStore.Close() }`, on success
  `if ownedStore != nil { d.OnClose(func() { _ = ownedStore.Close() }) }`. Identical to today.
- **Site 3** (`initCoordinator`): `repo, ownedStore := openDurableLedgerRepo(cfg, os.Stderr)`,
  then `if ownedStore != nil { d.OnClose(func() { _ = ownedStore.Close() }) }`. **This is the
  behavior fix** — today site 3 leaks the store. This is a strict improvement and is called
  out in §6 acceptance.

`initCoordinator` already receives `d *runtime.Dispatcher`, so it can wire `OnClose`.

## 4. Implementation waves

Every production task follows ADLC TDD discipline. This is a REFACTOR: existing tests must
stay green at every gate. New tests pin the helper directly.

| Wave | Scope | Required proof |
|---|---|---|
| 0 | Challenge: is the site-3 close gap intentional? Inspect `initCoordinator` callers to confirm `d.OnClose` is reachable there. | Step-0 disposition: the close wiring is safe to add (sites 1/2 already do it on the same `*runtime.Dispatcher`). |
| 1 | Add `openDurableLedgerRepo` in `orchestration_state.go` (RED: a new test asserts the signature exists and returns `(defaultOrchestrationRepo, nil)` for a non-sqlite config). | `go test ./internal/cli -run TestOpenDurableLedgerRepo` fails then passes. |
| 2 | Migrate site 1 (`NewSessionDispatcherWithContext`) to call the helper. | Existing dispatcher tests green; warning output unchanged. |
| 3 | Migrate site 2 (`NewSessionDispatcherWithBudgetProvider`). | Same gate. |
| 4 | Migrate site 3 (`initCoordinator`); add the `OnClose` wiring that was missing. | A new test asserts the store is closed when the dispatcher closes (the leak fix). Existing `initCoordinator` tests green. |
| 5 | Grep-residual gate: confirm `storage.OpenSQLite` appears exactly once in production `internal/cli` (in the helper). | `grep -n "storage.OpenSQLite" internal/cli/*.go` → one hit. |
| 6 | Audit + verify. | `make verify`, `make test`, `make race` on `internal/cli`. |

## 5. Verification

Minimum gates after implementation:

```text
go test ./internal/cli -count=1
go test -race ./internal/cli -count=1
go vet ./...
go build ./...
make verify
```

- Warning string appears exactly once in production source (grep `falling back to memory backend`).
- `storage.OpenSQLite` appears exactly once in production `internal/cli`.

## 6. Rollback

Pure revert of the extraction restores the three inline blocks. **Rollback must not restore
the site-3 store-close omission** — if the extraction is reverted, the `OnClose` wiring added
in Wave 4 must be kept (or site 3 leaks again). If the close wiring itself is found unsafe,
return to Step 0 rather than reverting only the helper.

## 7. Out of scope

- Collapsing the `NewSessionDispatcher*` constructor family → P2.3.
- Deleting the dead unexported `newSessionDispatcher`/`newSessionDispatcherWithContext` → P1.5.
