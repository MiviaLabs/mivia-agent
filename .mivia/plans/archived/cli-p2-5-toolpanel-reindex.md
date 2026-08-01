# P2.5 - `toolPanelState.reindex()` helper  *(TUI slice)*

**Status:** Implemented (2026-07-31) - `toolPanelState.reindex` owns the order+clamp idiom; clean sites in `tui_tools_apply.go` + guarded lazy path in `tui.go` converted; `tui_selection.go` / `livepanel.go` left on `orderToolIndices`. Pinned by `TestToolPanelReindex*` composition tests. Live HEAD had no `tui_events.go` sites (design-doc drift).
**Date:** 2026-07-31
**Source finding:** `.mivia/reports/cli-internal-refactoring-review.md` §Priority 2 → P2.5
**Depends on:** nothing.
**Blocks:** nothing.
**Blast radius:** LOW - single package (`internal/cli`), pure mechanical extraction of an
already-correct two-line idiom. No API change visible outside the package; no new types;
no behavior change. Existing tests pin every call site.

---

## 1. Problem

The exact two-line idiom

```go
m.toolPanel.ordered = orderToolIndices(m.toolRows)
m.toolPanel.Scroll = clampToolScroll(
    m.toolPanel.Scroll, m.toolPanel.Selected, m.toolPanel.ordered, toolMaxVisibleRows,
)
```

is duplicated across the TUI slice. Verified at HEAD via `grep orderToolIndices(m.toolRows)`
in `internal/cli`:

| # | Site | Shape | In scope? |
|---|------|-------|-----------|
| 1 | `tui.go:341` | **conditional** - inside `if len(m.toolPanel.ordered) == 0` (lazy init) | ✅ (guard preserved) |
| 2 | `tui_events.go:152` (`applyToolStartFromBus` tail) | clean two-line | ✅ |
| 3 | `tui_events.go:191` (`applyToolEventsOpts` ret) | clean two-line | ✅ |
| 4 | `tui_tools_apply.go:51` (`applyToolEventsOpts` len>0 arm) | two-line inside `len(toolRows)>0` branch | ✅ |
| 5 | `tui_tools_apply.go:133` (`applyToolEndEvent` tail) | clean two-line | ✅ |
| 6 | `tui_selection.go:138` (`focusToolPanel`) | **order-only** - assigns `ordered` then immediately reads `ordered[0]`/`ordered[len-1]`; **no clamp** | ⛔ out of scope (see §3) |
| 7 | `livepanel.go:201` (`liveToolRows`) | **local var** `ordered := orderToolIndices(...)` - not panel-state mutation | ⛔ out of scope (see §3) |

Test-file occurrences (also converted for uniformity, since they exercise the same
contract): `tui_journey_test.go:139,214`, `tui_phase1_test.go:160`,
`tui_smoke_test.go:50`, `tui_tools_test.go:36`, `tui_view_test.go:80,165`.

The report's "~7×" is the count of *production* `orderToolIndices(m.toolRows)` sites;
five of the seven are the canonical order-then-clamp idiom and are the refactor target.

## 2. Goal and non-goals

### Goal
Collapse the repeated order-then-clamp idiom into one method on the type that already
owns the state, so every call site reads `m.toolPanel.reindex(m.toolRows)` and the
ordering/clamping rule lives in exactly one place.

### Non-goals
- Do **not** change `orderToolIndices` or `clampToolScroll` themselves (they are pure,
  already unit-tested in `toolpanel_test.go`, and `clampToolScroll` is also called by
  `selectNext`/`scrollWindow` with other `maxVis` values).
- Do **not** touch the order-only site (`tui_selection.go:138`) or the local-var site
  (`livepanel.go:201`) - see §3.
- Do **not** alter scroll/selection *behavior*. Output of `clampToolScroll` must be
  byte-for-byte identical before and after.
- Do **not** add a config knob, a new type, or a public (exported) symbol.

## 3. Decisions / scope edge cases

**A. Target signature (per the report).**

```go
// reindex recomputes the display order of rows and re-clamps the scroll window so
// the current selection stays visible. It is the single owner of the
// order-then-clamp idiom used after every mutation of m.toolRows.
func (st *toolPanelState) reindex(rows []toolRow) {
    st.ordered = orderToolIndices(rows)
    st.Scroll = clampToolScroll(st.Scroll, st.Selected, st.ordered, toolMaxVisibleRows)
}
```

`toolMaxVisibleRows` is a package-level const (`toolpanel.go:25`, `= 6`).
`clampToolScroll` already defaults to `toolMaxVisibleRows` when `maxVis < 1`, and **every**
existing call site passes exactly `toolMaxVisibleRows`. Baking the const into `reindex`
therefore changes no observable value and matches `clampToolScroll`'s own default. (The
sibling methods `selectNext(delta, maxVis)` / `scrollWindow(delta, maxVis)` take `maxVis`
as a param because callers pass computed widths; `reindex` has no such caller - all five
sites pass the const - so a parameter would be dead variance.)

