# Centered, content-sized TUI dialogs

**Status:** Design complete, implementation-ready from Stage 1. Not started.
**Date:** 2026-07-31
**SoT:** `.mivia/plans/tui-centered-dialogs.md`
**Scope:** `/help`, `/status`, `/tools`, `/sessions`, block-detail overlay, fleet detail.

**Product goal:** dialogs render as framed panels at a size appropriate to their
content, centered over a still-visible chat frame — instead of replacing the
whole screen.

---

## Document map

| § | Content |
|---|--------|
| 0 | Why they are full-screen today (ground truth) |
| 1 | Three problems that must be solved in the same change |
| 2 | Architecture — four layers |
| 3 | Per-dialog sizing policy |
| 4 | Stages, gates, tests |
| 5 | Invariants |
| 6 | Risks, non-goals, backlog |

---

## 0. Why they are full-screen today

It is not a sizing choice. It is a **replacement**: `renderChatView` early-returns
before the chat frame is ever built.

```go
// tui_view.go:22-28
func (m *tuiModel) renderChatView() string {
    if m.sessionsDlg != nil { return m.sessionsDlg.View(max(40, m.width), max(6, m.height)) }
    if m.overlay != nil     { return m.overlay.View(max(20, m.width), max(6, m.height)) }
```

There is no base layer for a dialog to sit on. Each is handed the whole terminal
and fills it.

### Ground truth

| Fact | Evidence |
|------|----------|
| Chat frame never rendered while a dialog is open | `tui_view.go:22-28` |
| `blockOverlay.View` is full-bleed: `inner = w-4`, blank-pads to `overlayPageH(h) = h-4` | `overlay.go:74-114` |
| `sessionsDialog.View` **ignores `h` entirely**; rows capped at `sessionsDialogRows = 12` | `sessions_dialog.go:29`, `:86` |
| `/help`, `/status`, `/tools` are all `*blockOverlay` | `dialog_tui.go:19-92` |
| `sessionsDialog` re-implements border/title/fill/footer independently | `sessions_dialog.go:86-163` vs `overlay.go:74-128` |
| lipgloss is **v1.1.0** — no `Canvas`/`Layer` (v2 only) | `go.mod:8` |
| Early-return path has **no height clamp**, unlike `renderChatView`/`renderWelcomeBody` | `tui_view.go:84-87`, `:307-310` |

The two dialog families already disagree about what "size" means: the overlay is
full-width *and* full-height; the sessions dialog is full-width, top-anchored,
~15 rows. Unifying them is part of the work, not a side effect.

---

## 1. Three problems that must be solved in the same change

### 1.1 lipgloss v1.1.0 cannot composite

`Canvas`/`Layer` are v2 API. `lipgloss.Place` centers content inside a **blank**
box; it does not draw over existing content. The four `PlaceHorizontal` call
sites in this repo (`logo.go:42`, `logo.go:214`, `logostate.go:301`,
`pixel.go:195`) all center within their own blank region — none composite.

We need our own overlay primitive. Most of the machinery already exists:

| Have | Where |
|------|-------|
| `trackStyles(active []string, chunk string) []string` — walks a string carrying active SGR state | `markdown_table_render.go:226` |
| `truncateANSI` / `truncateANSIHard` — right-cut | `markdown_table_render.go:352`, `:252` |
| `padVisible` | `markdown_table_render.go:334` |
| `visibleWidth` | `tui_helpers.go:33` |
| `truncateToWidth` | `dialog.go:190` |

**Missing: exactly one primitive** — drop the first N *visible* cells off a line
while preserving escape state (a left-cut). Everything else composes from the
above.

### 1.2 Scroll and render will silently disagree

`overlayPageH(termH)` derives the content page from **terminal** height, and
`scroll()` takes `termH` as its argument:

```go
// overlay.go:48-71
func overlayPageH(termH int) int { ... return termH - 4 }
func (o *blockOverlay) scroll(delta, termH int) { pageH := overlayPageH(termH); ... }
```

The comment on `scroll` already states the requirement — *"the content page is
derived from it so scroll and View always agree on the window size"*. Shrink the
dialog below the terminal and that guarantee breaks: the clamp
`max := len(o.lines) - pageH` is computed against a page larger than what is
drawn, so the last lines become unreachable and paging overshoots.

This is the same failure class as the `composerPadRows` mismatch that clipped the
composer border on send (`tui_view.go:96-101`): two paths computing a height
independently. It gets the same fix — **one geometry value, computed once,
consumed by both** — rather than a second comment asking future code to be
careful.

### 1.3 The hit map goes stale, and centering is what exposes it

`m.hitMap.rebuild(...)` runs only on the chat path (`tui_view.go:68`). With the
early return, the map retains the **last chat layout** while a dialog is open,
and mouse handling never checks for one (`tui_message.go:98-111`, `:181-253`).
Right-click still resolves to whichever transcript block used to occupy that row
and copies it (`tui_message.go:99-108`).

Today the dialog covers the whole screen, so the bug is invisible. Center the
dialog, leave the chat visible behind it, and clicks around the dialog begin
acting on ghosts. **This change makes an existing latent bug user-visible**, so
it must be fixed in the same commit.

Constraint worth noting: `tuiHitMap.hit(y)` is **one-dimensional** — zones are
row ranges with no x extent (`tui_hitmap.go:20-56`). "Behind the dialog
horizontally" is therefore not expressible in the current model. Simplest correct
fix is a modal flag that suppresses hits wholesale while a dialog is open;
per-column routing would require adding x to the zone model and is not worth it
for this change.

---

## 2. Architecture — four layers

