# P2.6 — Use typed `ledger` status constants instead of string literals

**Status:** DONE — implemented and archived on master. Literal→typed ledger/local const swap.
**Date:** 2026-07-31
**Depends on:** nothing.
**Blocks:** nothing.
**Blast radius:** LOW — mechanical literal→const swap in `internal/cli`; no behavior change.

---

## 1. Problem

The `internal/ledger` package defines **typed** status constants in
`internal/ledger/types.go`:

```go
type RunStatus string
const (
    RunStatusCreated   RunStatus = "created"
    RunStatusQueued    RunStatus = "queued"
    RunStatusRunning   RunStatus = "running"
    RunStatusCompleted RunStatus = "completed"
    RunStatusFailed    RunStatus = "failed"
    RunStatusCanceled  RunStatus = "canceled"
)

type TaskStatus string
const (
    TaskStatusQueued          TaskStatus = "queued"
    TaskStatusRunning         TaskStatus = "running"
    TaskStatusCompleted       TaskStatus = "completed"
    TaskStatusFailed          TaskStatus = "failed"
    TaskStatusTimedOut        TaskStatus = "timed_out"
    TaskStatusCanceled        TaskStatus = "canceled"
    TaskStatusBlocked         TaskStatus = "blocked"
    TaskStatusCancelRequested TaskStatus = "cancel_requested"
    TaskStatusRetryPending    TaskStatus = "retry_pending"
)
```

Two `internal/cli` consumers already use them correctly:
- `orchestrate_salvage.go:50` — `switch ledger.TaskStatus(task.Status)`.
- `diagnostics.go:89-92` — `d.repo.ListRuns(ctx, ledger.RunStatusRunning, ledger.RunStatusQueued, ledger.RunStatusCreated)`.

But **`tui_run_dashboard.go`** ignores them and hardcodes the same words as raw string
literals throughout (~25 occurrences). A typo (`"timed-out"` vs `"timed_out"`,
`"cancelled"` vs `"canceled"`) would silently break status rollup with no compile error.

Verified literal sites in `tui_run_dashboard.go`:

| Line | Literal | Typed equivalent |
|---|---|---|
| 73 | `"running"`, `"cancel_requested"`, `"retry_pending"` | `TaskStatusRunning`, `TaskStatusCancelRequested`, `TaskStatusRetryPending` |
| 76 | `"queued"`, `"retry_queued"` | `TaskStatusQueued` / **no const — see §3** |
| 79 | `"failed"`, `"timed_out"`, `"interrupted_unrecoverable"` | `TaskStatusFailed`, `TaskStatusTimedOut` / **no const — see §3** |
| 81, 96 | `"completed"` | `TaskStatusCompleted` / `RunStatusCompleted` |
| 83, 84 | `"canceled"` | `TaskStatusCanceled` / `RunStatusCanceled` |
| 93 | `"running"` | `RunStatusRunning` |
| 99 | `"failed"` | `RunStatusFailed` |
| 101 | `"unknown"` | **no const — see §3** |
| 110, 161 | `!= "completed" && != "failed" && != "canceled"` | run-status terminal check |
| 255-325 | status-color switch | rollup display |
| 421-425 | `info.Status = "completed"/"failed"/"canceled"` | mapping into a local struct |

`dispatch.go` (`statusFromErr` at `:225-242`) also returns these words; it should be checked
for literal alignment but may intentionally produce wire-format strings — see §3.

## 2. Goals and non-goals

### Goals
- Replace hardcoded status literals in `tui_run_dashboard.go` (and audited sites in
  `dispatch.go`) with the typed `ledger` constants where the string denotes that exact state.
- Prevent typo-silent-breakage via compile-time constant references.
- Add local constants for the dashboard-specific compound states that have no `ledger` equivalent.

### Non-goals
- Do not change the `ledger` package or its constant set.
- Do not change `TaskSnapshot.Status` / `RunSnapshot.Status` field types (they are `string`
  for storage compatibility; the typed consts convert via `string(...)` / `ledger.TaskStatus(...)`).
- Do not change any rendered output — this is a literal→const swap, byte-for-byte.

## 3. The compound / non-ledger states (decision)

Three strings in the dashboard have **no** `ledger` constant:

| Literal | Used as | Decision |
|---|---|---|
| `"retry_queued"` | task status (set by coordinator retry path) | **Verify in `internal/coordinator`** whether it should be a `ledger.TaskStatus` const. If it is a real persisted status, propose adding `TaskStatusRetryQueued` to `ledger` (out of scope here — file a follow-up). For this plan: add a **local const** `taskStatusRetryQueued = "retry_queued"` in the dashboard file and reference it. |
| `"interrupted_unrecoverable"` | task failure reason (set by resume) | Local const `taskStatusInterruptedUnrecoverable`. Not a lifecycle status. |
| `"degraded"` | **dashboard-only** rollup state (a run with mixed task outcomes) | Local const `dashStatusDegraded`. Lives only in the dashboard. |
| `"unknown"` | dashboard fallback | Local const `dashStatusUnknown`. |

**Action:** add a small const block at the top of `tui_run_dashboard.go` for the four
non-ledger strings, and use `ledger.TaskStatus*` / `ledger.RunStatus*` for the rest.

**Note on `dispatch.go statusFromErr`:** this function produces the wire-format status
string embedded in the tool result payload (`{"status":"canceled"}`, etc.). These strings
are a **model-facing contract** (INV-AG-21), not internal comparisons. Replacing the literals
with `string(ledger.TaskStatusCanceled)` is safe **only if** the const value is identical
(verify: `TaskStatusCanceled = "canceled"` ✓). Do this in a separate wave with a test that
asserts the exact JSON bytes are unchanged.

## 4. Implementation waves

REFACTOR — TDD-preserving. Existing dashboard and dispatch tests are the gate.

| Wave | Scope | Required proof |
|---|---|---|
| 0 | Confirm the literal→const mapping; verify `"retry_queued"` origin in `internal/coordinator`. | Step-0 disposition. |
| 1 | Add the local const block in `tui_run_dashboard.go` for non-ledger states. | Compiles; no behavior change. |
| 2 | Replace the `case` literals in the rollup functions (`:73-101`) with `ledger.TaskStatus*` / local consts. | Dashboard tests green; rendered output byte-identical. |
| 3 | Replace the terminal-status checks (`:110, 161`) with `ledger.RunStatus*`. | Same. |
| 4 | Replace the color-switch and count literals (`:255-325`). | Same. |
| 5 | Replace `info.Status =` assignments (`:421-425`). | Same. |
| 6 | (Optional, separate) `dispatch.go statusFromErr` literal→const with a JSON-byte pinning test. | INV-AG-21 tests green; exact payload bytes unchanged. |
| 7 | Audit + verify. | `make verify`, `make test`. |

## 5. Verification

```text
go test ./internal/cli -count=1
go test -race ./internal/cli -count=1
go vet ./...
make verify
```

- Grep-residual: `grep -n '"completed"\|"failed"\|"running"\|"canceled"\|"timed_out"\|"cancel_requested"\|"retry_pending"\|"queued"' internal/cli/tui_run_dashboard.go`
  should return **only** the local-const declarations (for the non-ledger strings) or zero hits.

## 6. Rollback

Pure revert — consts become literals again. No behavior change.

## 7. Out of scope

- Adding `TaskStatusRetryQueued` to `ledger` (follow-up if `"retry_queued"` is persisted).
- Changing `dispatch.go`'s payload contract (Wave 6 is optional and isolated).
- The `statusFromErr` cancellation/timeout classification logic itself.
