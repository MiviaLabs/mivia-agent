# P1.5 — Delete dead code in `internal/cli`

**Status:** Source-verified plan; not yet implemented.
**Date:** 2026-07-30
**Depends on:** nothing.
**Blocks:** P1.1 (`tuiUserCardBg` lives in the `tui.go` style block P1.1 consolidates — removing it first shrinks that diff), P2.3 (`newSessionDispatcher*` deletion reduces the constructor surface P2.3 collapses), P2.5 (the dead `applyToolEndFromBus`/`applyToolStartFromBus` duplicate the `reindex()` idiom P2.5 promotes).
**Blast radius:** LOW — pure deletion of verified-unreachable / no-non-test-caller code; no behavioral change, no API change, no config or ledger change. One existing test becomes dead and is deleted alongside its function; one test-only helper is inlined or dropped.
**SoT:** this file.
**Review finding:** `.mivia/reports/cli-internal-refactoring-review.md` §P1.5.

---

## Problem

The structural review identified eight dead-code sites in `internal/cli` (see table). Each has been verified at HEAD to have **zero non-test callers**; several carry self-incriminating comments ("Retained for tests", "kept for API compatibility … Returns empty", "useful later", "legacy, test-only"). Dead code inflates the surface ahead of the larger P1.1–P1.4 refactors, invites copy-paste from the wrong variant (e.g. `applyToolEventFromBus` duplicates the live `applyToolEventsOpts` bridge path), and silently rots (e.g. `formatModelHeader` returns `""` but is still a documented entry point).

The deletions are mechanical and isolated, but **callers can appear between plan and implementation.** Every micro-task must therefore re-verify zero callers at edit time before deleting, not trust the table below.

---

## Goals and non-goals

**Goals**

- Delete all eight verified-dead sites listed in the finding.
- Remove the two tests whose only reason to exist is to keep dead code green (`TestFormatModelHeader_NoChrome`, `TestMakeAgentUIWithRenderer`).
- Keep `go build ./... && go vet ./... && go test ./internal/cli/...` green after each wave.
- Preserve every live code path and the public API.

**Non-goals**

- Consolidating the `tui*Style` block (that is P1.1; this plan only removes the single unused `tuiUserCardBg` var).
- Collapsing the `NewSessionDispatcher*` constructor explosion (P2.3; this plan only removes the two unexported wrappers).
- Promoting the `reindex()` idiom (P2.5).
- Any behavioral change, refactoring of live code, or test additions beyond the deletions.

---

## Approach

Each item is a **Fast-Path-eligible micro-task** (≤5 lines deleted, single file, no new types) under ADLC §Fast Path: skip Steps 0–3, delete in Step 4, one hostile auditor in Step 5, normal commit in Step 6. They are grouped into three waves only to keep each commit small and independently revertible. The hard rule for every task is the same:

> **Before deleting, re-run the grep in the Verification column against `internal/cli` (and, for public symbols, the whole repo). If a non-test caller now exists, STOP and surface it — do not delete.** The review verified zero callers at HEAD; that fact is stale the moment any other change lands.

Because this is deletion of already-unreachable code, the ADLC RED-then-GREEN TDD loop does **not** add a new failing test first (there is no new behavior to pin). The "test" for each micro-task is the existing suite staying green *after* the removal; the regression guard is `go test -race ./internal/cli/...`. The two tests being removed are explicitly dead (they assert that a function returns `""` / that a legacy handler runs) and are deleted with their functions.

### Dead-code table