**B. Conditional site `tui.go:341` - preserve the guard.**

Current:

```go
if len(m.toolPanel.ordered) == 0 {
    m.toolPanel.ordered = orderToolIndices(m.toolRows)
}
...
m.toolPanel.Scroll = clampToolScroll(
    m.toolPanel.Scroll, m.toolPanel.Selected, m.toolPanel.ordered, toolMaxVisibleRows,
)
```

This is lazy: it only reorders when `ordered` is empty, but **always** clamps. `reindex`
unconditionally does both, so it cannot be dropped in verbatim. Resolution: keep the
explicit `if` and call `reindex` inside it; the unconditional `clampToolScroll` that
follows becomes a second `reindex`-free clamp *or* - simplest - leave the trailing clamp
as-is and only replace the guarded `ordered = ...` line. **Preferred (minimal-diff):**
replace just the inner assignment with a call to a order-only expression is not possible
via `reindex`, so for this site the cleanest correct transform is:

```go
if len(m.toolPanel.ordered) == 0 {
    m.toolPanel.reindex(m.toolRows)   // sets ordered + clamps
}
// trailing clampToolScroll(...) stays - it must run even when the guard is false
```

Because `reindex` also clamps, the subsequent standalone `clampToolScroll` is now
redundant *only on the path where the guard fired*; on the (common) path where `ordered`
is already populated, the standalone clamp is still required. **Net:** keep the standalone
clamp; accept one redundant clamp on the cold path. This is provably behavior-identical
(clamp is idempotent) and is the smallest correct diff. Document the reasoning in a
comment at the site.

**C. Out-of-scope site `tui_selection.go:138` (order-only).**

```go
m.toolPanel.ordered = orderToolIndices(m.toolRows)
if reverse {
    m.toolPanel.Selected = m.toolPanel.ordered[len(m.toolPanel.ordered)-1]
} else {
    m.toolPanel.Selected = m.toolPanel.ordered[0]
}
```

This intentionally does **not** clamp - it reorders and then *overwrites* `Selected` from
`ordered`. Calling `reindex` here would run `clampToolScroll` against the *stale*
`Selected` and then discard its result. The clamp is a harmless no-op (output discarded),
but it is wasted work and muddies the "reindex == order+clamp" contract. **Decision: leave
this site on `orderToolIndices` directly.** Rationale recorded in §8. (If a later cleanup
wants full uniformity, add an order-only helper `reorder(rows)` - out of scope here.)

**D. Out-of-scope site `livepanel.go:201` (local var).**

```go
ordered := orderToolIndices(m.toolRows)   // local, for rendering only
```

This does not mutate `toolPanelState` at all; it computes a throwaway order for the live
panel render. `reindex` is a panel-state mutator and is the wrong tool. **Leave as-is.**

## 4. Files

**Create:** none (method lands in the existing `toolpanel.go`, next to its siblings).

**Modify (production, 5 files):**
- `internal/cli/toolpanel.go` - add `func (st *toolPanelState) reindex(rows []toolRow)`.
- `internal/cli/tui.go` - site #1 (conditional; see §3B).
- `internal/cli/tui_events.go` - sites #2, #3.
- `internal/cli/tui_tools_apply.go` - sites #4, #5.

**Modify (tests, 4 files - uniformity only; not strictly required but keeps the codebase
speaking one idiom):**
- `internal/cli/tui_journey_test.go` - lines 139, 214.
- `internal/cli/tui_phase1_test.go` - line 160.
- `internal/cli/tui_smoke_test.go` - line 50.
- `internal/cli/tui_tools_test.go` - line 36.
- `internal/cli/tui_view_test.go` - lines 80, 165.

**Do not modify:** `internal/cli/tui_selection.go`, `internal/cli/livepanel.go`,
`orderToolIndices`, `clampToolScroll`, `selectNext`, `scrollWindow`.

## 5. Test strategy (TDD, RED before GREEN)

`orderToolIndices` and `clampToolScroll` are already covered by
`toolpanel_test.go` (`TestOrderToolIndices*`, `TestClampToolScroll*`,
`TestToolPanelSelectNextKeepsSelectedVisible`, etc.). The *new* behavior to pin is the
**composition**: `reindex` must produce `ordered == orderToolIndices(rows)` **and**
`Scroll == clampToolScroll(preScroll, Selected, ordered, toolMaxVisibleRows)` in one call,
including the boundary cases that the five call sites rely on.

RED test (add to `internal/cli/toolpanel_test.go`, fails assertion before the method
exists - compiles only after the stub is present, so the RED task adds a minimal
non-functional stub *or* the test is written to reference the method and fails to compile;
per ADLC the RED must be an **assertion failure**, not a compile error - so the RED task
includes a one-line `func (st *toolPanelState) reindex(rows []toolRow) {}` stub that does
nothing, making the test compile and then fail its assertions):

