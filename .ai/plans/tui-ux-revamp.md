# TUI UX Revamp — Implementation Plan

## Current State Assessment

The TUI has a functional foundation (Bubble Tea, blocks model, focus system, mouse hit-map) but suffers from several critical rendering and UX issues that make it feel broken for a chat application.

## Critical Bugs (Broken Now)

### Bug 1: Assistant Message Cards Have No Footer

**File:** `internal/cli/chatblock_render.go:32-36`

`ChatBlockUser` AND `ChatBlockAssistant` are handled identically:

```go
case ChatBlockUser, ChatBlockAssistant:
    lines = RenderMessageForHistory(providerMessageForBlock(block, text), model, width)
```

`RenderMessageForHistory` for `RoleAssistant` returns:
- `formatModelHeader(modelName, w)` → `╭─ modelname ────`
- Content (markdown or tool lines)
- **NO closing footer**

But `formatUserMessageCard` returns:
- `╭─ you ────────────╮`
- `│ content          │`
- `╰─────────────────╯`

**Result:** Assistant messages have an open/unclosed header while user messages are fully bordered. Looks broken.

**Fix:** `RenderMessageForHistory` for assistant role must append `formatModelFooter(w)` as a closing line. Or `RenderChatBlocks` should add it after the assistant block.

### Bug 2: Duplicated Model Header in Live Stream

**File:** `internal/cli/tui.go:392` (startAI)

```go
m.appendMsg(formatModelHeader(m.modelName, cardW))
```

This creates a `ChatBlockSystem` (pre-rendered header line) BEFORE the assistant starts streaming.

Then `finishStream` creates a `ChatBlockAssistant` block which when rendered ALSO calls `formatModelHeader` via `RenderMessageForHistory`.

**Result:** Two headers shown per assistant turn.

**Fix:** Remove the `appendMsg(formatModelHeader(...))` from `startAI`. The assistant block rendering should be the sole source of the header.

### Bug 3: liveThinkingScroll Field Never Updated

**File:** `internal/cli/tui.go:82` (struct field)

