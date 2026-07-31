# Centered, content-sized TUI dialogs

**Status:** Revised after hostile Step 0 review; implementation-ready only after
the preflight gate passes. Not started.
**Date:** 2026-07-31
**SoT:** .mivia/plans/tui-centered-dialogs.md
**Scope:** /help, /status, /tools, /sessions, block-detail pager, and
fleet-detail pager. Fleet detail is not a third dialog implementation: it is
another blockOverlay producer through openFleetOverlay.

## Goal and decision

Render modal reference material as a bounded panel over the already-rendered chat
frame. Keep the chat visible, keep modal keyboard ownership, prevent all mouse
input from reaching transcript/composer controls, and keep pager scroll clamping
tied to the exact panel geometry that was rendered.

Do not upgrade Bubble Tea or Lip Gloss in this change. Lip Gloss v1.1.0 has no
layer/canvas compositor; the current module already includes
github.com/charmbracelet/x/ansi, whose ANSI-aware Cut/CutWc functions are the
safer cell-slicing primitive. Reuse it rather than duplicating an ANSI parser.
Upgrade to Lip Gloss v2 is a separate dependency plan.

## Hostile review verdict

The original plan had the right product direction but was not implementation
ready. It lacked exact ownership, a geometry handoff, modal mouse routing, a
base-canvas normalization contract, a resize rule, and the ADLC task breakdown.
Those omissions could produce a visually centered dialog with broken paging,
stale hit testing, clipped base lines, or an unscrollable modal.

### Confirmed findings and dispositions

| ID | Finding | Disposition |
|---|---|---|
| H1 | fleet detail is created as the same blockOverlay used by block detail. | One shared pager policy; no fleet-specific frame or type. |
| H2 | tuiHitMap invalidation alone does not prevent the mouse path from trying the stale map before modal handling. | Add an early modal mouse router before right-click copy and viewport fallback. |
| H3 | A wholesale mouse swallow would also swallow modal wheel scrolling. | Modal router consumes clicks/right-clicks; routes wheel to the active pager or session cursor. |
| H4 | View can produce short/ragged lines while overlayAt needs a terminal-sized cell canvas. | Normalize the chat base to exactly termW by termH cells before compositing. |
| H5 | Recomputing page height from terminal height repeats the existing mismatch. | Compute one dialogLayout from one helper and pass pageH to render, key, wheel, and resize paths. |
| H6 | Resizing can leave yOffset beyond the new page maximum before the next key. | Clamp modal state on WindowSizeMsg and before rendering after a geometry change. |
| H7 | A custom left cutter would be SGR-fragile and visibleWidth undercounts emoji/combining cases. | Use ansi.Cut or ansi.CutWc and lipgloss.Width/ansi.StringWidth for compositor cells. |
| H8 | Content-sized had no formula; producers can emit rows wider than the panel. | Define exact preferred/min/max dimensions and truncate rows to inner width. |
| H9 | Existing substring tests can pass while centering/compositing is wrong. | Add cell-grid positional tests, modal input tests, resize tests, and a TUI journey gate. |
| H10 | There was no clean-build preflight or rollback criterion. | Add both as hard gates; record the current baseline blocker below. |
| H11 | ADLC task rows omit API, command, timeout, and context scope, and group multiple files under one task. | Expand every task into the required record schema before Step 1; split tests, manifests, docs, and audits by file. |
| H12 | New invariants have no exact test-to-row mapping. | Define the mapping and allocate the lowest free INV-TUI IDs at landing. |
| H13 | The focused regex omits several planned dialog tests. | Use a selector that includes every proposed dialog, modal, resize, and integration test. |
| H14 | The recorded baseline blocker is stale after the shared worktree changed. | Make P0 rerun the baseline; record the current review snapshot provisionally below. |
| H15 | The explicit structure gate omits staged-file size, and delivery omits hook/commit checks. | Use make structure-check, make pre-commit, make pre-push, make commit-check, and forbid hook bypasses. |
| H16 | Help content is pre-truncated at open time, and status has no many-subagent overflow policy despite being non-paging. | Store raw help content for current-width rendering and require a compact status summary when rows do not fit. |
| H17 | x/ansi cell cutting does not by itself prove SGR continuity when a slice starts inside an active style. | Make sliceANSI state-aware, re-emit active SGR, reset at slice ends, and test partial resets at both seams. |
| H18 | An asynchronous paste can arrive after a modal opens and leak into the hidden composer. | Guard paste messages at the modal boundary and add a transition-race test tied to INV-TUI-24. |
| H19 | Bubble Tea's modern Action/Button fields differ from deprecated Type fixtures. | Route using Action/Button/IsWheel and retain legacy fixtures only as compatibility coverage. |
| H20 | Transition owners are distributed across slash handlers, cancel, and key paths. | Route all owners through one open/close invalidation helper and list each owner as a separate task. |
| H21 | Snapshot status/fleet semantics conflict with comments that describe live state. | Make captured-at-open semantics explicit in copy and test reopen as the refresh action. |
| H22 | Resize can preserve the wrong scroll anchor when wrapping changes display-row indexes. | Preserve the first logical source line when possible, then map and clamp to the new display rows. |

