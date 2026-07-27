# Phase 4 — Inline Thinking/Reasoning: Implementation Plan

Read first: `AGENTS.md`, `.ai/INDEX.md`, `internal/cli/tui.go`, `internal/cli/tui_layout.go`,
`internal/cli/tui_stream.go`, `internal/cli/tui_message.go`, `internal/cli/chatblock.go`,
`internal/cli/chatblock_render.go`, `internal/cli/thinking.go`, `internal/cli/tui_view.go`,
`internal/cli/brand.go`, `internal/cli/tui_hitmap.go`, `docs/plans/chat-history-ui-phases.md`.

---

## Goal

Show model thinking/reasoning **inline** within the assistant block (not as a separate toggle panel). During streaming, thinking appears interleaved with the assistant's text. In history, thinking blocks remain collapsible. Only the **5–6 most recent lines** are shown when expanded. Mouse wheel over a thinking block scrolls inside it without scrolling the main viewport.

---

## Constraints (non-negotiable)

1. **Bubble Tea v1 — no nested viewports.** The `bubbles/viewport.Model` does not support nested scrollable areas. Internal thinking scrolling must be implemented as a windowed render with offset tracking, not a second viewport.
2. **No v2 migration.** Keep Bubble Tea v1.3.10, Bubbles v1.0.0, Lip Gloss v1.1.0.
3. **`--plain` mode unchanged.** All changes are in the TUI (`internal/cli/`), not `renderer.go`/plain mode.
4. **State stays in the Bubble Tea update loop.** Streaming producers (`SendUserWithEvent`, bridge) must not mutate `tuiModel` directly.
5. **Clipboard/persistence must strip thinking** unless explicitly opted in (already happens for `ChatBlockThinking` blocks from `HydrateChatBlocks` — verify).

---

## Challenges & Questions (answered)

### Q1: Why not keep the separate toggle panel and just reduce lines?
The separate panel (`showThinking` toggle) is visually disconnected from the assistant message flow. The panel sits between the viewport and tools, not where the reader looks. Inline placement puts thinking in the natural reading order: user question → thinking → assistant answer → tool calls → final response.

### Q2: How to scroll inside a thinking block without nested viewports?
Bubble Tea viewport doesn't support nested scroll. **Solution**: Track a `thinkingScrollOffset` per thinking occurrence. When rendering, only show a 5–6 line window of the total thinking lines, determined by the offset. Mouse wheel over the thinking block adjusts the offset and re-renders the viewport content. This is a "clipped window" approach, not a real scroll pane. The offset is bounded to `[0, max(0, totalLines - visibleLines)]`.

### Q3: What about the live streaming path? The thinking buffer grows in real-time.
During streaming, thinking accumulates in `m.thinkingBuf`. The live view always shows the most recent N lines (offset automatically tracks the bottom). If the user scrolls up in the thinking window, they can freeze the offset to see older lines while streaming continues.

### Q4: Where does the thinking render in the live stream — after assistant text or interleaved?
Currently `renderStreamVP()` appends thinking AFTER all stream content. The requirement says "inline within the assistant block." During streaming, there is only one active assistant block (the one being generated). **Solution**: thinking renders as a distinct section immediately after the active stream content, before any tool summary block. In history (after `finishStream`), thinking is its own `ChatBlockThinking` block and interleaves naturally.

### Q5: Can the hitmap identify thinking blocks for mouse scrolling?
Yes. The hitmap already stores per-block `blockID` ranges via `chatBlockRanges`. The mouse handler can look up the block by ID in `m.blocks` and check its `Kind`. If it's `ChatBlockThinking`, mouse wheel adjusts the thinking scroll offset.