### Layer 1 · Geometry (pure, no rendering)

```go
type dialogPrefs struct{ prefW, prefH, minW, minH, maxWPct, maxHPct int }
type rect struct{ x, y, w, h int }

func dialogRect(termW, termH int, p dialogPrefs) rect
```

Clamps to terminal minus margin, enforces minimums, centers. Content page height
derives from `rect.h`. `blockOverlay.scroll` changes signature to take the rect
(or the derived page height) instead of `termH`. **This kills §1.2 by
construction rather than by discipline.**

Pure function, fully unit-testable with no terminal.

### Layer 2 · Compositor

```go
func cutANSILeft(line string, n int) string           // the one new primitive
func overlayAt(base, panel string, x, y int) string
```

`overlayAt` splices per line — left segment of base, panel row, right segment of
base — re-emitting active SGR state at each seam so neither side bleeds colour
into the other. Backdrop treatment (dim / leave as-is) is a one-line policy
inside it.

### Layer 3 · One dialog frame

`dialog_tui.go`'s header comment already claims help/status/tools *"share one
surface, one key model and one look"* — true, because all three return
`*blockOverlay`. But `sessionsDialog` duplicates the border, title bar, blank
fill and footer. Extract the frame once; each dialog supplies title, lines,
footer and `dialogPrefs`. That duplication is why the two families drifted, and
leaving it in place means they drift again.

### Layer 4 · Wire-up

`renderChatView` always renders the chat frame, then composites the dialog over
it. `hitMap.rebuild` learns about modal state.

---

## 3. Per-dialog sizing policy

Current model is "fill the screen". Correct model is **size to content, capped by
terminal**.

| Dialog | Width | Height | Rationale |
|--------|-------|--------|-----------|
| `/status` | ~60 | content | Fixed short readout (`dialog_tui.go:56-81`); must never scroll |
| `/tools` | ~50 | min(content, 60%) | Narrow list of names |
| `/help` | ~76 | min(content, 70%) | Key column padded to 26 (`dialog_tui.go:37`) |
| `/sessions` | ~70 | content, clamped | Already content-sized; needs centering + a real `h` clamp |
| Block detail | 90% × 85% | near-full | The genuine pager |

**The last row is the trap.** The block overlay is the doorway from every inline
truncation to full content (`overlay.go:1-6`). Shrinking it is a regression, not
polish. "Custom size" must mean *per-dialog appropriate*, not *uniformly small*.

---

## 4. Stages, gates, tests

Stages 1–3 are invisible to the user and independently verifiable. Stage 4 is the
only risky one.

| # | Stage | Gate (RED first) |
|---|-------|------------------|
| 1 | `dialogRect` + prefs | Centering; clamp to terminal; minimums honoured; odd-width rounding stable; degenerate terminals (w<minW, h<minH) |
| 2 | `cutANSILeft` + `overlayAt` | Styles survive both seams; total visible width preserved; no colour bleed base↔panel; wide runes and the braille logo not split mid-cell |
| 3 | `blockOverlay` onto rect geometry | **"The last content line is reachable at every dialog size"** — the regression this stage exists to prevent. Page-down never overshoots |
| 4 | Composite in `renderChatView`; modal `hitMap` | Chat header visible behind dialog; dialog border columns at expected x; right-click outside dialog copies nothing |
| 5 | Per-dialog prefs; `sessionsDialog` honours `h` | `/status` does not scroll; sessions dialog fits a 10-row terminal |
| 6 | Optional polish | Dimmed backdrop, drop shadow |

### Existing tests will not catch a half-done stage 4

`sessions_dialog_test.go` and `overlay_test.go` assert via
`stripANSI(m.View())` + `strings.Contains`. Those keep passing under compositing
— good for safety, but **they do not pin centering at all**. Stage 4 could
half-work and stay green. New positional assertions are required, not optional.

---

## 5. Invariants

Add to `.mivia/invariants.md`:

- **Dialog geometry is the single source of truth.** Scroll clamping and frame
  rendering derive their page height from the same `rect`; neither may read
  terminal height directly. (Generalises the `composerPadRows` lesson.)
- **A modal dialog suppresses transcript hit-testing.** While `m.overlay` or
  `m.sessionsDlg` is non-nil, no mouse event resolves to a transcript block.
- **The block-detail overlay stays near-full-screen.** It is the only pager for
  truncated content; per-dialog sizing must not shrink it.

---

## 6. Risks, non-goals, backlog

**Risks**

- Compositing at seams is where ANSI bugs live. Stage 2's tests are the whole
  defence; do not merge stage 4 with stage 2 amber.
- The braille logo in the header is wide-rune content directly in the base layer
  (`logostate.go`). Cutting it mid-cell corrupts the frame — explicitly covered
  in the stage 2 gate.
- `handleOverlayKey` and `handleSessionsDialogKey` consume **every** key by
  design (`overlay.go:130-152`, `sessions_dialog.go:178-220`). That stays correct
  when the chat is visible behind — modal means modal — but it now *looks* like
  the chat should be interactive. Worth a deliberate decision, not a drift.

**Non-goals for this plan**

- **Click-outside-to-close** — a mouse/keymap concern; belongs with the pending
  input-layer audit, not here.
- Backdrop dim colour — a visual decision that should be seen before it is pinned
  in a test.
- Adding x extent to `tuiHitMap` zones (§1.3) — not justified by this change.

**Backlog**

- Migrating to lipgloss v2 would make Layer 2 obsolete. Not worth doing for this
  change alone, but if v2 lands for other reasons, `overlayAt` should be retired
  rather than maintained alongside `Canvas`.
