# P1.3 — Extract `accessibleOrchestrationHandle` + run-handle error consts

**Status:** DESIGN-READY — implementation must pass ADLC Step 0 (plan challenge + lock)
before any Go file is created or edited. REFACTOR, behavior-preserving, TDD-first.
**Date:** 2026-07-31
**Depends on:** nothing. (In the review's suggested order this is Wave 3, after P1.5 dead-code
deletion and P1.1 theme — those are independent and not prerequisites; this plan stands alone.)
**Blocks:** nothing directly. P1.4 (`openDurableLedgerRepo`) and P2.1 (tool-name consts)
touch the same `internal/cli` orchestration files and benefit from this helper existing, but
neither contractually depends on it.
**Blast radius:** LOW — pure mechanical extract inside one package (`internal/cli`). No
public/exported API changes, no cross-package boundaries, no config, no storage. Four `Execute`
methods each lose ~8 duplicated lines; behavior is pinned byte-for-byte by existing tests.

---

## 1. Problem

The identical ~8-line **run-handle lookup + accessibility gate** is copy-pasted across four
tool `Execute` methods. Each site opens with the same triple:

```go
if params.RunID == "" {
    return `{"error":"run_id is required"}`, nil
}
rawHandle, ok := runHandles.Load(params.RunID)
if !ok {
    return `{"error":"unknown run_id"}`, nil
}
record, ok := rawHandle.(*orchestrationHandle)
if !ok || !orchestrationHandleAccessible(ctx, record, t.dispatcher, t.repo) {
    return `{"error":"unknown run_id"}`, nil
}
```

The four sites, verified at HEAD:

| # | File | Site (function) | empty-check | `runHandles.Load` | accessibility gate |
|---|------|-----------------|-------------|-------------------|--------------------|
| 1 | `orchestrate.go:355` | `inspectAgentTool.Execute` (`inspect_agents`) | :355 | :358 | :363 |
| 2 | `orchestrate_lifecycle.go:158` | `joinRunTool.Execute` (`join_run`) | :158 | :161 | :166 |
| 3 | `orchestrate_lifecycle.go:245` | `cancelRunTool.Execute` (`cancel_run`) | :245 | :248 | :253 |
| 4 | `ledger_tools.go:286` | `listRunEventsTool.Execute` (`list_run_events`) | :286 | :301 | :306 |

Alongside the gate, the two JSON error literals are inlined everywhere:

- `{"error":"unknown run_id"}` — **9** production occurrences:
  the 8 gate-block returns (2 per site above) **plus** a distinct post-gate branch at
  `ledger_tools.go:315` (an accessible run whose event ledger row is gone —
  `errors.Is(err, ledger.ErrNotFound)`). The 9th is outside the helper's lookup path but
  must return the *same* string for the *same* reason, so it consumes the same const.
- `{"error":"run_id is required"}` — **4** occurrences (one per site, the empty-check).

### ⚠️ Critical invariant — INV-AG-9 (must be preserved verbatim)

> The repetition is **deliberate**. An **unknown** run and an **inaccessible** (foreign-principal)
> run **must remain indistinguishable** to every caller. (`INV-AG-9`,
> `.mivia/invariants.md`.)

The accessibility gate collapses two distinct failure causes — (a) no handle registered for
the ID, and (b) a handle exists but belongs to a different session principal — onto the
**single** literal `{"error":"unknown run_id"}`. This is an intentional anti-enumeration
property: a caller probing for run existence cannot learn whether a run *exists but is not
theirs*. Any helper that refactors this gate **must not** split the two into different
responses. This is the single highest-risk requirement of the change and is restated as an
explicit goal, a non-goal, an acceptance criterion, and a RED test.

The existing tests already pin the indistinguishability byte-for-byte (§5), so this is a
TDD-*preserving* refactor: the regression net exists today and must stay green.

---

## 2. Goals and non-goals

### Goals

- Collapse the 4 copy-pasted lookup+gate blocks into one package-private helper.
- Replace the 9 inline `unknown run_id` literals and 4 inline `run_id is required`
  literals with two named consts, so the exact byte string is owned in exactly one place.
- **Preserve INV-AG-9 exactly:** unknown and inaccessible runs continue to return the
  identical `{"error":"unknown run_id"}` string; no caller can distinguish them.
- Keep every existing test byte-for-byte green (no assertion changes — the JSON these tests
  compare against is the *value* the consts now hold).
- Remove ~32 lines of duplication across 4 files with zero behavior change.