### Q6: What happens to `showThinking` toggle and `ctrl+t`?
Removed. Thinking is always shown inline (or collapsed, controlled by the block's `Collapsed` field, toggled by `enter`/`space` in `focusScrollback` mode — same as Phase 3 tool blocks).

### Q7: How do we handle multiple thinking blocks in history?
Each `ChatBlockThinking` block in history gets its own scroll offset. Add a `maxLines` field (constant, 6) and a per-block `scrollOffset int` that can be adjusted via mouse wheel. Non-live thinking blocks store their offset in the `ChatBlock` struct.

### Q8: What about `thinkingBuf` vs thinking in stream. Which drains into which?
Currently:
- `bridge.Drain()` returns `thinking` text separately from `stream` text
- `tuiTickMsg` handler writes thinking into `m.thinkingBuf` and stream into `m.streamBuf`
- `renderStreamVP()` renders both independently

**New**: keep separate buffers. In `renderStreamVP()`, render the thinking inline with stream content using the windowed (5-6 line) approach. In `finishStream()`, thinking already becomes a `ChatBlockThinking` block.

---

## Implementation Steps (in order)

### Step 1: Add scroll offset infrastructure

**`internal/cli/chatblock.go`** — add to `ChatBlock`:
```go
ScrollOffset int  // internal scroll offset for thinking blocks (0 = bottom, show latest)
```

**`internal/cli/tui.go`** — add to `tuiModel`:
```go
// Remove: showThinking bool, thinkingLines int
// Keep:   thinkingBuf strings.Builder
// Add:
liveThinkingScroll int // scroll offset for the live streaming thinking block
```

### Step 2: Modify `renderThinkingBlock` for windowed rendering

**`internal/cli/thinking.go`** — rewrite `renderThinkingBlock`:

```go
const maxThinkingLines = 6

// renderThinkingBlock renders a ChatBlockThinking with a windowed view.
// It shows at most maxThinkingLines lines from the full text, determined by scrollOffset.
// scrollOffset=0 means show the LAST maxThinkingLines lines (most recent).
// scrollOffset=N means skip N lines from the top before showing the window.
func renderThinkingBlock(id, text string, collapsed bool, scrollOffset int, model string, width int) []string {
    if collapsed || strings.TrimSpace(text) == "" {
        return []string{tuiThinkingStyle.Render(headerCollapsed)}
    }
    lines := strings.Split(SafeChatBlockText(text, 0), "\n")
    totalLines := len(lines)
    if totalLines <= maxThinkingLines {
        // Show all lines, no scroll needed.
        return renderAllThinkingLines(lines, totalLines)
    }
    // Windowed view: scrollOffset is clamped to [0, totalLines - maxThinkingLines].
    // scrollOffset=0 means at bottom (show last maxThinkingLines lines).
    maxOffset := totalLines - maxThinkingLines
    if scrollOffset < 0 {
        scrollOffset = 0
    }
    if scrollOffset > maxOffset {
        scrollOffset = maxOffset
    }
    window := lines[scrollOffset : scrollOffset+maxThinkingLines]
    var out []string
    out = append(out, tuiThinkingStyle.Render(headerExpanded))
    if scrollOffset > 0 {
        out = append(out, tuiThinkingStyle.Render("    ↑ ..."))
    }
    for _, line := range window {
        out = append(out, tuiThinkingStyle.Render("    "+line))
    }
    if scrollOffset < maxOffset {
        out = append(out, tuiThinkingStyle.Render("    ↓ ..."))
    }
    return out
}
```

### Step 3: Remove separate thinking panel from layout

**`internal/cli/tui_layout.go`** in `layout()`:
- Remove `thinkingPanel` variable and its calculation from `layout()`
- Remove the `if m.showThinking && m.thinkingBuf.Len() > 0` block from viewport height calculation
- The tool panel and viewport divide the available space directly

### Step 4: Rewrite `renderStreamVP()` to inline thinking

**`internal/cli/tui_layout.go`** in `renderStreamVP()`:

```go
func (m *tuiModel) renderStreamVP() {
    m.hitMap.invalidate()
    content := m.buildViewportContent()

    // Append live stream content (assistant text being generated).
    if m.streamBuf.Len() > 0 {
        if content != "" {
            content += "\n"
        }
        content += tuiDimStyle.Render("▌ ") + m.streamBuf.String()
    }

    // Append thinking inline after stream content if present.
    if m.thinkingBuf.Len() > 0 {
        thinkingLines := renderThinkingBlock(
            "thinking-live",
            m.thinkingBuf.String(),
            false, // never collapsed during live stream
            m.liveThinkingScroll,
            m.modelName,
            m.width,
        )
        thinkingStr := strings.Join(thinkingLines, "\n")
        if thinkingStr != "" {
            if content != "" {
                content += "\n"
            }
            content += thinkingStr
        }
    }

    // ... rest unchanged (elapsed indicator, viewport scroll) ...
}
```

### Step 5: Mouse wheel handling for thinking scroll

**`internal/cli/tui_message.go`** — add to the `tea.MouseMsg` handler:

```go
case tea.MouseMsg:
    // ... existing welcome handling ...

    zone, hit := m.hitMap.hit(msg.Y)

    // Check if mouse wheel is over a thinking block.
    if hit && (msg.Type == tea.MouseWheelUp || msg.Type == tea.MouseWheelDown) {
        if zone.kind == hitTranscript && zone.blockID != "" {
            block := m.blockByID(zone.blockID)
            if block != nil && block.Kind == ChatBlockThinking {
                dir := -1
                if msg.Type == tea.MouseWheelUp {
                    dir = -1
                } else {
                    dir = 1
                }
                if zone.blockID == "thinking-live" {
                    m.adjustLiveThinkingScroll(dir)
                } else {
                    m.adjustBlockThinkingScroll(zone.blockID, dir)
                }
                m.renderVP()
                skipViewport = true
                break
            }
        }
    }

    // ... rest of mouse handling unchanged ...
```

Add helper methods:
- `m.blockByID(id string) *ChatBlock` — linear scan `m.blocks` looking for the block
- `m.adjustLiveThinkingScroll(dir int)` — adjust `m.liveThinkingScroll`
- `m.adjustBlockThinkingScroll(id string, dir int)` — adjust `ChatBlock.ScrollOffset`

### Step 6: Remove `showThinking` toggle

**`internal/cli/tui_message.go`**:
- Remove the `case "ctrl+t":` handler that toggles `m.showThinking`
- Remove `m.showThinking = !m.showThinking` and related layout/render calls

**`internal/cli/brand.go`** in `renderStatusBar()`:
- Remove `showThinking bool` parameter and the "thinking on" hint
- Simplify the idle status bar rendering

**`internal/cli/tui_view.go`**:
- Remove `showThinking` from `renderChatView()` / `renderStatusBar()` call

**`internal/cli/tui.go`**:
- Remove `showThinking bool`, `thinkingLines int` from `tuiModel`
- Remove their initialization in `newTUIModel()`

### Step 7: Update `finishStream()` to preserve thinking scroll offset

**`internal/cli/tui_layout.go`** in `finishStream()`:
```go
if thinking := strings.TrimSpace(m.thinkingBuf.String()); thinking != "" {
    m.appendBlock(ChatBlock{
        Kind:          ChatBlockThinking,
        Text:          thinking,
        Collapsed:     false,
        ScrollOffset:  m.liveThinkingScroll, // preserve user's scroll position
    })
}
m.liveThinkingScroll = 0 // reset live offset
```

### Step 8: Clipboard/persistence strip

Verify `HydrateChatBlocks` already handles thinking — it doesn't, because `ChatBlockThinking` is added via `finishStream()`, not from history messages (there's no `RoleThinking` in the provider). This is correct for now: thinking is **ephemeral** and not persisted to session history.