## Current ground truth

At HEAD, renderChatView early-returns to sessionsDialog.View or
blockOverlay.View before building the chat frame (internal/cli/tui_view.go).
blockOverlay.View fills terminal dimensions and derives its page from termH - 4
(internal/cli/overlay.go); sessionsDialog.View ignores h and uses the fixed
sessionsDialogRows = 12 (internal/cli/sessions_dialog.go). /help, /status, and
/tools produce blockOverlay values (internal/cli/dialog_tui.go), while
openFleetOverlay does the same (internal/cli/fleetbox.go).

The right-click copy path runs before handleMouseMsg
(internal/cli/tui_message.go), and tuiHitMap.hit only models y ranges
(internal/cli/tui_hitmap.go). Therefore this change must suppress transcript
dispatch at the message router, not pretend that a centered rectangle can be
represented by the current one-dimensional hit map.

The repository is shared with other active agents, so this is a review snapshot,
not a stable baseline. The latest independent verification reported:

    go build ./...                                  PASS
    go test -count=1 ./...                         FAIL: internal/cli/attribution_test.go:96
    git diff --check                               PASS

The full-test failure was reported at `internal/cli/attribution_test.go:96` in
`TestToolPanelRowShowsAgentBadge`, with additional active-worker warnings. P0 must rerun these commands after other
agents release the tree and record the exact owner/resolution of any remaining
failure. Do not overwrite or repair another agent's changes as part of this plan.

## Revised design

### 1. One concrete geometry value

Keep the abstraction package-local to internal/cli; no interface or new package
is justified. Add focused geometry/compositor files rather than making overlay.go
or tui_view.go grow into another mixed-responsibility file.

    type rect struct { x, y, w, h int }

    type dialogPrefs struct {
        preferredW int
        preferredH int
        preferredWPct, preferredHPct int
        minW, minH int
        maxWPct, maxHPct int
        pager bool
    }

    type dialogLayout struct {
        rect       rect
        innerW     int
        pageH      int
        frameCols  int
        frameRows  int
        borderless bool
    }

    func dialogRect(termW, termH int, p dialogPrefs, contentW, contentH int) rect
    func makeDialogLayout(termW, termH int, p dialogPrefs,
        measure func(innerW int) (contentW, contentH int)) dialogLayout
    func wrapDisplayRows(lines []string, innerW int) []string

Contract:

- Raw terminal dimensions are preserved for canvas bounds. Logical layout
  minimums are used only when choosing preferred geometry; no logical rectangle
  may exceed raw termW by termH, and no negative dimensions or divide-by-zero are
  possible.
- The outer rectangle is clamped to the terminal, then centered with stable
  floor-left/top rounding for odd spare dimensions.
- A non-zero preferred pixel size wins; otherwise a non-zero preferred
  percentage uses the terminal dimension; otherwise the content size is used.
  The result is then capped by max percentages and expanded to minimums.
  Content-sized dimensions include frame rows/cols. This makes the block/fleet
  pager's near-full policy representable without a second geometry path.
