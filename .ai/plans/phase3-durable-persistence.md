# Phase 3: Durable Persistence — Implementation Plan

**Status:** Draft implementation plan
**Date:** 2026-07-28
**Parent:** `.ai/plans/orchestration-completion.md`

---

## 1. Current State Summary

```
Completed:
  ✅ LedgerRepository interface         (internal/ledger/repository.go)
  ✅ MemoryLedgerRepository             (internal/ledger/memory.go)
  ✅ Types + transitions                (internal/ledger/types.go, transition.go)
  ✅ DisplayNameGenerator               (internal/ledger/displayname.go)
  ✅ Coordinator (Spawn/Join/Inspect/Cancel) (internal/coordinator/)
  ✅ Pool, multi_step, oneshot          (internal/subagents/)
  ✅ CLI orchestration tools            (internal/cli/orchestrate.go, orchestrate_lifecycle.go)
  ✅ storage.Store + SQLite impl        (internal/storage/store.go)
  ✅ EventBus + UIAdapter               (internal/events/, internal/cli/ui_adapter.go)
  ✅ Agent loop → EventBus wiring       (internal/agent/emit.go)

Gaps (Phase 3 — this plan):
  ❌ StorageLedgerRepository — wraps storage.Store, implements LedgerRepository
  ❌ Event schema — mapping Run/Task/Attempt state ↔ storage events with projection rebuild
  ❌ Config option — backend selection (memory vs sqlite) in mivia.toml
  ❌ Wiring — passing configured store through initCoordinator()
  ❌ Recovery — startup reconciliation from durable state
  ❌ Tests — kill/restart, crash recovery, duplicate prevention, projection rebuild

Gaps (Phase 4 — follow-up plan):
  ❌ Metrics adapter for events.Bus
  ❌ Operator diagnostics
  ❌ Benchmarks
```

## 2. Implementation Steps

### Step 1: Define Event Schema for Durable Storage

The `storage.Store` interface currently stores flat `Event` records:
```go
type Event struct {
    ID       string
    RunID    string
    Sequence int
    Kind     string
    Payload  []byte
}
```

We need to define a canonical mapping from `ledger.LifecycleEvent` / run/task state to these storage events.

**Schema design** — define these `Kind` constants for storage events:

| Kind | Payload (JSON) | Purpose |
|------|----------------|---------|
| `run_created` | `RunSnapshot` | Initial run record |
| `run_status_changed` | `{"status": "...", "completed_at": "..."}` | Run state transition |
| `task_created` | `TaskSnapshot` | Initial task record |
| `task_status_changed` | `{"status": "...", "version": N, "completed_at": "..."}` | CAS status transition |
| `task_output_set` | `{"output_ref": "...", "error_ref": "..."}` | Output references |
| `task_attempt` | `{"attempt_id": "...", "status": "...", "finished_at": "..."}` | Attempt terminal state |
| `lifecycle_event` | `LifecycleEvent` | Arbitrary lifecycle events |
| `run_closed` | `{}` | Run closed marker |

**Projection rebuild**: On startup (or on first access of a run), the `StorageLedgerRepository` replays all events for a run in sequence order, building the current `RunSnapshot` + `TaskSnapshot` in memory. This is deterministic — the same event sequence always produces the same state.

### Step 2: Create `StorageLedgerRepository`

**File:** `internal/ledger/storage.go`

```go
package ledger

import (
    "context"
    "encoding/json"
    "fmt"
    "sync"
    "time"

    "github.com/MiviaLabs/mivia-agent/internal/storage"
)

// StorageLedgerRepository wraps a storage.Store as a durable LedgerRepository.
// It maintains an in-memory projection (like MemoryLedgerRepository) that is
// rebuilt from stored events on first access or on startup.
// Writes go to both the in-memory projection AND the durable store atomically.
type StorageLedgerRepository struct {
    store    storage.Store
    mem      *MemoryLedgerRepository // in-memory projection (read path)
    mu       sync.RWMutex
    // ...projection rebuild tracking
}
```