**`internal/cli/tui_slash_handlers.go`** — if `/copy` or clipboard export includes thinking blocks, add a filter to strip them.

---

## Tests

| Test | What it proves |
|------|---------------|
| `TestRenderThinkingBlock_Windowed` | Thinking with >6 lines shows only 5-6 most recent, with "↑ ..." / "↓ ..." indicators |
| `TestRenderThinkingBlock_AllLines` | Thinking with ≤6 lines shows all lines, no scroll indicators |
| `TestRenderThinkingBlock_Collapsed` | Collapsed thinking shows only the header |
| `TestRenderThinkingBlock_ScrollOffset` | Different scroll offsets produce different line windows |
| `TestView_ThinkingInlineInStream` | During streaming, thinking appears inline after stream content in viewport |
| `TestView_ThinkingMouseScroll` | Mouse wheel over thinking block adjusts scroll offset and re-renders |
| `TestLayout_NoThinkingPanelHeight` | The separate thinking panel is gone from layout calculations |
| `TestBrandStatusBar_NoThinkingHint` | Status bar no longer shows "thinking on" hint |

---

## Files changed

| File | Change |
|------|--------|
| `internal/cli/chatblock.go` | Add `ScrollOffset int` field to `ChatBlock` |
| `internal/cli/thinking.go` | Rewrite `renderThinkingBlock()` to accept scrollOffset, window to 6 lines; add helpers |
| `internal/cli/tui.go` | Remove `showThinking`, `thinkingLines`; add `liveThinkingScroll int` |
| `internal/cli/tui_layout.go` | Remove thinking panel from `layout()`; inline thinking in `renderStreamVP()`; preserve offset in `finishStream()` |
| `internal/cli/tui_message.go` | Remove `ctrl+t` handler; add mouse wheel handling for thinking blocks; add `blockByID()`, `adjustLiveThinkingScroll()`, `adjustBlockThinkingScroll()` |
| `internal/cli/tui_view.go` | Remove `showThinking` from `renderChatView()` / `renderChatViewLayout()` |
| `internal/cli/brand.go` | Remove `showThinking` param from `renderStatusBar()`; simplify idle hint |
| `internal/cli/tui_slash_handlers.go` | Strip thinking blocks from clipboard export |
| `internal/cli/chatblock_render.go` | (Maybe) Use `ScrollOffset` in `RenderChatBlocks` for thinking block rendering |

---

## Acceptance gates

1. `go test ./internal/cli/... -count=1` passes all existing and new tests
2. `go test -race ./internal/cli/... -count=1` passes (no data races)
3. `go test ./... -count=1` passes (no regressions)
4. `make verify` passes (or equivalent: vet, build, sec scan)
5. Manual check: start a session, send a message that triggers thinking, verify:
   - Thinking appears inline below streaming text (not in a separate panel)
   - Only 5-6 lines visible
   - Mouse wheel scrolls through thinking content
   - After stream finishes, thinking is a collapsible block in history
   - `ctrl+t` no longer does anything (removed)
6. Manual check: `/copy` does not include thinking text

---

## Visual design

```
  ▌ Let me analyze the code...          ← streaming assistant text
    ▾ thinking                           ← expanded thinking (magenta italic)
      I need to check the imports first
      The user wants to grep for main()
      The function signature is...
      There's an error in line 42
      ↑ ...                              ← more older lines above (if scrolled down)
    ✓ read_file ... parseMainFile        ← tool call (from stream, already works)
    ── done · 12.3s ──
```

Collapsed state (after stream finishes, user presses enter/space on the block):
```
    ▸ thinking                           ← collapsed
    ✓ read_file ... parseMainFile
```