### Non-goals

- Do **not** introduce a new error type, `errors.Is`-able sentinel, or structured error.
  The wire contract is a fixed JSON *string* returned from `Execute`; introducing a typed
  error would change `Execute`'s `(string, error)` semantics and risks `statusFromErr`
  misclassification. Keep returning the raw string.
- Do **not** merge the `unknown run_id` response with any other message, status code, or
  distinguishing field. Indistinguishability is the point (INV-AG-9).
- Do **not** refactor `orchestrationHandleAccessible` (`orchestration_state.go:171`) itself,
  the `runHandles` registry, the `orchestrationHandle` struct, or the `ErrNotFound` *logic*.
  Only the repeated *call sites* and the literals move.
- Do **not** touch the legacy `spawn_agent`/`dispatch_tasks` registration paths
  (`orchestrate.go:462`, `dispatch.go`) — they are not among the 4 gated tool reads.
- Do **not** rename the existing predicate `orchestrationHandleAccessible` — its callers in
  `resume_test.go:357,361` would churn for no benefit.

---

## 3. Architecture / Approach

### New module: `internal/cli/orchestration_access.go`

One new file holds the two consts and the single helper. It depends only on symbols already
defined in `orchestration_state.go` (`orchestrationHandle` at `:68`, `runHandles`, and the
predicate `orchestrationHandleAccessible` at `:171`), so it stays inside package `cli` with
no new import and no new dependency direction.

```go
// orchestration_access.go — package cli

// Run-handle error envelopes returned by the orchestration read tools.
// INV-AG-9: an unknown run and an inaccessible (foreign-principal) run MUST
// return the identical errJSONUnknownRunID string. Do not split them.
const (
    errJSONUnknownRunID   = `{"error":"unknown run_id"}`
    errJSONRunIDRequired  = `{"error":"run_id is required"}`
)

// accessibleOrchestrationHandle performs the run-handle lookup and accessibility
// gate shared by the read/control tools (inspect_agents, join_run, cancel_run,
// list_run_events). On success it returns the resolved handle record and an
// empty error string. On any failure (empty id, unregistered id, wrong type,
// or an inaccessible foreign-principal run) it returns a nil record and the
// matching error JSON string, which the caller returns verbatim.
//
// INV-AG-9: the "not registered" and "registered but inaccessible" cases both
// return errJSONUnknownRunID so a caller cannot tell them apart. This
// indistinguishability is load-bearing — do not add a distinguishing branch.
func accessibleOrchestrationHandle(
    ctx context.Context,
    runID string,
    dispatcher *runtime.Dispatcher,
    repo ledger.LedgerRepository,
) (*orchestrationHandle, string) {
    if runID == "" {
        return nil, errJSONRunIDRequired
    }
    rawHandle, ok := runHandles.Load(runID)
    if !ok {
        return nil, errJSONUnknownRunID
    }
    record, ok := rawHandle.(*orchestrationHandle)
    if !ok || !orchestrationHandleAccessible(ctx, record, dispatcher, repo) {
        return nil, errJSONUnknownRunID
    }
    return record, ""
}
```

The helper is a verbatim lift of the four existing blocks. It adds **no logic** — only the
naming of the return path. The predicate `orchestrationHandleAccessible` stays the single
source of the principal/dispatcher/repo match; the helper only sequences lookup →
type-assert → gate → error-string mapping.

### Why a new file (not extending `orchestration_state.go`)

The two are alternatives and both are correct; the new file is preferred because:
- ADLC favors additive, isolated units (one new prod file = one reviewable task, low blast
  radius).
- It keeps `orchestration_state.go` focused on the handle *type*, the registry, and
  retention; the access *protocol* for tool callers becomes a named seam.
- The package is already decomposed into ~90 focused files (per the review), so a
  `*_access.go` sibling fits the established grain.

If a Step-0 challenge prefers locality, the consts+helper may instead be appended to
`orchestration_state.go` directly below `orchestrationHandleAccessible` (`:177`) — same
package, identical behavior, zero functional difference. Either home is acceptable; **do not
do both.**

### Naming proximity note

`accessibleOrchestrationHandle` (this helper — returns `(*orchestrationHandle, string)`)
sits beside `orchestrationHandleAccessible` (existing predicate — returns `bool`). The names
are deliberately close to signal "the helper is the lookup+gate wrapper around the
predicate." The doc comments on both disambiguate. This is intentional, not a smell.

### Call-site transform (identical at all 4 sites)

