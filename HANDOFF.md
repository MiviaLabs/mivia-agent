# Mivia Agent — Handoff / Implementation Plan

**Branch:** `master` (as of handoff draft)
**Last shipped UX slice:** `6763367` — message cards + desktop-style composer; `tui_view.go` extract; welcome auto-session labels.

This document is the **source of planned work left after the chat UX wave**. Prefer implementing from here rather than re-deriving from chat history.

---

## Already done (do not re-build)

| Area | Status |
|------|--------|
| Welcome screen (no auto-load last) | Done — user picks session or types new |
| Rolling auto-save (≤5), unique names | Done — `SaveLast`, `IsAutoSaveName`, `LatestAutoSaveName` |
| Welcome labels: Last session / Auto · age | Done — `displaySessionName(si, latestAuto)` |
| User message cards (`╭─ you ─`) | Done — `msgcard.go`, live + history |
| Composer card (message-like input) | Done — `composer.go`, chat + welcome |
| Tool strip max 6, tab/↑↓/wheel | Done — `toolpanel.go` |
| Sticky status bar (1-line brand glyph) | Done — `brand.go` / `renderStatusBar` |
| History formatters (tools inline) | Done — `RenderHistoryMessages` / turn-aware |
| Go LOC/function gates | Done — `.ai/policy/go-structure.json` |
| Headless UI tests | Done — `tui_*_test.go`, structure tests |

---

## Still later — implementation plan

Work is ordered to **reduce thrash** and stay under structure limits (`tui.go` baseline ~1671; prefer **new files**, never raise baseline).

### Phase 0 — Data model (foundation)

**Problem:** Transcript is a flat `[]string` of pre-rendered ANSI lines. That blocks:

- Click-to-expand on a specific tool/thinking block
- Keyboard navigation of “turns”
- Collapsing thinking without re-rendering the whole buffer
- Consistent live vs history tool presentation

**Target model** (`internal/cli/chatblock.go` or similar):

```go
type blockKind uint8
const (
    blockUser blockKind = iota
    blockAssistant
    blockTool
    blockThinking
    blockSystem
    blockTurnDivider
    blockSkill // future
)

type chatBlock struct {
    ID        string
    Kind      blockKind
    Collapsed bool
    // raw payload for re-render at width changes
    UserText   string
    Assistant  string // markdown source
    ToolName   string
    ToolDetail string
    ToolResult string
    Thinking   string
    Meta       map[string]string // path chips, duration, skill name
}
```

**Migration:**

1. Keep `messages []string` as **render cache** only.
2. Own source of truth: `blocks []chatBlock`.
3. `rebuildTranscript(width)` → `messages` + `viewport.SetContent`.
4. `appendMsg` becomes `appendBlock` + rebuild tail (or incremental paint).

**Acceptance tests:**

- Rebuild at width 40 and 120 produces line counts that fit cards.
- Collapse/expand toggles only one block’s lines without losing others.
- `loadMoreMessages` prepends blocks and preserves YOffset (existing scroll tests adapted).

**LOC:** new package file(s); do not dump into `tui.go`.

---

### Phase 1 — Focus modes (composer | scrollback | tools)

**Goal:** Desktop-app focus, Grok-style simple mode.

| Mode | Keys | Mouse |
|------|------|--------|
| **composer** (default) | typing; Enter send/queue; Alt+Enter newline | click composer focuses |
| **scrollback** | j/k or ↑↓ select block; Enter/space expand; PgUp/PgDn page; Home/End | click message zone selects block; wheel scrolls viewport |
| **tools** | existing tab/↑↓/space (only when tool strip visible) | wheel/click on tool strip |

**State:**

```go
type focusPane uint8 // focusComposer, focusScrollback, focusTools
```

**Key routing (priority):**

1. Tools focused + strip visible → tool panel keys
2. Scrollback focused → block nav (do **not** send ↑↓ to textarea)
3. Composer focused → textarea (current behavior)
4. Tab cycles: composer → scrollback → tools (if any) → composer
5. Esc: tools → scrollback → composer (clear selection)

**Wire:** extract key routing into `tui_focus.go` / `tui_keys.go` (keep under function hard limit 120).

**Acceptance:**

- With empty composer, ↑ scrolls transcript **or** moves block selection when scrollback-focused (define: empty composer + scrollback focus).
- Typing a letter while scrollback focused **auto-focuses composer** and inserts (Grok simple-mode pattern) — optional but high desktop feel.
- Tool wheel never moves viewport YOffset (existing test).

---

### Phase 2 — Thinking as collapsible transcript blocks

**Today:** optional bottom panel (`showThinking` + `thinkingBuf`) — not in history, not clickable, competes with tool strip height.

**Target:**

1. On turn stream: append/update a `blockThinking` (collapsed by default after turn ends).
2. Header line: `▸ thinking · 1.2s` / `▾ thinking` (expand shows last N lines).
3. Ctrl+T toggles **global** “show thinking expanded by default” **and** expands selected thinking block.
4. Remove free-floating thinking panel from `View()` (or keep only during stream as live strip if height allows).
5. Persist thinking in session only if product already stores it; otherwise session-ephemeral is OK (document).

**Acceptance:**

- Finished turn history includes a thinking block when model emitted reasoning.
- Space/Enter on selected thinking toggles collapse.
- Height budget: expanded thinking capped (e.g. 12 lines) like tools.

---

### Phase 3 — Unified tool presentation (live + history)

**Today:**

- Live: windowed strip (`toolpanel.go`)
- History: compact lines from `RenderMessageForHistory` / `finishStream` summary

**Target visual language (one family):**

```
  ⠋ read_file  [path/chip]  120ms     ← running
  ✓ search_replace  [a.go]  +2 −1  45ms  ▸
    ╭─ diff / output (expanded)
```