- Minimums are honored when the terminal can fit them. If the terminal is smaller
  than a minimum, the rectangle is reduced to the terminal rather than overflow.
- A two-pass render is mandatory when wrapping affects height: choose width,
  wrap/measure semantic rows at that inner width, choose height, then reuse the
  resulting display-row snapshot for View, key paging, wheel paging, and resize
  clamping. No path independently measures content.
- `makeDialogLayout` owns that two-pass measurement through its callback; the
  callback receives the candidate inner width and returns measured display width
  and height. `dialogRect` remains pure for table tests, but production callers do
  not pre-measure with a different width.
- In framed mode, `frameCols` and `frameRows` are exact shared constants:
  `frameCols=4` for two border cells plus two padding cells, and frame rows are
  title plus footer/bottom rows for the chosen producer. Thus
  `innerW=max(1,rect.w-frameCols)` and `pageH=max(1,rect.h-frameRows)`. In
  borderless mode both are zero and `pageH=rect.h`.
- The block/fleet frame is exactly one title row, `pageH` display rows, and one
  footer/bottom row. Sessions is exactly one title row, its visible rows, one
  footer row, and one bottom row. A sessions “N more” indicator consumes one of
  the visible rows; the confirmation footer consumes the footer row and is not
  counted as a selectable session.
- All four paths use makeDialogLayout: renderChatView, modal key routing, modal
  wheel routing, and WindowSizeMsg clamping. No modal path reads termH-4 directly.
- If the terminal cannot fit the frame chrome, set dialogLayout.borderless=true,
  frameCols=0, frameRows=0, and render a clipped fallback that still returns
  exactly the raw terminal canvas. Test 1x1, 10x4, 20x6, and 39x10.

The production composite path passes the already-created dialogLayout through.
Compatibility View(w, h) test helpers may create one through makeDialogLayout.

### 2. Cell-safe composition

Use ansi.Cut and ansi.StringWidth for grapheme width consistently. Do not mix
ansi.CutWc, lipgloss.Width, or the local CJK-only visibleWidth for compositor
coordinates. Encode this decision in tests for ANSI styles, emoji, CJK,
combining marks, variation selectors, ZWJ sequences, and chat-header glyphs.
The chat base uses the simple diamond; the braille welcome hero is outside this
modal scope. A grapheme wider than the available row is emitted intact on its
own clipped-safe row, never split.

    func normalizeCanvas(s string, termW, termH int) []string
    func overlayAt(base, panel string, panelRect rect, termW, termH int) string
    func sliceANSI(line string, left, right int) string

normalizeCanvas pads/truncates every base row to termW cells and returns exactly
termH rows. For block/fleet content, wrap long logical lines into display rows
with `wrapDisplayRows` before paging; never discard cells with an ellipsis. The
same wrapped rows must be used by ViewAt and scroll clamping. On resize, preserve
the first logical source line that was visible when possible; if its wrapped-row
index changes, anchor at that line's first new display row and then clamp to the
new final page. Other reference dialogs may truncate producer rows to their
declared policy width. `overlayAt` treats base and panel as ANSI-bearing cell
strings, replaces only the panel rectangle, resets at both seams, and returns
exactly termH newline-separated rows of width termW. It is safe when a panel is
partly outside a tiny terminal; geometry should normally prevent this, but the
compositor remains fail-closed.

`sliceANSI` may use x/ansi for grapheme-aware cell slicing, but x/ansi alone is
not the style contract: it must carry active SGR state into a slice that starts
mid-row, re-emit that state at the slice start, and append a reset at the slice
end. Partial resets such as 22m, 39m, and 49m update the carried state. Every
normalized row and panel row is self-contained for this reason. Tests cover a
style opened before the panel, a reset inside the panel, and a style reopened
after the panel seam.

Use one renderDialogFrame(title, rows, footer, layout) for blockOverlay and
sessionsDialog. The frame owns border, title, row fill, footer truncation, and
exact dimensions. Content owners retain only state-specific rows and input.
Frame chrome constants define whether rows include title, footer, and borders;
tests must assert the arithmetic instead of relying on the old overlayPageH
comment.

### 3. Modal input and resize