Before (e.g. `orchestrate.go:354-365`):

```go
if params.RunID == "" {
    return `{"error":"run_id is required"}`, nil
}
rawHandle, ok := runHandles.Load(params.RunID)
if !ok {
    return `{"error":"unknown run_id"}`, nil
}
record, ok := rawHandle.(*orchestrationHandle)
if !ok || !orchestrationHandleAccessible(ctx, record, t.dispatcher, t.repo) {
    return `{"error":"unknown run_id"}`, nil
}
handle := record.handle
```

After:

```go
record, errJSON := accessibleOrchestrationHandle(ctx, params.RunID, t.dispatcher, t.repo)
if errJSON != "" {
    return errJSON, nil
}
handle := record.handle
```

Each site keeps `handle := record.handle` and its existing `record.coord.*` calls unchanged
— the helper returns the full `*orchestrationHandle` record, not just the inner
`coordinator.RunHandle`, so `record.coord`/`record.handle` remain reachable exactly as today.

### The 9th `unknown run_id` site (`ledger_tools.go:315`)

This is **not** part of the lookup/gate; it sits *after* the gate passes, in the
`t.repo.ListEvents` error path (`errors.Is(err, ledger.ErrNotFound)`). It returns the same
string for the same INV-AG-9 reason (a run whose in-memory handle is accessible but whose
durable event rows are gone must not be distinguished from an unknown run). Its transform is
const-only — it does **not** call the helper:

```go
// ledger_tools.go:313-315, before:
if errors.Is(err, ledger.ErrNotFound) {
    return `{"error":"unknown run_id"}`, nil
}
// after:
if errors.Is(err, ledger.ErrNotFound) {
    return errJSONUnknownRunID, nil
}
```

---

## 4. Implementation waves

Per ADLC: every production task is preceded by a compiling RED test that fails an
assertion; 1 file per task; waves gate on `go build ./... && go test -race ./internal/cli/...`.

| Wave | Task | File | Type | Depends on | Required proof (RED→GREEN) |
|------|------|------|------|------------|------------------------------|
| 1 | `w1a` | `orchestration_access_test.go` | **test (RED)** | — | New test compiles, fails: `accessibleOrchestrationHandle` undefined. Asserts all 5 paths (see §5). |
| 1 | `w1b` | `orchestration_access.go` | **prod (GREEN)** | `w1a` | Consts + helper defined; `w1a` passes. |
| 2 | `w2a` | `orchestrate.go` | **refactor** | `w1b` | `inspect_agents` gate (:354-365) → helper call; existing `TestUnauthorizedAndUnknownAreIndistinguishable` + `TestRunHandleNotAccessibleToOtherOwner["inspect"]` stay green. |
| 2 | `w2b` | `orchestrate_lifecycle.go` | **refactor** | `w1b` | Both `join_run` (:157-168) and `cancel_run` (:244-255) → helper call. Same file, two functions — one task (mechanical identical edit); cite ADLC tension, accept for a byte-identical transform. Existing tests :250,:289,:325 stay green. |
| 2 | `w2c` | `ledger_tools.go` | **refactor** | `w1b` | `list_run_events` empty-check (:286) + gate (:300-308) → helper call; **and** the `ErrNotFound` branch (:315) → `errJSONUnknownRunID` const. `TestListRunEventsRequiresRunOwnership` (:483) stays green. |
| 3 | `w3a` | (read-only) | **review** | `w2a,w2b,w2c` | A reviewer reads all 5 changed/new files, confirms INV-AG-9 preserved, no remaining inline `{"error":"unknown run_id"}` / `{"error":"run_id is required"}` literals in production code, and that every site still returns the raw string (no typed error). |

### Wave notes

- **Wave 2 is parallelizable** — `w2a`, `w2b`, `w2c` touch disjoint files, so they may run
  concurrently via `dispatch_tasks`. They all depend only on `w1b` (the helper exists).
- The `w2b` "two functions, one file" case technically strains the ADLC "1 function per
  production task" rule. It is accepted because the two edits are byte-identical mechanical
  transforms in the same file that cannot conflict, and splitting them into two tasks on the
  *same file* would force serial execution for no safety gain. If a Step-2 validator
  objects, split `w2b` into `w2b-join` and `w2b-cancel` as serial sub-tasks.
- **No test files are edited** except the new `orchestration_access_test.go`. The existing
  pinning tests (`orchestrate_lifecycle_test.go`, `ledger_tools_test.go`) are the regression
  net and must pass unchanged — their string literals compare against the *value*, which the
  consts now hold verbatim.
