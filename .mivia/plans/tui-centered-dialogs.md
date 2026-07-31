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

The repository was dirty before this review. This focused baseline command:

    go test ./internal/cli -run 'Test(Overlay|SessionsDialog|Fleet|RightClick|LayoutAndView|TUISmoke)' -count=1

did not reach tests because unrelated worktree edits currently leave
internal/chat/persistence.go referring to Session.Model, while the type has
model. Do not overwrite those edits. Implementation starts only after preflight
establishes a compiling baseline or explicitly records the owner of that blocker.

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
        rect   rect
        innerW int
        pageH  int
    }

    func dialogRect(termW, termH int, p dialogPrefs, contentW, contentH int) rect
    func makeDialogLayout(termW, termH int, p dialogPrefs, contentW, contentH int) dialogLayout

Contract:

- Terminal dimensions are clamped to the same minimums used by the base TUI; no
  negative dimensions or divide-by-zero are possible.
- The outer rectangle is clamped to the terminal, then centered with stable
  floor-left/top rounding for odd spare dimensions.
- A non-zero preferred pixel size wins; otherwise a non-zero preferred
  percentage uses the terminal dimension; otherwise the content size is used.
  The result is then capped by max percentages and expanded to minimums.
  Content-sized dimensions include frame rows/cols. This makes the block/fleet
  pager's near-full policy representable without a second geometry path.
- Minimums are honored when the terminal can fit them. If the terminal is smaller
  than a minimum, the rectangle is reduced to the terminal rather than overflow.
- innerW = max(1, rect.w-frameCols) and pageH = max(1, rect.h-frameRows).
- All four paths use makeDialogLayout: renderChatView, modal key routing, modal
  wheel routing, and WindowSizeMsg clamping. No modal path reads termH-4 directly.
- If the terminal cannot fit the frame chrome, use a clipped, borderless tiny
  fallback that still returns exactly the terminal canvas. Minimums are preferred
  dimensions, not permission to overflow. Test 1x1, 10x4, 20x6, and 39x10.

The production composite path passes the already-created dialogLayout through.
Compatibility View(w, h) test helpers may create one through makeDialogLayout.

### 2. Cell-safe composition

Use ansi.Cut for grapheme width unless the existing TUI's intentional rune-width
policy requires ansi.CutWc; choose one policy and use it for all compositor
measurements. Encode that decision in tests for ANSI styles, emoji, CJK,
combining marks, and braille logo rows. Do not use len, byte slicing, or the local
visibleWidth for compositor coordinates.

    func normalizeCanvas(s string, termW, termH int) []string
    func overlayAt(base, panel string, x, y, termW, termH int) string

normalizeCanvas pads/truncates every base row to termW cells and returns exactly
termH rows. For block/fleet content, wrap long logical lines into display rows
with ansi.Cut or ansi.CutWc before paging; never discard cells with an ellipsis.
The same wrapped rows must be used by ViewAt and scroll clamping. Other reference
dialogs may truncate producer rows to their declared policy width. overlayAt
treats base and panel as ANSI-bearing cell strings,
replaces only the panel rectangle, resets at both seams, and returns exactly
termH newline-separated rows of width termW. It is safe when a panel is partly
outside a tiny terminal; geometry should normally prevent this, but the
compositor remains fail-closed.

Use one renderDialogFrame(title, rows, footer, layout) for blockOverlay and
sessionsDialog. The frame owns border, title, row fill, footer truncation, and
exact dimensions. Content owners retain only state-specific rows and input.
Frame chrome constants define whether rows include title, footer, and borders;
tests must assert the arithmetic instead of relying on the old overlayPageH
comment.

### 3. Modal input and resize

Add a modal branch before right-click copy, handleMouseMsg, and textarea/viewport
fallback:

- overlay: wheel changes yOffset using layout.pageH; left/right/middle and
  right-click are consumed; no transcript copy, selection, composer focus, or
  viewport scroll occurs;
- sessions: wheel moves the cursor using the same visible-row count used by
  rendering; all clicks are consumed;
- no modal: preserve current hit-map and viewport behavior unchanged.

Keep global ctrl+c/ctrl+q precedence unchanged. Modal keys remain modal; modal
mouse input is modal too. tuiHitMap need not gain x extents.