Add a modal branch before right-click copy, handleMouseMsg, and textarea/viewport
fallback:

- overlay: inspect Bubble Tea's modern `MouseMsg` Action/Button fields and
  `IsWheel()` first (retain legacy Type fixtures only for compatibility tests);
  wheel changes yOffset using layout.pageH; left/right/middle and right-click
  are consumed; no transcript copy, selection, composer focus, or viewport
  scroll occurs;
- sessions: wheel moves the cursor using the same visible-row count used by
  rendering; all clicks are consumed;
- no modal: preserve current hit-map and viewport behavior unchanged.

The modal branch returns immediately after handling or swallowing the message.
It must set no fallthrough flag that can reach copy, textarea, viewport, or a
stale hit map later in the same Update. The quit-arm exception is explicit:
global ctrl+c/ctrl+q behavior may still disarm or quit, but modal mouse input
cannot mutate chat selection, copy, focus, or viewport state. Test each modal
transition both before and after the next View, because a close followed by a
same-frame click must not ghost-hit the old chat map. Async paste messages need
the same guard: `pasteTextMsg` arriving after a modal opens is swallowed and
covered by `TestAsyncPasteDroppedWhileModalOpens`.

Keep global ctrl+c/ctrl+q precedence unchanged. Modal keys remain modal; modal
mouse input is modal too. tuiHitMap need not gain x extents.

On tea.WindowSizeMsg, invalidate the hit map, update dimensions, clamp active
modal state against the newly computed layout, then render. Opening or closing a
modal also invalidates the hit map. Dialog content is a snapshot at open time for
status and fleet detail, matching current behavior; resize reflows/wraps that
snapshot, but does not silently introduce a live-refresh contract. This is an
intentional product decision for this change: title/help copy must say “captured
at open” or equivalent, and reopening is the documented refresh action. Live
refresh while a modal is open is separate work.

Help stores untruncated semantic descriptions and renders them against the
current inner width on every View; it must not call `truncateToWidth` using the
opening width. Add a resize test that opens help narrow, widens it, and verifies
the full current-width content is available.

### 4. Sizing policy

These are outer dimensions and must be named prefs, not scattered magic values:

| Surface | Width | Height | Pager |
|---|---:|---:|---|
| /status | 60 preferred, min 32 | content, min 8 | no; summarize agent rows on overflow |
| /tools | 50 preferred, min 28 | content capped at 60% terminal, min 8 | yes if needed |
| /help | 76 preferred, min 40 | content capped at 70% terminal, min 8 | yes |
| /sessions | 70 preferred, min 40 | rows plus frame, capped by terminal | cursor window |
| block/fleet detail | 90% preferred, min 40 | 85% preferred, min 8 | yes |

Status and every other producer truncate each row to the frame inner width at
render time. Constructors must not assume the eventual terminal width. Because
status is intentionally non-paging, an overflowed agent list is replaced by the
core session/current-turn facts plus a compact count such as `agents: N (open
fleet for details)`; it never silently clips the last fact or pretends that all
agent rows are visible. `TestStatusDialogOverflowPolicy` covers many agents
before and after a narrow resize.

## Exact change surface

Files to create:

- internal/cli/dialog_geometry.go - rect, prefs, layout, and pure helpers.
- internal/cli/dialog_compositor.go - canvas normalization, overlayAt, shared frame.
- internal/cli/dialog_geometry_test.go - table-driven geometry and resize cases.
- internal/cli/dialog_compositor_test.go - ANSI/cell and exact canvas cases.
- internal/cli/dialog_tui_test.go - producer prefs and snapshot-policy cases.
- internal/cli/dialog_input_test.go - modal key/mouse/paste transition tests,
  including before/after View hit-map invalidation.
- internal/cli/dialog_program_test.go - one Bubble Tea program journey covering
  open, resize, wheel, close, and post-modal chat input.

Files to modify:

- internal/cli/overlay.go - shared frame/layout; scroll accepts pageH, not termH;
  preserve full-content, redaction, and pager semantics.