- This refactor is **not** Fast-Path-eligible (it adds a new type-bearing helper + new test
  file), so full Steps 0–6 apply.

---

## 5. Verification

### New RED test (`orchestration_access_test.go`) — pins the helper directly

The unit test for `accessibleOrchestrationHandle` covers all branches and the invariant:

| Scenario | Input | Assert |
|----------|-------|--------|
| empty run id | `runID == ""` | returns `(nil, errJSONRunIDRequired)`; value == `` `{"error":"run_id is required"}` `` |
| unknown id | unregistered `runID` | returns `(nil, errJSONUnknownRunID)` |
| wrong type | a non-`*orchestrationHandle` value stored under the id | returns `(nil, errJSONUnknownRunID)` |
| foreign principal (inaccessible) | registered handle, `principal.sessionID != caller` | returns `(nil, errJSONUnknownRunID)` |
| **INV-AG-9 indistinguishability** | unknown vs. foreign-principal outputs compared | the two returned strings are **equal** and both == `errJSONUnknownRunID` |
| accessible | registered handle, matching caller principal/dispatcher/repo | returns `(non-nil record, "")`; `record.handle` reachable |

This RED test is additional to — not a replacement for — the existing pinning tests below.

### Existing pinning tests (must stay byte-for-byte green, unedited)

These assert the exact JSON strings and the indistinguishability property at the `Execute`
level; they are the behavior contract the refactor must not move:

- **`TestUnauthorizedAndUnknownAreIndistinguishable`** — `orchestrate_lifecycle_test.go:~386`
  (pins `inspect_agents`; `unauthorized != unknown || unknown != `unknown run_id`` at :416).
- **`TestRunHandleNotAccessibleToOtherOwner`** — `orchestrate_lifecycle_test.go:268`, drives
  `inspect`/`join`/`cancel` (map at :283) and asserts `out != `unknown run_id`` per tool
  (cross-session table at :240, asserting at :250/:289/:325).
- **`TestListRunEventsRequiresRunOwnership`** — `ledger_tools_test.go:~470`, asserts
  `unauthorized != unknown || unknown != `unknown run_id`` at :483.
- **`TestListRunEventsRejectsUnknownKind`** — `ledger_tools_test.go:321`; its control
  assertion (:353) guards that the unknown-kind path is *not* silently masked by the
  ownership gate — relevant because this refactor touches the gate at `ledger_tools.go`.

### Minimum command gates (ADLC Step 4 wave gate + Step 6)

```text
go test ./internal/cli/... -count=1
go test -race ./internal/cli/... -count=1
go vet ./...
go build ./...
make invariants
make verify
```

`make invariants` must pass — `internal/cli` is an invariant-enforced package, and
`INV-AG-9`'s tests (`TestUnauthorizedAndUnknownAreIndistinguishable`,
`TestListRunEventsRequiresRunOwnership`, `TestCancelRunCannotCancelForeignRun`,
`TestRunHandleAccessibleToAncestor`, `TestRunHandleNotAccessibleToOtherOwner`) are the
manifest rows this change is most likely to disturb if the gate were mis-extracted.

---

## 6. Rollback

The change is a pure mechanical extract with no schema, storage, or config impact, so
rollback is simply `git revert` of the commits. There are no migrations to undo and no
persistent state to reconcile:

- Reverting restores the four inline gate blocks and the nine/four literals verbatim.
- Because no wire bytes change (the consts hold the exact prior strings), a partial revert
  (e.g. only the call sites, leaving `orchestration_access.go`) is also coherent — the
  helper becomes dead code but compiles and is harmless.
- **Rollback criterion (what kills this plan):** any of the §5 invariant tests
  (`TestUnauthorizedAndUnknownAreIndistinguishable`, `TestListRunEventsRequiresRunOwnership`,
  or any INV-AG-9 manifest row) turns red, *or* a caller can be shown to distinguish an
  unknown run from an inaccessible one. That is an INV-AG-9 regression — halt, revert,
  return to ADLC Step 0.

---

*Plan written against code at HEAD. All file:line references verified via `grep`/`read_file`
over `internal/cli`. Finding source: P1.3 in `.mivia/reports/cli-internal-refactoring-review.md`.
Process: ADLC (`.mivia/rules/05-adlc-agentic-development-lifecycle.md`), REFACTOR path,
TDD-preserving.*