| Test | Scenario | Asserts |
|------|----------|---------|
| `TestToolPanelReindexOrdersAndClamps` | 6 rows (3 done, 3 running), `Selected` set to a running row scrolled out of view, `Scroll` deliberately too large | after `reindex(rows)`: `ordered == orderToolIndices(rows)` (running-first, done-recent-first) **and** `Scroll` moved so `Selected` is inside the visible window |
| `TestToolPanelReindexEmptyRows` | `rows = nil`, arbitrary `Scroll`/`Selected` | `ordered` empty, `Scroll == 0` (mirrors `clampToolScroll` empty-list rule) |
| `TestToolPanelReindexIdempotent` | call `reindex` twice on same rows | second call leaves `ordered` and `Scroll` unchanged (clamp is idempotent) - proves the §3B redundant-clamp-on-cold-path claim |
| `TestToolPanelReindexMatchesOldIdiom` | table: same inputs through `reindex` vs. the literal `ordered=orderToolIndices; Scroll=clampToolScroll(...)` | field-by-field equality - the regression guard proving zero behavior change |

GREEN: implement `reindex` per §3A. All four tests pass; existing
`toolpanel_test.go` + the five touched call sites' tests remain green.

## 6. Execution (ADLC - Fast Path)

This change is ≤5 effective lines of new production logic in a single file plus
mechanical call-site edits, with no new types and no config → **Fast-Path-eligible**
(ADLC `05`: skip Steps 0–3, go to Step 4; Step 5 uses **1** hostile auditor, not 3–4).

| Step | ADLC phase | Action | Gate |
|------|-----------|--------|------|
| 4a | RED | Add the 4 tests + no-op `reindex` stub to `toolpanel_test.go`/`toolpanel.go` | `go test -run 'TestToolPanelReindex' ./internal/cli/` → assertion failures (not compile errors) |
| 4b | GREEN | Fill in `reindex` body (§3A) | `go test -run 'TestToolPanelReindex' ./internal/cli/` → PASS |
| 4c | REFACTOR call sites | Convert the 5 production sites + 7 test sites to `m.toolPanel.reindex(m.toolRows)`; preserve `tui.go:341` guard per §3B | `go build ./internal/cli && go test ./internal/cli -count=1` → PASS |
| 5 | Audit (1 auditor) | Hostile review: confirm zero behavior diff, confirm out-of-scope sites untouched, confirm `livepanel.go`/`tui_selection.go` still compile and behave | Auditor reports 0 confirmed bugs; rejected findings backed by the `TestToolPanelReindexMatchesOldIdiom` table |
| 6 | Commit | `refactor(cli): extract toolPanelState.reindex for order+clamp idiom (P2.5)` | `go vet ./... && go test -race ./internal/cli/...` PASS; tree clean |

**Invariant check:** `internal/cli` is on the invariant list (ADLC `05`, "Invariant
Enforcement"). Before Step 4 read `.mivia/invariants.md` and run invariant tests; this
refactor introduces no new boundary, status string, untrusted-data path, or ownership
change, so no invariant is in tension - note that explicitly in the commit body.

## 7. Verification commands

```text
go test -run 'TestToolPanelReindex' ./internal/cli/ -count=1   # RED→GREEN proof
go test ./internal/cli/ -count=1                                # whole package
go test -race ./internal/cli/... -count=1                       # race (ADLC wave/Step 6 gate)
go vet ./...
go build ./...
make structure-check   # file/function size caps - new method is tiny; confirm still green
make verify            # if available
```

## 8. Rollback

Pure refactor with no behavior change: revert the commit. No migration, no config, no
persistence. The two-line idiom is trivially restored at each site. If any
`TestToolPanelReindexMatchesOldIdiom` table row fails during GREEN, that is proof of a
behavior diff - **halt, revert, return to Step 0** per ADLC rejection rules (do not
"tune" `clampToolScroll` to make it pass).

## 9. Risk assessment

- **Behavior risk:** NONE intended. Fully covered by the
  `TestToolPanelReindexMatchesOldIdiom` table, which runs both forms against identical
  inputs and asserts field equality.
- **Structural risk:** NONE. The method joins four existing `*toolPanelState` methods in
  `toolpanel.go`; cohesion improves (the order+clamp rule was duplicated, now it has one
  home). No new abstraction, no speculative generality.
- **Scope-creep risk:** the two order-only / local-var sites (§3C, §3D) are explicitly
  left alone with rationale; converting them would either waste work or misapply a
  mutator. This is noted to prevent a future "why not here too" edit from introducing a
  behavior change.
- **Fast-Path fit:** confirmed - single new function (≤5 LOC body), single new-file
  concept (method on existing type), no exported surface, fully test-pinned.