- internal/cli/sessions_dialog.go - shared frame; visible rows derive from layout.
- internal/cli/dialog_tui.go - named prefs and producer row contract.
- internal/cli/fleetbox.go - shared near-full pager policy, no new type.
- internal/cli/tui_view.go - always render base, normalize, make one layout, compose.
- internal/cli/tui_message.go - modal mouse gate before copy and fallback; clamp
  modal state in its existing WindowSizeMsg owner, with no duplicate resize path.
- internal/cli/tui_slash_handlers.go, internal/cli/tui_cancel.go, and
  internal/cli/tui_keys.go - route every modal open/close owner through the
  centralized transition helper so each transition invalidates tuiHitMap once.
- internal/cli/keymap_test.go and internal/cli/tui_removed_keys_test.go - update
  help-constructor callers if the width-taking constructor is removed; preserve
  a compatibility wrapper only if it can still retain raw semantic content.
- internal/cli/tui_view_test.go - positional, resize, and base-visible coverage.
- internal/cli/tui_mouse_test.go - modal transition and no-ghost-hit coverage.
- internal/cli/overlay_test.go - pager wrapping, page height, resize, and ANSI
  preservation coverage.
- internal/cli/sessions_dialog_test.go - dynamic row and cursor policy coverage.
- internal/cli/fleetbox_test.go - snapshot and shared pager policy coverage.
- .mivia/invariants.md - accepted invariants with exact test names.
- Makefile - add every new invariant test name to the make invariants selector.
- docs/architecture/overview.md - durable TUI base-plus-modal boundary.
- docs/development/terminal-input.md - owned manual modal mouse/resize checks.

No dependency upgrade is planned. If direct use of x/ansi requires changing its
go.mod classification, run go mod tidy and include only intentional module
metadata changes.

## ADLC implementation waves

The following is the locked breakdown after Step 0. Every row is a complete task
record with `ID`, `Wave`, `File`, `Type`, `API`, `Depends on`, `Verification
command`, `Timeout`, and `Context scope`. A review-only row uses `none
(read-only)` as its file and names its exact review scope; grouped write tasks are
not allowed. Timeouts are per command and must be recorded in the report.