On tea.WindowSizeMsg, invalidate the hit map, update dimensions, clamp active
modal state against the newly computed layout, then render. Opening or closing a
modal also invalidates the hit map. Dialog content is a snapshot at open time for
status and fleet detail, matching current behavior; resize reflows/wraps that
snapshot, but does not silently introduce a live-refresh contract.

### 4. Sizing policy

These are outer dimensions and must be named prefs, not scattered magic values:

| Surface | Width | Height | Pager |
|---|---:|---:|---|
| /status | 60 preferred, min 32 | content, min 8 | no |
| /tools | 50 preferred, min 28 | content capped at 60% terminal, min 8 | yes if needed |
| /help | 76 preferred, min 40 | content capped at 70% terminal, min 8 | yes |
| /sessions | 70 preferred, min 40 | rows plus frame, capped by terminal | cursor window |
| block/fleet detail | 90% preferred, min 40 | 85% preferred, min 8 | yes |

Status and every other producer truncate each row to the frame inner width at
render time. Constructors must not assume the eventual terminal width.

## Exact change surface

Files to create:

- internal/cli/dialog_geometry.go - rect, prefs, layout, and pure helpers.
- internal/cli/dialog_compositor.go - canvas normalization, overlayAt, shared frame.
- internal/cli/dialog_geometry_test.go - table-driven geometry and resize cases.
- internal/cli/dialog_compositor_test.go - ANSI/cell and exact canvas cases.
- internal/cli/dialog_tui_test.go - producer prefs and snapshot-policy cases.

Files to modify:

- internal/cli/overlay.go - shared frame/layout; scroll accepts pageH, not termH;
  preserve full-content, redaction, and pager semantics.
- internal/cli/sessions_dialog.go - shared frame; visible rows derive from layout.
- internal/cli/dialog_tui.go - named prefs and producer row contract.
- internal/cli/fleetbox.go - shared near-full pager policy, no new type.
- internal/cli/tui_view.go - always render base, normalize, make one layout, compose.
- internal/cli/tui_message.go - modal mouse gate before copy and fallback; clamp
  modal state in its existing WindowSizeMsg owner, with no duplicate resize path.
- internal/cli/tui_view_test.go and internal/cli/tui_mouse_test.go - positional,
  modal transition, resize, and no-ghost-hit coverage.
- internal/cli/overlay_test.go - pager wrapping, page height, resize, and ANSI
  preservation coverage.
- internal/cli/sessions_dialog_test.go and internal/cli/fleetbox_test.go -
  dynamic row, snapshot, and shared pager policy coverage.
- .mivia/invariants.md - accepted invariants with exact test names.
- Makefile - add every new invariant test name to the make invariants selector.
- docs/architecture/overview.md - durable TUI base-plus-modal boundary.
- docs/development/terminal-input.md - owned manual modal mouse/resize checks.

No dependency upgrade is planned. If direct use of x/ansi requires changing its
go.mod classification, run go mod tidy and include only intentional module
metadata changes.

## ADLC implementation waves

The following is the locked breakdown after Step 0. Every production task has a
preceding RED task, each task owns one file, and each wave has a focused gate.