**Key design decisions:**

1. **Dual-write**: Every mutation writes to both the durable `storage.Store` (as an append-only event) AND the in-memory projection (`MemoryLedgerRepository`). The in-memory projection serves reads without replay.

2. **Projection rebuild**: On startup, or when a run is first accessed after process restart:
   - Read all events for the `runID` from `storage.Store` via `Events(ctx, runID)`
   - Replay in sequence order to reconstruct `runRecord` and `taskRecord` state
   - Populate the in-memory projection

3. **Idempotency**: Each storage event has a unique ID (same as `LifecycleEvent.ID`). The store's duplicate detection prevents double-writes on retry.

4. **Recovery marker**: On startup, any run with status `running` or `queued` that has no active handle is marked as `interrupted_unrecoverable` (added as a new status or using `failed` with a special label).

### Step 3: Add Config Support

**In `internal/config/types.go`**, add to `SubagentConfig`:

```go
type SubagentConfig struct {
    // ...existing fields...

    // StoreBackend selects the ledger storage backend: "memory" (default) or "sqlite".
    StoreBackend string `toml:"store_backend"`
    // StorePath is the SQLite file path (only used when StoreBackend == "sqlite").
    // Default: ~/.local/share/mivia/orchestration.db (or platform equivalent).
    StorePath string `toml:"store_path"`
}
```

**In `mivia.toml.example`**, add:
```toml
[subagents]
# ...existing config...
# store_backend = "memory"    # "memory" (default) or "sqlite"
# store_path = ""             # SQLite path (auto-resolved when backend=sqlite)
```

**In `internal/config/defaults.go`** or `load.go`, add defaults:
```go
func defaultStorePath() string { /* platform-specific data dir */ }
```

### Step 4: Wire StorageLedgerRepository into initCoordinator

**In `internal/cli/orchestrate.go`**, modify `initCoordinator()`:

```go
func initCoordinator(d *runtime.Dispatcher, cfg config.SubagentConfig, repos ...ledger.LedgerRepository) *coordinator.Coordinator {
    // ...existing singleton check...

    repo := defaultOrchestrationRepo  // MemoryLedgerRepository (default)
    if len(repos) > 0 {
        repo = effectiveOrchestrationRepo(repos[0])
    } else if cfg.StoreBackend == "sqlite" {
        // Open SQLite store, create StorageLedgerRepository
        path := cfg.StorePath
        if path == "" {
            path = defaultStorePath()
        }
        store, err := storage.OpenSQLite(path)
        if err != nil {
            // Log but fall back to memory
            fmt.Fprintf(os.Stderr, "warning: failed to open SQLite store %q: %v; using memory\n", path, err)
        } else {
            repo = ledger.NewStorageLedgerRepository(store)
        }
    }
    // ...rest of initCoordinator...
}
```

Also add a cleanup hook:
```go
d.OnClose(func() {
    if sr, ok := repo.(*ledger.StorageLedgerRepository); ok {
        sr.Close()
    }
    coordinators.Delete(d)
})
```

### Step 5: Startup Recovery

In `StorageLedgerRepository`, implement a `Recover()` method:

```go
// Recover scans all stored runs, rebuilds projections, and marks any run
// with status "running" or "queued" (and no active handle) as
// "interrupted_unrecoverable".
func (s *StorageLedgerRepository) Recover(ctx context.Context) ([]RecoveredRun, error)
```

**Logic:**
1. Read all events grouped by `runID` (iterate known run IDs via a `runs` index table or by scanning event kinds).
2. Rebuild projection for each run.
3. For runs with status `running` or `queued`: mark as `failed` with label `interrupted_unrecoverable`.
4. Return list of recovered runs for logging/notification.

