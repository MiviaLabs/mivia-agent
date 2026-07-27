# Chat History UI — Remaining Implementation Phases

Read first: `AGENTS.md`, `.ai/INDEX.md`, `internal/cli/msgcard.go`, `internal/cli/renderer.go`, `internal/cli/chatblock.go`, `internal/cli/chatblock_render.go`.

## Phase 2: Turn separators (high impact, low complexity)

**Goal:** Add thin visual dividers between conversation turns so the user can
visually separate one Q&A from the next.

### Problem

Turns are concatenated with zero visual gap. In a long conversation it is hard
to see where one turn ends and another begins.

### Implementation

**`internal/cli/renderer.go`** — in `RenderHistoryMessages`, after each turn
produced by `RenderTurn`, append a dim divider line:

```go
const turnSeparator = "  ─── · ───"
// after appending turn lines:
result = append(result, tuiDimStyle.Render(turnSeparator))
```

For the TUI viewport (`buildViewportContent` / `appendBlock`), inject a
`ChatBlockDivider` block between turns when hydrating from
`HydrateChatBlocks`.

**Style**: `lipgloss.NewStyle().Faint(true)` — thin, unobtrusive, 2–3 spaces
of breathing room above and below.

### Tests

- `TestRenderTurn_Separator`: verify a 2-turn `RenderHistoryMessages` call
  produces the separator line between turns.
- `TestView_TurnSeparatorInViewport`: verify separator appears in viewport
  content when messages span multiple turns.

---

## Phase 3: Expandable tool results in history (medium impact)

**Goal:** Click/select a tool result line to expand/collapse full output inline
within the viewport, without switching to a separate pane.

### Problem

Tool results are always shown as compact one-liners (~80 chars). Users cannot
read the full output without scrolling to the raw tool message in the session
or re-running the tool.

### Implementation

**`internal/cli/chatblock.go`** — `ChatBlock` already has a `Collapsed` field.
Use it for tool blocks.

**`internal/cli/chatblock_render.go`** — in `RenderChatBlocks`, when a
`ChatBlockTool` block has `Collapsed == false` and its `Text` exceeds
`maxToolResultPreview`, render the full content with a dim/faint background
and syntax highlighting if the content looks like code.

**`internal/cli/tui_focus.go`** — when `focusScrollback` is active and the
cursor is over a tool block line, pressing `enter` or `space` toggles
`Collapsed` and calls `renderVP()`.

**`internal/cli/tui_hitmap.go`** — tool result lines need hit targets for
mouse click detection, reusing the existing `chatBlockRanges` map.

### Design

```
  ✓ read_file ... package main func main…   ← collapsed (default)
```

When expanded:

```
  ✓ read_file [click to collapse]
    package main

    func main() {
        fmt.Println("Hello")
    }
```

Use `tuiDimStyle` background or a shaded block to visually distinguish
expanded output from surrounding chat bubbles.

### Tests

- `TestRenderMessageForHistory_ToolResultExpanded`: verify content > preview
  limit renders full output when not collapsed.
- `TestView_ToolBlockToggleCollapse`: verify toggling collapsed state
  re-renders viewport with correct content.

---

## Phase 4: Inline thinking/reasoning (medium impact)

**Goal:** Show model thinking/reasoning inline within the assistant block
(rather than as a separate toggle panel), appearing during streaming and
remaining collapsible in history.

### Problem

The thinking buffer (`thinkingBuf`, `showThinking` toggle) renders as a
separate panel at a fixed location. It is visually disconnected from the
assistant's message flow where the thought applies.

### Implementation

**`internal/cli/tui_stream.go`** — during streaming, `PushThinking` writes to
the bridge. Instead of a separate buffer, emit `ChatBlockThinking` blocks
interleaved with `ChatBlockAssistant` content in `renderStreamVP()`.

**`internal/cli/chatblock_render.go`** — `ChatBlockThinking` blocks render
with a magenta italic header:

```
  ▾ thinking                    ← when expanded
    Let me check the file...
    The user wants to...
  ▸ thinking                    ← when collapsed
```

**`internal/cli/tui_view.go`** — remove the separate thinking panel toggle
(`showThinking`). Keep the field as a per-block collapse state.

**`internal/cli/tui_focus.go`** — same `enter`/`space` toggle as Phase 3
works for thinking blocks too.

### Transition

- **Phase 4a**: Inline thinking blocks during streaming (live view).
- **Phase 4b**: Persist thinking blocks in history (re-hydrate from saved
  messages — may need a new message role or metadata field).

### Tests

- `TestRenderChatBlocks_ThinkingInline`: verify thinking block renders
  correctly in the block sequence.
- `TestView_ThinkingCollapseToggle`: verify collapse/expand via keyboard.

---

## Phase 5: Streaming transition indicator (low impact)

**Goal:** Show a visual cue when the model is generating, at the point in the
viewport where the new content will appear.

### Problem

When streaming starts, text just appears. There is no "generating…" indicator
at the insertion point. Users must look at the status bar to confirm the
model is working.

### Implementation

**`internal/cli/tui_view.go`** — in `renderStreamVP()`, when `m.waiting &&
m.streamBuf.Len() > 0`, append a pulsing cursor or spinner at the bottom of
the viewport content.

```go
if m.waiting && m.streamBuf.Len() > 0 {
    glyph := m.spinner.View() // Bubble Tea spinner
    content += "\n" + tuiDimStyle.Render(fmt.Sprintf("  %s generating…", glyph))
}
```

The Bubble Tea spinner is already available in `tuiModel.spinner` (spinner.Dot).

**`internal/cli/tui_stream.go`** — the `tuiTickMsg` handler already calls
`renderStreamVP()` on each tick. The spinner frame advances naturally.

**Clear on finish**: when the stream finishes and the block is finalized, the
last `renderVP()` call (from `finishStream`) builds content from
`buildViewportContent()` which does not include the indicator.

### Tests

- `TestView_StreamingIndicatorShown`: verify indicator appears when waiting
  with stream buffer content.
- `TestView_StreamingIndicatorCleared`: verify indicator disappears after
  `finishStream`.

---

## Ordering recommendation

| Phase | Est. effort | Risk | Value |
|-------|-------------|------|-------|
| 2 — Turn separators | ~1h | Low | High |
| 5 — Streaming indicator | ~1h | Low | Medium |
| 3 — Expandable tool results | ~3h | Medium | High |
| 4 — Inline thinking | ~4h | Medium | Medium |

Start with Phase 2 (simple, high visual ROI), then Phase 5 (easy, improves
flow), then the more involved Phases 3 and 4.

---

## Related files

| File | Role in these phases |
|------|----------------------|
| `internal/cli/msgcard.go` | Card chrome functions (Phase 1 complete) |
| `internal/cli/renderer.go` | Turn/block rendering (Phase 2–5) |
| `internal/cli/chatblock.go` | Block model, `Collapsed` field (Phase 3–4) |
| `internal/cli/chatblock_render.go` | Block-to-lines conversion (Phase 3–4) |
| `internal/cli/tui_view.go` | Viewport assembly (Phase 2, 5) |
| `internal/cli/tui_stream.go` | Streaming bridge (Phase 4–5) |
| `internal/cli/tui_focus.go` | Keyboard routing, toggle (Phase 3–4) |
| `internal/cli/tui_hitmap.go` | Mouse hit detection (Phase 3) |
| `internal/cli/tui.go` | TUI model, state (Phase 2–5) |