| # | Symbol | File:line (HEAD) | LOC | Re-verify zero callers (run at edit time) | Risk | Test impact |
|---|---|---|---|---|---|---|
| D1 | `applyToolEventFromBus` | `tui_events.go:108` | ~100 (incl. D2, D3) | `grep -rn 'applyToolEventFromBus' internal` → only the def | LOW. Duplicates live `applyToolEventsOpts` bridge path; comment says "Retained for tests". | None |
| D2 | `applyToolStartFromBus` | `tui_events.go:117` | (part of D1 block) | `grep -rn 'applyToolStartFromBus' internal` → only the def + D1's call | LOW | None |
| D3 | `applyToolEndFromBus` | `tui_events.go:160` | (part of D1 block) | `grep -rn 'applyToolEndFromBus' internal` → only the def + D1's call | LOW | None |
| D4 | `newSessionDispatcher` | `dispatcher.go:114` | ~3 | `grep -rn 'newSessionDispatcher\b' internal` (word boundary) → only the def + its own body. **Note:** `session_tool_budget_test.go:49` calls the *exported* `NewSessionDispatcher`, not this wrapper — confirm the match is lowercase. | LOW | None |
| D5 | `newSessionDispatcherWithContext` | `dispatcher.go:118` | ~3 | `grep -rn 'newSessionDispatcherWithContext\b' internal` → only the def + D4's body. Distinguish from the *live* `newSessionDispatcherWithContextAndBudget` (`:122`). | LOW | None |
| D6 | `renderLabeledBody` | `messagebubble.go:424` | ~44 | `grep -rn 'renderLabeledBody' internal` → only the def + its own fallback call into D7 | LOW. `Render` goes through `renderBodyLines`/`renderPlain`, not here. | None |
| D7 | `renderStacked` | `messagebubble.go:469` | ~29 | `grep -rn 'renderStacked' internal` → only the def + D6's call | LOW | None |
| D8 | `renderHalfBlocks` | `pixel.go:202` | ~18 | `grep -rn 'renderHalfBlocks' internal` → only the def | LOW | None |
| D9 | `logoFramesLegacy` | `logo.go:253` | ~14 | `grep -rn 'logoFramesLegacy' .` (whole repo, incl. tests) → only the def. Finding says no test references it. | LOW | None |
| D10 | `formatModelHeader` | `msgcard.go:52` | ~6 | `grep -rn 'formatModelHeader' internal` → def + `msgcard_test.go:64` only | LOW | **Delete `TestFormatModelHeader_NoChrome`** (`msgcard_test.go`) — its entire body asserts these two funcs return `""`. |
| D11 | `formatModelFooter` | `msgcard.go:59` | ~4 | `grep -rn 'formatModelFooter' internal` → def + `msgcard_test.go:67` only | LOW | Deleted with D10 |
| D12 | `tuiUserCardBg` | `tui.go:27` | 1 | `grep -rn 'tuiUserCardBg' internal` → only the def | LOW | None |
| D13 | `makeAgentUIWithRenderer` | `classic_agent_ui.go:209` | ~4 | `grep -rn 'makeAgentUIWithRenderer' internal` → def + `renderer_test.go:156` only | LOW | **Resolve `TestMakeAgentUIWithRenderer`** (`renderer_test.go:152`): either inline `makeAgentUIWithRenderer` at the call site (it is a 2-line `newClassicAgentHandler(r)` wrapper) or delete the test if `newClassicAgentHandler` is already covered elsewhere. Prefer inlining so the test's event-routing assertions survive. |

### Notes baked into the table

- **Word boundaries matter.** D4/D5 share a stem with the live `newSessionDispatcherWithContextAndBudget` and the exported `NewSessionDispatcher*` family; the grep must use `\b` / `--word-regexp` and the implementer must confirm the match is the exact lowercase identifier, not a prefix of a live symbol.
- **`renderer_test.go` inlining (D13).** The wrapper is:
  ```go
  func makeAgentUIWithRenderer(r *ChatRenderer) func(agent.Event) {
      _, h := newClassicAgentHandler(r)
      return h
  }
  ```
  Inlining means replacing `handler := makeAgentUIWithRenderer(r)` at `renderer_test.go:156` with `_, handler := newClassicAgentHandler(r)` and deleting the wrapper. The test name can stay.
- **`msgcard_test.go` (D10/D11).** `TestFormatModelHeader_NoChrome` asserts *only* that these two functions return `""`. It exists to keep the stubs green. Delete the whole test function with the stubs.
- **Self-incriminating comments.** The deletion also removes the comments above D1 ("Retained for tests…"), D10/D11 ("kept for API compatibility … Returns empty"), D8 ("useful later"), D9 ("not used in normal path"), and D13 ("legacy, test-only") — these are the dead-code markers the review cited.

---

## Implementation waves

Each wave ends with the wave gate. One commit per wave (or per item) is acceptable; keep commits independently revertible.

### Wave 1 — TUI event/render dead code (D1–D3, D6–D9)

- **D1–D3** `tui_events.go`: delete `applyToolEventFromBus` (108), `applyToolStartFromBus` (117), `applyToolEndFromBus` (160) — the full ~100-LOC block, plus the `refreshToolPanelIfWaiting` call site *only if* it becomes orphaned (re-grep `refreshToolPanelIfWaiting` first; it is also reached from the live path at `tui_events.go:212+`, so do **not** delete it unless the re-grep shows otherwise).
- **D6–D7** `messagebubble.go`: delete `renderLabeledBody` (424) and `renderStacked` (469).
- **D8** `pixel.go`: delete `renderHalfBlocks` (202).
- **D9** `logo.go`: delete `logoFramesLegacy` (253).