| ID | Wave | File | Type | API | Depends on | Verification command | Timeout | Context scope |
|---|---:|---|---|---|---|---|---:|---|
| P0 | 0 | worktree | preflight | none | none | `go build ./...`; `go test -count=1 ./...`; `git diff --check` | 5m | shared-worktree status and baseline only |
| G1 | 1 | `dialog_geometry_test.go` | RED | `dialogRect`, `makeDialogLayout` | P0 | focused geometry tests fail for absent API | 1m | named geometry and tiny-terminal cases |
| G2 | 1 | `dialog_geometry.go` | GREEN | rect/prefs/layout/wrapDisplayRows | G1 | `go test ./internal/cli -run 'TestDialog(Rect|Views|Resize)' -count=1` | 2m | one geometry implementation file |
| R1 | 1 | none (read-only) | review | geometry contract | G1/G2 | inspect diff; rerun focused geometry tests | 2m | geometry, resize, and tiny fallback |
| C1 | 2 | `dialog_compositor_test.go` | RED | `normalizeCanvas`, `overlayAt`, `sliceANSI` | R1 | compositor tests fail for absent API | 1m | cell grid, ANSI state, and seam cases |
| C2 | 2 | `dialog_compositor.go` | GREEN | canvas/compositor/frame helpers | C1 | focused compositor tests plus `go test -race ./internal/cli -run 'TestDialog.*(Canvas|ANSI)'` | 3m | exact canvas and style continuity |
| R2 | 2 | none (read-only) | review | compositor seams | C1/C2 | inspect diff; `git diff --check` | 1m | no byte slicing, no ANSI bleed |
| O1 | 3 | `overlay_test.go` | RED | blockOverlay `ViewAt`/scroll API | G2/C2 | pager tests fail for absent API | 1m | wrapping, page height, anchor, redaction |
| O2 | 3 | `overlay.go` | GREEN | shared frame and display-row pager | O1 | `go test ./internal/cli -run 'TestBlockOverlay' -count=1` | 2m | block and fleet-compatible pager |
| S1 | 3 | `sessions_dialog_test.go` | RED | sessions layout/render API | G2/C2 | dynamic-row tests fail for absent API | 1m | visible rows, cursor, deletion, footer |
| S2 | 3 | `sessions_dialog.go` | GREEN | shared frame and cursor window | S1 | `go test ./internal/cli -run 'TestSessionsDialog' -count=1` | 2m | sessions only; no second frame |
| R3 | 3 | none (read-only) | review | shared frame | O2/S2 | inspect producers and run focused pager tests | 2m | frame arithmetic and duplicate geometry |
| V1a | 4 | `tui_view_test.go` | RED | base-plus-panel View path | R3 | positional view tests fail for absent compositor path | 1m | chat remains visible behind dialogs |
| V1b | 4 | `tui_mouse_test.go` | RED | modal transition/hit-map API | R3 | before/after View hit-map tests fail | 1m | stale-map and ghost-click ordering |
| V2 | 4 | `tui_view.go` | GREEN | base render, layout handoff, compose | V1a/V1b | focused view and positional tests | 3m | one layout per render |
| M1 | 4 | `dialog_input_test.go` | RED | modal Update/key/mouse/paste API | V2 | modal input tests fail for absent gate | 1m | modern and legacy mouse fixtures, key precedence |
| M2 | 4 | `tui_message.go` | GREEN | modal gate, paste guard, resize clamp | M1 | focused modal tests plus `go test -race ./internal/cli` | 5m | event ordering and no fallthrough |
| M2a | 4 | `tui_slash_handlers.go` | GREEN | centralized modal-open transition | M1 | modal open tests pass; focused CLI tests | 2m | `/help`, `/status`, `/tools`, `/sessions` |
| M2b | 4 | `tui_cancel.go` | GREEN | centralized modal-close transition | M1 | close-before/after-View tests pass | 2m | global cancel and close paths |
| M2c | 4 | `tui_keys.go` | GREEN | fleet/block modal transitions | M1 | modal key precedence and fleet tests | 2m | key-open/key-close owners |
| R4 | 4 | none (read-only) | review | input ownership | V2/M2 | inspect Update ordering; run modal input tests | 2m | copy, focus, viewport, quit-arm exception |
| D0 | 5 | `dialog_tui_test.go` | RED | producer prefs/raw-content API | R4 | prefs, help reflow, status overflow tests fail | 1m | help/status/tools snapshot policy |
| D1 | 5 | `dialog_tui.go` | GREEN | named prefs and semantic rows | D0 | `go test ./internal/cli -run 'Test(Help|Status|DialogTUI)' -count=1` | 2m | no opening-width truncation |
| F0 | 5 | `fleetbox_test.go` | RED | fleet shared pager API | R4 | fleet policy test fails for absent wiring | 1m | fleet is blockOverlay producer |
| F1 | 5 | `fleetbox.go` | GREEN | openFleetOverlay shared policy | F0 | `go test ./internal/cli -run 'TestFleet' -count=1` | 2m | no fleet-specific dialog type |
| I1a | 5 | `.mivia/invariants.md` | GREEN | exact invariant rows | D1/F1 | `make validate-invariants` | 2m | lowest free INV-TUI IDs and test mapping |
| I1b | 5 | `Makefile` | GREEN | invariant selector | I1a | `make invariants` | 3m | every named dialog test is selected |
| DOC1a | 5 | `docs/architecture/overview.md` | GREEN | durable boundary text | D1/F1 | `make docs-check` | 2m | owned architecture documentation |
| DOC1b | 5 | `docs/development/terminal-input.md` | GREEN | manual checks | D1/F1 | `make docs-check` | 2m | owned terminal-input documentation |
| IT1 | 5 | `dialog_program_test.go` | RED/GREEN | Bubble Tea journey | V2/M2/D1/F1 | `go test ./internal/cli -run 'TestDialogProgramResizeIntegration' -count=1` | 3m | open/resize/wheel/close/chat journey |
| A1 | 6 | none (read-only) | hostile audit | geometry/compositor | all prior | review report with zero confirmed bugs or named blocker | 3m | exact canvas, style, sizing |
| A2 | 6 | none (read-only) | hostile audit | modal input | all prior | review report with zero confirmed bugs or named blocker | 3m | mouse, paste, key precedence, transitions |
| A3 | 6 | none (read-only) | hostile audit | pager/content | all prior | review report with zero confirmed bugs or named blocker | 3m | wrapping, anchors, overflow, redaction |
| A4 | 6 | none (read-only) | hostile audit | delivery gates | all prior | `make verify`; `make test`; `make race`; hooks | 10m | invariants, docs, structure, commit scope |