`liveThinkingScroll int` is initialized to 0 and never modified anywhere except in `finishStream` where it's reset. There's no handler for mouse wheel scrolling over thinking blocks (the `adjustThinkingScroll` method mentioned in mouse handlers doesn't exist or does nothing).

**Result:** Thinking blocks always show the most recent lines regardless of scroll attempts.

**Fix:** Implement `adjustThinkingScroll` or remove the field if thinking blocks don't need scrolling.

### Bug 4: ChatBlockAssistant in History Doesn't Close Turn Divider

**File:** `internal/cli/tui_layout.go:169-192` (finishStream)

After appending the assistant block, `finishStream` appends a `ChatBlockSystem` message for duration:

```go
m.appendMsg(tuiDimStyle.Render(fmt.Sprintf("  ─ done · %s ─", formatDuration(total))))
```

This appears as a system message mixed into the transcript. Turn transitions between user/assistant rounds aren't visually clean.

---

## Usability Issues (Degraded Experience)

### Issue 5: User Messages Lack Visual Weight

Current user card uses only a thin border in `tuiDimStyle` (ANSI 8, gray). The "you" label is bold blue now, but the card body has no background fill. In a busy terminal, user messages blend with assistant responses.

**Fix (partial):** `tuiUserCardBg` style was added (ANSI 235 dark gray background) to user card content lines. Verify this is rendering correctly.

### Issue 6: Tool Strip Above Composer is Distracting

The live tool strip (`toolpanel.go`) shows a windowed view of running/completed tools above the composer. This:
- Eats vertical space from the transcript
- Shows tool calls that also appear in history (redundant)
- Doesn't persist (tools disappear when turn ends)
- Competes with the chat history for visual attention

**Better approach:** Show a minimal status line during tool execution (e.g., `◐ read_file · 2 running · 3 completed`) and render all tool results as collapsible blocks in history only.

### Issue 7: Chat History Lacks Thread/Conversation Structure

Messages appear as flat blocks with no visual grouping by turn. After a few exchanges, it's hard to tell which assistant response goes with which user message.

**Fix:** Group user + assistant + tool blocks from one turn into a visual unit. Add a subtle turn divider or indentation.

### Issue 8: Status Bar is Cluttered

The status bar shows: brand glyph, model name, waiting state, elapsed time, tool counts, queue length, message count, step detail. For a chat app, this is too much information in one line.

**Fix:** Show only essential info: brand glyph + model name + brief state indicator. Move secondary info to a help line or hide behind `/status`.

### Issue 9: No Visual Scroll Position Indicator

When scrolled up in history, a `↓ more below` hint appears but there's no indicator of *how far* scrolled up or where "bottom" is. Chat apps like iMessage/Slack show a "Jump to bottom" button.

**Fix:** Show a floating "↓ N new messages" indicator when scrolled up during streaming.

### Issue 10: Focus State Not Visually Distinct

When switching focus between composer, scrollback, and tools, only the composer border color changes (focused → teal `tuiInfoStyle`, waiting → gray `tuiDimStyle`). The scrollback/tools panes have no focus indicator.

**Fix:** Add a subtle left-border highlight on the active pane, or change the selected block background.

### Issue 11: Welcome Screen Session Picker Lacks Visual Polish

The session list is plain text with no icons, dates, or preview. Auto-saved sessions show "Auto · 5m ago" but the format is inconsistent.

---

## Implementation Plan

### Phase 1: Fix Critical Rendering Bugs

#### 1a: Close Assistant Message Cards

**File:** `internal/cli/renderer.go` — `RenderMessageForHistory` for `RoleAssistant`

Add `formatModelFooter(w)` as the last line of the assistant result:

```go
case provider.RoleAssistant:
    var lines []string
    // ... existing tool call and content lines ...
    if len(lines) == 0 {
        return nil
    }
    header := formatModelHeader(modelName, w)
    footer := formatModelFooter(w)
    result := make([]string, 0, len(lines)+2)
    result = append(result, header)
    result = append(result, lines...)
    result = append(result, footer)
    return result
```

**Test:** `TestRenderMessageForHistory_AssistantNoTools` must include footer in expected output.

#### 1b: Remove Duplicate Model Header from startAI

**File:** `internal/cli/tui.go:387-392` — Remove:
```go
m.appendMsg(formatModelHeader(m.modelName, cardW))
```

The assistant block rendering in `RenderChatBlocks` → `RenderMessageForHistory` already produces the header.

**Test:** Verify `finishStream` output produces exactly one header per turn.

#### 1c: Fix Turn Divider / Duration Display

**File:** `internal/cli/tui_layout.go:189-192` (finishStream)

Replace the system-message duration line with a proper `ChatBlockDivider` that shows duration:

```go
duration := formatDuration(time.Since(m.turnStart))
m.appendBlock(ChatBlock{
    Kind: ChatBlockDivider,
    Text: duration,
    Rendered: tuiDimStyle.Render(fmt.Sprintf("  ─── · %s · ───", duration)),
})
```

---

### Phase 2: Chat History Redesign

#### 2a: Group User + Assistant + Tools as Visual Turns

**File:** `internal/cli/chatblock_render.go` — `RenderChatBlocks`

Add a `chatBlockGroup` concept: when rendering blocks:
- A `ChatBlockUser` starts a new group
- All subsequent blocks (assistant, tool, thinking) belong to the same group
- A `ChatBlockDivider` or the next `ChatBlockUser` closes the group

Groups get a subtle left border (`lipgloss.NewStyle().BorderLeft(true).BorderStyle(lipgloss.NormalBorder())`) or subtle background tint.

#### 2b: Per-Turn Tool Grouping

**File:** `internal/cli/tui_layout.go:171-183` (finishStream)

Instead of one `ChatBlockTool` per tool row, collect all tools from one turn into a single collapsible `ChatBlockTool` section:

```go
if len(m.toolRows) > 0 {
    var toolLines []string
    for _, r := range m.toolRows {
        item := newToolRenderItem(r.Name, r.Detail, r.Result, r.Done, r.Failed)
        opts := terminalToolRenderOptions()
        toolLines = append(toolLines, formatToolLine(item, m.width, opts))
    }
    m.appendBlock(ChatBlock{
        Kind:      ChatBlockTool,
        ToolName:  "tools",
        Text:      strings.Join(toolLines, "\n"),
        Collapsed: true, // collapsed by default
    })
}
```

#### 2c: Remove Live Tool Strip

**File:** `internal/cli/tui_view.go:58-74` (toolStrip section)

Replace the live tool strip with a minimal status line:

```go
toolStatus := ""
if m.waiting && len(m.toolRows) > 0 {
    open, done, total := countTools(m.toolRows)
    toolStatus = tuiDimStyle.Render(fmt.Sprintf("  ◐ %d running · %d done · %d total", open, done, total))
}
```

Remove `renderToolPanelWindow` call. Keep only `m.toolPanel` for tracking state. Remove `toolMaxLines` from layout.

**Files to delete/trim:** `toolpanel.go`, parts of `tui_layout.go`, `toolui.go` (keep `formatToolLine` for history rendering).

---

### Phase 3: Visual Polish

#### 3a: User Message Background

Already partially done (`tuiUserCardBg` style added). Verify the background renders across terminals. Ensure `NO_COLOR` env works.

#### 3b: Status Bar Simplification

**File:** `internal/cli/brand.go` — `renderStatusBar`

Reduce to: `[brand] modelname [state]` where state is one of:
- `idle` (waiting for input)
- `◐ working` (processing)
- `⏎ queue N` (messages queued)
- `✓ done · 12s`

Move step detail and tool counts to the hint line below composer or hide entirely.

#### 3c: Scroll-to-Bottom Indicator

**File:** `internal/cli/tui_view.go:45-47`

When not `AtBottom()` and streaming:
```
┌──────────────────────────────┐
│ ↓ N new messages · click/G   │
└──────────────────────────────┘
```

Use a floating footer overlay or a persistent bottom bar.

#### 3d: Focus Highlight

Add a 1-char left border (`▎`) on the focused pane:
- `focusComposer` → blue left border on composer card
- `focusScrollback` → subtle left border on selected block
- `focusTools` → highlight on selected tool row

---

### Phase 4: Welcome Screen Refinement

#### 4a: Session Preview

Show first 2 lines of the last assistant message in each session entry, plus message count and relative time.

#### 4b: Visual Polish

- Add icons for auto/named sessions
- Add a "New chat" button or prominent hint
- Show keyboard shortcuts inline

---

## Acceptance Criteria

### Phase 1 (Critical Fixes)
- [ ] Assistant messages render with both top header AND bottom footer in history
- [ ] No duplicate model headers in streamed responses
- [ ] Turn duration shows as a proper divider, not a system message
- [ ] `go test ./internal/cli/ -count=1` passes
- [ ] `go test -race ./internal/cli/ -count=1` passes
- [ ] `python3 scripts/check_go_structure.py --all` passes

### Phase 2 (History Redesign)
- [ ] User + assistant + tools from same turn are visually grouped
- [ ] All tools from one turn appear as a single collapsible block in history
- [ ] Live tool strip is replaced with minimal status line
- [ ] `toolpanel.go` removed or simplified
- [ ] `finishStream` no longer references `toolRows` for history rendering

### Phase 3 (Visual Polish)
- [ ] Status bar shows only essential info
- [ ] Scroll-to-bottom indicator works during streaming
- [ ] Focus state is visually distinct across all 3 panes
- [ ] User cards have visible background in color terminals

### Phase 4 (Welcome)
- [ ] Session picker shows preview of last response
- [ ] Keyboard shortcuts visible inline
- [ ] Mouse click opens sessions on single click (not double)

---

## File Change Summary

| File | Phase | Change |
|------|-------|--------|
| `internal/cli/renderer.go` | 1 | Add footer to assistant cards |
| `internal/cli/tui.go` | 1 | Remove duplicate model header from startAI |
| `internal/cli/tui_layout.go` | 1,2 | Fix finishStream: proper divider, grouped tools |
| `internal/cli/chatblock_render.go` | 2 | Add turn-group rendering |
| `internal/cli/chatblock.go` | 2 | Add group/thread metadata if needed |
| `internal/cli/tui_view.go` | 2,3 | Remove tool strip, add scroll indicator |
| `internal/cli/brand.go` | 3 | Simplify status bar |
| `internal/cli/toolpanel.go` | 2 | Remove or simplify |
| `internal/cli/toolui.go` | 2 | Keep formatToolLine, trim others |
| `internal/cli/welcome.go` | 4 | Session preview, polish |
| `internal/cli/tui_focus.go` | 3 | Add visual focus indicator |
| `.ai/policy/go-structure.json` | 2 | Update baselines |