1. Shared painter: `formatToolBlock(row, expanded, width)` used by:
   - live strip
   - history `blockTool`
   - finishStream summary (collapsed one-liners)
2. History tools expandable via Phase 1 selection (not only live strip).
3. Path chips + edit +/− already partially in write tools — keep in Meta.
4. Mouse Y hit map for tool lines in **viewport** (harder) — after blocks model: store per-block screen line ranges on last paint.

**Acceptance:**

- Same icons/colors for live and reloaded history.
- Expand selected historical tool shows result preview (capped).
- Live strip still max 6 rows.

---

### Phase 4 — Skills / slash / system presentation

**Today:** slash handled out-of-band; little/no “skill call” chrome.

**Target:**

| Kind | Presentation |
|------|----------------|
| User slash that ran locally | System chip: `⚙ /save project` dim, not a user bubble |
| Future skill invocation | `◇ skill:name` block with short status, expandable args/result |
| System / info | dim italic lines (current `appendInfo` style) |

1. Introduce `blockSystem` / `blockSkill` in model.
2. When `handleSlash` succeeds, append system block instead of (or in addition to) raw info line.
3. If skills become first-class tools later, map tool name prefix or event kind → `blockSkill`.

**Acceptance:** slash outcomes appear in transcript and survive hydrate if persisted (or re-derived).

---

### Phase 5 — Composer / message area polish (remaining)

Already have bordered composer + user cards. Remaining:

1. **Composer height:** grow with content up to max; internal textarea scroll (bubbles already scrolls if height fixed).
2. **Queued draft:** while waiting, show pending queue lines as ghost chips above composer (not only hint bar).
3. **Focus ring:** already cyan when focused — ensure blur when scrollback/tools focused (set `renderComposer(..., focused: pane==composer)`).
4. **Welcome:** same composer + optional “Continue last” key (`Ctrl+L` or `l` empty) → open latest auto without arrowing.

---

### Phase 6 — Mouse interactivity matrix

| Zone | Click | Double-click | Wheel |
|------|-------|--------------|-------|
| Status | no-op | — | no-op |
| Transcript | select block under Y | expand block | scroll viewport / loadMore near top |
| Tool strip | select tool | expand | scroll tool window only |
| Composer | focus composer | — | no-op (or multiline) |
| Hint | no-op | — | no-op |

**Implementation notes:**

- On each `View()`, after building transcript string, compute `blockRanges []struct{ id; y0; y1 }` from rendered line counts.
- Store on model for next `Update` mouse path.
- Do not put status/tool/composer Y into viewport content.

---

## Interaction matrix (target end state)

| Key | composer focus | scrollback focus | tools focus |
|-----|----------------|------------------|-------------|
| printable | type | focus composer + type | type if any; else no-op |
| Enter | send/queue | expand selected | expand tool |
| Space | (tool if selected) / type | expand | expand |
| ↑↓ | viewport if empty else caret | prev/next block | prev/next tool |
| Tab | → scrollback | → tools or composer | → composer |
| Esc | clear draft? / no-op | clear selection → composer | clear → scrollback |
| Ctrl+T | toggle thinking policy | toggle selected thinking | same |
| Ctrl+C | cancel / quit | cancel / quit | cancel / quit |
| PgUp/Home | history | history | history |

---

## File / package plan (LOC-safe)

| New / grow | Responsibility |
|------------|----------------|
| `chatblock.go` | block types, collapse, rebuild |
| `chatblock_render.go` | width-aware paint → lines |
| `tui_focus.go` | focus pane + key routing |
| `tui_mouse.go` | Y hit tests for blocks + tools |
| `thinking.go` | live thinking buffer → blocks |
| `composer.go` | already exists — polish only |
| `msgcard.go` | already exists — reuse for user blocks |
| `toolpanel.go` / `toolui.go` | share formatToolBlock |

**Do not:** add 200+ lines to `tui.go`. Lower baseline after every extract.

---

## Test plan (mandatory with each phase)

| Phase | Tests |
|-------|--------|
| 0 | rebuild width; collapse; loadMore offset with blocks |
| 1 | focus machine table; keys don’t double-handle; empty-composer scroll |
| 2 | thinking block appears; expand cap; Ctrl+T |
| 3 | live vs history tool paint parity; expand history tool |
| 4 | slash → system block |
| 5 | composer focused flag; welcome continue-last |
| 6 | mouse Y selects correct block (synthetic Y after View) |

Reuse pattern from `tui_view_test.go`, `tui_tools_test.go`, `scroll_fix_test.go`.

---

## Explicit non-goals (this handoff)

- Full git-diff side-by-side in-panel (keep unified preview).
- Subagent UI chrome (product may not expose yet).
- Rewriting plain `--plain` REPL to cards (optional later).
- Raising go-structure baselines to avoid splits.

---

## Suggested implementation order

1. **Phase 0** blocks model + rebuild (unblocks everything).
2. **Phase 1** focus modes (desktop feel).
3. **Phase 5.3–5.4** composer focus flag + continue last (cheap wins).
4. **Phase 2** thinking blocks.
5. **Phase 3** unified tools.
6. **Phase 6** mouse hit map (depends on 0+1).
7. **Phase 4** skills/slash chips.

---

## Verification commands

```bash
go test ./internal/cli/ ./internal/chat/ -count=1
python3 scripts/check_go_structure.py --all
make structure-check
```

Never claim interactive TUI verified without a real `mivia chat` smoke for focus + click.

---

## Open product decisions (resolve before Phase 2/4)

1. Persist model thinking in session JSONL? (privacy + size)
2. Auto-focus scrollback after send, or stay in composer?
3. Continue-last key binding (and whether welcome-only).
4. Whether historical tool expand shows full result or capped preview only.