Implementation must return to Step 0 if the base canvas cannot be normalized
without style corruption, if x/ansi width policy conflicts with existing TUI
rendering, or if sessions cursor semantics cannot agree with visible rows. Do
not land a partial compositor or partial modal gate.

## Required tests and acceptance matrix

At minimum, add named tests for:

- TestDialogRectFitsAndCenters: centered even/odd spare dimensions, width/height
  clamps, content sizing, percentage caps, minimums, and zero/negative inputs;
- TestDialogViewsStayWithinTerminalBounds: exact canvas for 1x1, 10x4, 20x6,
  39x10, and normal terminals, including the borderless tiny fallback;
- TestDialogResizeRecentersAndReflows: wide to narrow to wide while open, with
  stable frame arithmetic and current-width row wrapping;
- TestDialogResizeClampsScroll: shrinking/growing while scrolled leaves the
  final display row reachable;
- TestDialogANSISeamsPreserveStyles: exact termW by termH, panel at expected
  x/y, base preserved outside the rectangle, ANSI styles at both seams,
  reset/no bleed, emoji/CJK/combining/variation-selector/ZWJ cells, short base
  rows, short panel rows, and tiny-terminal clipping;
- TestDialogCompositorExactCanvas: every returned row has exactly termW cells
  and the result has exactly termH rows, including a clipped borderless fallback;
- TestBlockOverlayPreservesLongLines: long code/output lines, CJK, emoji, and
  ANSI styling are wrapped or otherwise reachable without truncation; final
  display row is reachable at every policy size, page-down uses rendered pageH,
  resize clamps offset, and redaction remains intact;
- TestSessionsDialogUsesAvailableRows: visible row count derives from layout, a
  10-row terminal stays
  bounded, cursor/scroll clamp after resize and deletion, and all rows reachable;
- TestHelpReflowsAfterResize: help retains raw semantic descriptions and exposes
  the full current-width rendering after narrow-to-wide resize;
- TestStatusDialogOverflowPolicy: many agents preserve core status facts and
  emit an explicit compact count instead of silently clipping or paging status;
- TestStatusAndFleetSnapshotPolicy: status and fleet content remain snapshots
  until reopened, while resize reflows the snapshot and does not expose stale
  geometry;
- model rendering: chat remains visible behind every dialog, panel border columns
  are exact, and closing restores the same chat layout;
- modal input: right-click cannot copy a transcript block, clicks cannot focus
  composer or select transcript, overlay wheel scrolls only overlay, sessions
  wheel moves only sessions, and no modal event reaches viewport fallback;
- TestModalMouseEventsAreFullySwallowed: motion, release, left, middle, right,
  and wheel events cannot mutate chat state while a modal is open;
- TestModalOpenSwallowsMouseBeforeView and TestModalOpenSwallowsMouseAfterView:
  opening a modal blocks stale-map dispatch before and after the next View;
- TestModalCloseInvalidatesHitMapBeforeView and
  TestModalCloseRebuildsHitMapAfterView: closing cannot ghost-click old chat
  zones, regardless of render timing;
- TestModalKeyPrecedenceBeforeAndAfterView: ctrl+c, ctrl+q, esc, and q retain
  the documented precedence around transitions;
- TestAsyncPasteDroppedWhileModalOpens: an in-flight paste cannot land in a
  composer hidden behind a newly opened modal;
- TestDialogProgramResizeIntegration: one Bubble Tea program journey opens each
  modal, resizes, wheels, closes, and then types/copies in chat;
- end-to-end TUI journey: open every modal, resize while open, scroll, close,
  then copy/select/type in chat. Preserve existing INV-TUI-2, INV-TUI-6,
  INV-TUI-16, INV-TUI-19, INV-TUI-20, INV-TUI-23, and INV-TUI-24 coverage.