**Add a new index table** in SQLite store for efficient run listing:
```sql
CREATE TABLE IF NOT EXISTS runs (
    run_id TEXT PRIMARY KEY,
    status TEXT NOT NULL DEFAULT 'created',
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

This is a secondary index — the event stream remains the source of truth, and the `runs` table is updated alongside event writes for O(1) run listing.

### Step 6: Tests

#### Unit Tests (in `internal/ledger/storage_test.go`)

| Test | Scenario |
|------|----------|
| `TestStorageLedgerRepository_CreateRun` | Create a run, verify it's in store |
| `TestStorageLedgerRepository_GetRun` | Read back a created run |
| `TestStorageLedgerRepository_ProjectionRebuild` | Rebuild from stored events after simulated restart |
| `TestStorageLedgerRepository_DuplicateEventProtection` | Append same event twice → ErrDuplicate |
| `TestStorageLedgerRepository_TaskLifecycle` | Full task lifecycle through CAS |
| `TestStorageLedgerRepository_ConcurrentAccess` | Race-free concurrent reads/writes |
| `TestStorageLedgerRepository_CloseRun` | Close run, verify terminal |
| `TestStorageLedgerRepository_RecoverInterrupted` | Recover marks running/queued runs as interrupted |
| `TestStorageLedgerRepository_EmptyStore` | Recover on empty store returns empty |
| `TestStorageLedgerRepository_ProjectionConsistency` | Verify in-memory projection matches full replay |

#### Integration Tests

| Test | Scenario |
|------|----------|
| `TestIntegration_Durable_KillRestart` | Simulate process kill/restart — create run, simulate restart (new repo instance), verify state |
| `TestIntegration_CrashRecovery` | Kill mid-task, recover, verify interrupted state |
| `TestStorageLedgerRepository_SQLiteVsMemory` | Same operations produce same results on both backends |

#### Test infrastructure

Use `storage.NewMemory()` (already exists) for fast unit tests. Use `storage.OpenSQLite(":memory:")` for SQLite-specific tests.

### Step 7: Update DOD Gate

Update `.ai/plans/orchestration-completion.md`:

- [ ] Step 1: Event schema defined
- [ ] Step 2: StorageLedgerRepository implemented
- [ ] Step 3: Config option added
- [ ] Step 4: Wiring in initCoordinator
- [ ] Step 5: Startup recovery logic
- [ ] Step 6: All tests pass + race clean
- [ ] `go build ./cmd/mivia/` succeeds
- [ ] `go test -race ./internal/ledger/...` passes
- [ ] `go test -race ./internal/cli/...` passes
- [ ] `go test -race ./internal/coordinator/...` passes

## 3. Files to Create

| File | Purpose |
|------|---------|
| `internal/ledger/storage.go` | StorageLedgerRepository implementation |
| `internal/ledger/storage_test.go` | Unit + integration tests |
| `internal/ledger/storage_recovery.go` | Recovery/reconciliation logic (or inline in storage.go) |

## 4. Files to Modify

| File | Change |
|------|--------|
| `internal/config/types.go` | Add `StoreBackend`, `StorePath` to `SubagentConfig` |
| `internal/config/defaults.go` | Add default store path resolution |
| `internal/config/load.go` | Load new config fields |
| `internal/cli/orchestrate.go` | Wire StorageLedgerRepository in `initCoordinator()` |
| `internal/storage/store.go` | Add `ListRunIDs()` method, `runs` index table, helper for recovery |
| `mivia.toml.example` | Document new config options |
| `.ai/plans/orchestration-completion.md` | Update DOD gate |

## 5. Risk Assessment

| Risk | Likelihood | Mitigation |
|------|-----------|------------|
| SQLite write contention under high concurrency | Low | WAL mode + mutex on Append; pool already limits workers |
| Projection rebuild too slow for many runs | Low | O(n) rebuild; n = total events; index table for O(1) listing |
| Migration between backends | Low | Event schema is uniform; both backends use same event format |
| File path resolution across platforms | Low | Use `os.UserCacheDir()` with platform-specific fallback |
| Data loss on partial write | Low | SQLite WAL + synchronous=FULL + transactions |
| File descriptor leak | Low | OnClose hook in dispatcher cleanup |