**Wave gate:** `go build ./internal/cli/... && go vet ./internal/cli/... && go test -race ./internal/cli/...`

### Wave 2 — Dispatcher + tui style dead code (D4, D5, D12)

- **D4–D5** `dispatcher.go`: delete `newSessionDispatcher` (114) and `newSessionDispatcherWithContext` (118). Leave `newSessionDispatcherWithContextAndBudget` (122) and the exported `NewSessionDispatcher*` family untouched.
- **D12** `tui.go`: delete the `tuiUserCardBg` var (27). Do not touch the surrounding `tui*Style` block — that is P1.1.

**Wave gate:** same as Wave 1.

### Wave 3 — Dead stubs + their tests (D10, D11, D13)

- **D10–D11** `msgcard.go`: delete `formatModelHeader` (52) and `formatModelFooter` (59). **Same edit** deletes `TestFormatModelHeader_NoChrome` in `msgcard_test.go`.
- **D13** `classic_agent_ui.go`: inline `makeAgentUIWithRenderer` into `renderer_test.go:156` (`_, handler := newClassicAgentHandler(r)`) and delete the wrapper; or, if `newClassicAgentHandler` is already covered, delete `TestMakeAgentUIWithRenderer` too. Decide by grepping `newClassicAgentHandler` across `*_test.go` first.

**Wave gate:** same as Wave 1, plus confirm no remaining references to any deleted symbol (see Verification).

---

## Verification

Run from repo root. **Every grep must return only the symbol's own definition (and its known test reference, where listed) before deletion.**

```bash
# 0. Re-verify zero non-test callers at implementation time (per item — see table).
grep -rn --word-regexp 'applyToolEventFromBus|applyToolStartFromBus|applyToolEndFromBus' internal
grep -rn --word-regexp 'newSessionDispatcher'            internal   # mind the WithContextAndBudget + exported family
grep -rn --word-regexp 'newSessionDispatcherWithContext\b' internal
grep -rn 'renderLabeledBody|renderStacked' internal
grep -rn 'renderHalfBlocks'   internal
grep -rn 'logoFramesLegacy'   .          # whole repo incl. tests
grep -rn 'formatModelHeader|formatModelFooter' internal
grep -rn 'tuiUserCardBg'      internal
grep -rn 'makeAgentUIWithRenderer' internal

# 1. Build + vet + race tests after each wave and at the end.
go build ./... && go vet ./...
go test -race ./internal/cli/...

# 2. After all waves: nothing references any deleted symbol anywhere.
grep -rn --word-regexp \
  'applyToolEventFromBus|applyToolStartFromBus|applyToolEndFromBus' .
grep -rn --word-regexp 'newSessionDispatcher\b|newSessionDispatcherWithContext\b' .
grep -rn 'renderLabeledBody|renderStacked|renderHalfBlocks|logoFramesLegacy' .
grep -rn 'formatModelHeader|formatModelFooter|tuiUserCardBg|makeAgentUIWithRenderer' .

# 3. Full repo gate (touches only internal/cli, but confirm no cross-package reference).
make verify   # or: go test ./... -count=1
```

**ADLC note (Fast Path):** this is deletion of already-unreachable code, so there is no RED phase with a new failing test. The regression guard is the existing `internal/cli` suite staying green post-deletion. Step 5 uses **one** hostile auditor (Fast Path), not 3–4.

**Mutation check (sanity, not a formal mutation-proof requirement):** after deleting a function, a deliberate re-insertion of a single call to it from a live path should fail to compile — confirming the symbol is genuinely gone and not silently aliased.

---

## Rollback

Each item is independently revertible by `git revert <commit>` (one commit per wave, or per item). Because nothing live calls any deleted symbol, reverting restores dead code without behavioral effect. No data, config, ledger, or API migration is involved.

**Kill condition for the whole plan:** if the implementation-time re-grep (Verification step 0) reveals a **non-test caller** for any item that the review called dead, stop, do not delete that item, and update the review finding — the caller appeared between review and implementation. Items with confirmed callers are carved out; the rest of the plan proceeds.