| Wave | Task | File | Type | Depends on | Gate |
|---|---|---|---|---|---|
| 0 | P0 | worktree | preflight | none | clean-build decision; preserve unrelated edits |
| 1 | G1 | dialog_geometry_test.go | RED | P0 | named geometry tests fail for absent API |
| 1 | G2 | dialog_geometry.go | GREEN | G1 | focused geometry tests pass |
| 1 | R1 | context | review | G1/G2 | geometry and tiny-terminal cases accepted |
| 2 | C1 | dialog_compositor_test.go | RED | G2 | cell/ANSI tests fail for absent compositor |
| 2 | C2 | dialog_compositor.go | GREEN | C1 | compositor tests and race pass |
| 2 | R2 | context | review | C1/C2 | no byte slicing; exact canvas dimensions |
| 3 | O1 | overlay_test.go | RED | G2/C2 | page-height/last-line/resize tests fail |
| 3 | O2 | overlay.go | GREEN | O1 | overlay pager tests pass |
| 3 | S1 | sessions_dialog_test.go | RED | G2/C2 | dynamic visible-row tests fail |
| 3 | S2 | sessions_dialog.go | GREEN | S1 | sessions tests pass |
| 3 | R3 | context | review | O2/S2 | shared frame and no duplicate geometry |
| 4 | V1 | existing view/mouse tests | RED | O2/S2 | positional/modal-input tests fail |
| 4 | V2 | tui_view.go | GREEN | V1 | composite and hit suppression pass |
| 4 | M1 | existing mouse/view tests | RED | V1 | modal wheel/click/resize tests fail |
| 4 | M2 | tui_message.go | GREEN | M1 | modal gate and resize clamp pass |
| 4 | R4 | context | review | V2/M2 | event ordering accepted |
| 5 | D0 | dialog_tui_test.go | RED | R4 | prefs/snapshot tests fail |
| 5 | D1 | dialog_tui.go | GREEN | D0 | all prefs and producer truncation tests pass |
| 5 | F0 | fleetbox_test.go | RED | R4 | fleet pager policy test fails |
| 5 | F1 | fleetbox.go | GREEN | F0 | fleet uses shared pager policy |
| 5 | I1 | .mivia/invariants.md and Makefile | GREEN | D1/F1 | manifest and selector agree |
| 5 | DOC1 | owned docs | GREEN | D1/F1 | docs checks pass |
| 6 | A1 | context | hostile audit | all prior | three independent audits find zero confirmed bugs |

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
  x/y, base preserved outside
  the rectangle, ANSI styles at both seams, reset/no bleed, emoji/CJK/combining/
  braille cells, short base rows, short panel rows, and tiny-terminal clipping;
- TestBlockOverlayPreservesLongLines: long code/output lines, CJK, emoji, and
  ANSI styling are wrapped or otherwise reachable without truncation; final
  display row is reachable at every policy size, page-down uses rendered pageH,
  resize clamps offset, and redaction remains intact;
- sessions: visible row count derives from layout, a 10-row terminal stays
  bounded, cursor/scroll clamp after resize and deletion, and all rows reachable;
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
- TestModalTransitionInvalidatesHitMap: open and close transitions cannot reuse
  chat hit zones;
- end-to-end TUI journey: open every modal, resize while open, scroll, close,
  then copy/select/type in chat. Preserve existing INV-TUI-2, INV-TUI-6,
  INV-TUI-16, INV-TUI-19, INV-TUI-20, INV-TUI-23, and INV-TUI-24 coverage.

Manual terminal check, after automated gates: run mivia chat in normal, small,
and resized terminals; verify modal wheel behavior, no ghost right-click copy,
panel centering, and a legible base. Add only unautomatable checks to the owned
terminal-input guide.

## Invariants at landing time

Allocate the lowest free IDs, then run make validate-invariants:

- geometry, render, and scroll use the same dialogLayout.pageH; no modal pager
  path reads terminal height directly;
- while a modal is open, no mouse event reaches transcript copy, selection,
  composer focus, or viewport fallback; modal wheel is handled by the modal;
- block and fleet detail retain complete content and the near-full pager policy;
- long block/fleet logical lines are wrapped into reachable display rows; no
  horizontal content is silently discarded when the panel is narrower;
- composition preserves exact cell dimensions and does not leak ANSI state across
  panel seams.

Each row must name exact tests. Substring assertions alone are not coverage.

## Verification, scorecard, and rollback

Preflight and final gates, in order:

    go test ./internal/cli -run 'Test(Dialog|Overlay|Sessions|Fleet|Tui|RightClick)' -count=1
    go test -race ./internal/cli
    go vet ./...
    go build ./...
    make invariants
    make validate-invariants
    python3 scripts/check_go_structure.py --strict --all
    make docs-check
    git diff --check

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
- unrelated cleanup or repair of the pre-existing Session.Model worktree blocker.

## Research anchors

- Lip Gloss v1.1.0 tree: https://github.com/charmbracelet/lipgloss/tree/v1.1.0
- Lip Gloss current compositing README: https://github.com/charmbracelet/lipgloss#compositing
- Charm x/ansi truncate implementation: https://github.com/charmbracelet/x/blob/main/ansi/truncate.go
