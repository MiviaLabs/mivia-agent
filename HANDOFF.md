# Mivia Agent — Handoff Document

## Current State

Commit `5ba7463` pushed to master. 10/10 Go packages pass `-race`.

## What's Working

- Full Bubble Tea TUI with viewport, textarea, spinner
- Message queueing (Enter while agent busy queues, Enter empty force-sends)
- Tool panel with expand/collapse, selection via Tab, thinking toggle
- Scroll-to-bottom indicator (↓ button when scrolled up)
- Escape key to deselect/collapse tools
- Ctrl+C to cancel in-flight requests
- Markdown rendering: **bold**, *italic*, `code`, ```code blocks```, # headings, - lists, > quotes, --- horizontal rules
- **Table rendering** — works correctly in `RenderMarkdown` (ANSI dim borders with `│`), tested
- **Word wrapping** — `wrapANSIv2` handles ANSI + UTF-8 correctly, wraps at spaces, preserves CJK width
- Session persistence (auto-save, save/load/list/delete)
- File size protection at write time (500 KiB) and pre-commit
- `/search` routes through AI

## Known Issues

### 1. Infinite Scroll NOT Working
The `loadMoreMessages()` function exists and `msgOffset` tracking is in place, but **scrolling up doesn't trigger loading older messages**.

**Root cause analysis**: The check is in the bottom type switch in `Update()`:
```go
case tea.KeyMsg:
    k := v.String()
    if k == "up" ... {
        m.viewport, vpCmd = m.viewport.Update(v)
    }
    if k == "pgup" || k == "up" || k == "home" {
        if m.msgOffset > 0 && m.viewport.YOffset <= 0 && ... {
            m.loadMoreMessages()
        }
    }
```

**Probable causes**:
1. `viewport.Update(v)` resets YOffset to 0 after processing "up", but `TotalLineCount()` might not account for ANSI-wrapped lines correctly
2. The viewport's `TotalLineCount()` returns raw newline count, not visual line count — if messages are single lines, they fit in viewport
3. The check `TotalLineCount() > Height` might be false for short histories

**Fix needed**:
- Force `loadMoreMessages()` when user presses "up" at the very beginning regardless of `TotalLineCount()` vs `Height`
- Add debug logging to trace the conditions
- Consider using a threshold like `YOffset < 3` instead of `YOffset <= 0`

### 2. loadMoreMessages Wrapping Missing
In `loadMoreMessages()`, assistant content is rendered with `RenderMarkdown` but **NOT wrapped** with `wrapANSIv2`. This means loaded messages may overflow viewport width.

**Fix**: Add `wrapANSIv2(content, m.width-4)` after `RenderMarkdown`.

In `runTUI()` startup, messages are also not wrapped. Fix: wrap with `wrapANSIv2` before `appendMsg`.

### 3. SubAgent / Multi-Agent Architecture NOT Implemented
The user wanted subagent capability — being able to spawn child agent processes for parallel work (e.g., web research while continuing to chat).

**No implementation exists.** This requires:
- New package `internal/subagent/` or integration with existing agent loop
- Ability to create child `Session` instances with their own provider clients
- A new tool `run_agent(prompt, tools[])` or similar
- Message routing between parent and child agents
- Resource limits (context budget sharing, max concurrent agents)

### 4. `/bug-audit` Command
The user uses `/bug-audit` as a prompt instruction rather than a CLI command. There's no actual `/bug-audit` slash command handler. This is fine — it's a meta-instruction for the AI.

## Next Priorities

1. **Fix infinite scroll** — debug `loadMoreMessages()` trigger conditions
2. **Add wrapping to `loadMoreMessages` and `runTUI` startup** — use `wrapANSIv2`
3. **Implement subagent system** — `internal/subagent/` with `run_agent` tool
4. **DuckDuckGo web search is broken** — returns challenge page. Need to add browser-like headers or use alternative search API
5. **Full history load** — when at top of loaded history, show "Press L to load full history" option
6. **Performance optimizations** for very large chat sessions (10k+ messages)

## Code Map

| File | Purpose | Lines |
|------|---------|-------|
| `internal/cli/tui.go` | Bubble Tea model, viewport, input, streaming, infinite scroll | ~1450 |
| `internal/cli/markdown.go` | Streaming markdown→ANSI converter | ~450 |
| `internal/cli/toolui.go` | Tool panel rendering (expandable rows) | ~160 |
| `internal/cli/chat.go` | Chat command, `runTUI()` entry point | ~200 |
| `internal/cli/renderer.go` | ChatRenderer for `--plain` mode | ~150 |
| `internal/cli/input.go` | `InputBuffer` for `--plain` mode | ~250 |
| `internal/cli/terminal.go` | Raw terminal wrapper with bracketed paste | ~120 |
| `internal/cli/dialog.go` | Help dialog with ANSI borders | ~150 |
| `internal/agent/loop.go` | Tool-calling agent loop with parallel execution | ~270 |
| `internal/chat/session.go` | Multi-turn session state | ~200 |
| `internal/chat/persistence.go` | JSONL session save/load | ~350 |
| `internal/tools/tools.go` | Tool registry, `NewDefaultRegistry`, 8 tools | ~160 |
| `internal/tools/searcher.go` | Unified search (local/web/url) | ~330 |
| `internal/tools/write.go` | write_file + search_replace (with size limit) | ~120 |

## Key Dependencies

- `github.com/charmbracelet/bubbletea v1.3.10` — TUI framework
- `github.com/charmbracelet/bubbles v1.0.0` — viewport, textarea, spinner
- `github.com/charmbracelet/lipgloss v1.1.0` — styling

## Build & Test

```bash
go test -race ./...
go vet ./...
go build -o mivia ./cmd/mivia
```