Manual terminal check, after automated gates: run mivia chat in normal, small,
and resized terminals; verify modal wheel behavior, no ghost right-click copy,
panel centering, and a legible base. Add only unautomatable checks to the owned
terminal-input guide.

## Invariants at landing time

At Step I1a, inspect the current manifest and allocate the lowest free IDs; do
not assume a numeric suffix in a shared worktree. Register these exact mappings,
then run `make validate-invariants`:

- geometry/render/scroll: `TestDialogRectFitsAndCenters`,
  `TestDialogViewsStayWithinTerminalBounds`, `TestDialogResizeRecentersAndReflows`,
  `TestDialogResizeClampsScroll`;
- composition: `TestDialogCompositorExactCanvas`,
  `TestDialogANSISeamsPreserveStyles`;
- pager/content: `TestBlockOverlayPreservesLongLines`,
  `TestSessionsDialogUsesAvailableRows`, `TestStatusDialogOverflowPolicy`;
- modal ownership: `TestModalMouseEventsAreFullySwallowed`,
  `TestModalOpenSwallowsMouseBeforeView`, `TestModalOpenSwallowsMouseAfterView`,
  `TestModalCloseInvalidatesHitMapBeforeView`,
  `TestModalCloseRebuildsHitMapAfterView`,
  `TestModalKeyPrecedenceBeforeAndAfterView`,
  `TestAsyncPasteDroppedWhileModalOpens`;
- producer semantics: `TestHelpReflowsAfterResize`,
  `TestStatusAndFleetSnapshotPolicy`;
- integration: `TestDialogProgramResizeIntegration`.

The corresponding invariant rows must state: one `dialogLayout.pageH` is shared
by geometry/render/scroll; modal events never reach transcript copy, selection,
composer focus, or viewport fallback; block/fleet content remains complete and
reachable under the near-full pager policy; and composition preserves exact
cell dimensions without ANSI state bleeding across seams.

Each row must name exact tests. Substring assertions alone are not coverage.

## Verification, scorecard, and rollback

Preflight and final gates, in order:

    go test ./internal/cli -run 'Test(Dialog|BlockOverlay|SessionsDialog|StatusDialog|Modal|Help|Fleet|Overlay|RightClick|Tui)' -count=1
    go test -race ./internal/cli
    go vet ./...
    go build ./...
    make invariants
    make validate-invariants
    make structure-check
    make docs-check
    git diff --check

Before any commit, also run `make pre-commit`, `make commit-check
MSG='docs(ai): tighten dialog plan gates'`, and `git diff --cached --check`.
Before delivery, run `make pre-push`. Never disable or bypass repository hooks;
record each command, exit status, and timeout in the completion report. These
checks are scoped to the plan-only commit while other
agents' unrelated changes remain unstaged.

Full repository gates remain required before merge when the unrelated baseline
blocker is resolved: make verify, make test, and make race.

Scorecard before implementation: compile/no cycles = pending preflight; no
breaking public API = pass (internal/cli only); isolated tests = pass after the
split; backward-compatible config = pass; every production function tested =
pending wave gates; invariant/doc ownership = pending implementation.

Rollback the plan, rather than weakening tests, if a modal can copy/focus/scroll
chat behind it; a pager cannot reach its final line after resize; any output row
exceeds the terminal canvas; ANSI state bleeds between base and panel; or the
shared frame forces a second independent sessions frame.

## Non-goals

- click-outside-to-close behavior;
- x-extent expansion of tuiHitMap zones;
- backdrop dimming or shadow until composition is proven;
- Lip Gloss/Bubble Tea major-version upgrade;
- unrelated cleanup or repair of other agents' shared-worktree changes or
  baseline test failures.

## Research anchors

- Lip Gloss v1.1.0 tree: https://github.com/charmbracelet/lipgloss/tree/v1.1.0
- Lip Gloss current compositing README: https://github.com/charmbracelet/lipgloss#compositing
- Charm x/ansi truncate implementation: https://github.com/charmbracelet/x/blob/main/ansi/truncate.go
