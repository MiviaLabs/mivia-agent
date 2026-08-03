# Option C — Drag to Copy (Final Validated Plan)

> **Status:** Reviewed, challenged, and validated
> **Date:** 2025-07-14
> **Scope:** Chat transcript drag-to-copy + whole-composer drag-to-copy
> **Bubbletea:** v1.3.10 (uses `msg.Action`, NOT v2 event types)

---

## What Was Challenged & Fixed

| Issue from review | Resolution in final plan |
|---|---|
| 🔴 **Wrong event type names** — plan assumed v2 API (`MouseLeftDown/Up/Click/Motion`) | Use **`msg.Action`** field (`MouseActionPress`, `MouseActionMotion`, `MouseActionRelease`) which exists in v1.3.10. `msg.Type` is a legacy compat field that conflates press and motion. |
| 🔴 **`applySelectionChrome` refactor too invasive** (70+ references) | Add a **separate `applyDragHighlight()`** function. Leave `applySelectionChrome` untouched. |
| 🔴 **`renderVP()` on motion invalidates hit map** | **Never call `renderVP()` during drag motion**. Only on press (initial chrome) and release (clear). Apply drag chrome by mutating `m.messages[i]` in-place. |
| 🟠 **No visual feedback on press** (if click deferred to release) | Set `selectedBlockID` + `renderVP()` + `setFocus(focusScrollback)` **on press immediately**. Double-click logic fires on below-threshold release. |
| 🟠 **Composer textarea fork is disproportionate** | **Dropped character-level composer drag** from v1 scope. Use **whole-composer-text drag** instead: drag in composer → copy entire composer value. Matches chat blocks pattern. F2/select mode still exists for character-level terminal-native selection. |
| 🟠 **Auto-copy unbounded block count** | Cap at **10 blocks**. Show real-time count during drag. |
| 🟠 **2 tests will break** (`TestTUIMouseHitTranscriptBlockClick`, `TestTUIMouseHitComposerClick`) | Update tests to send press + release sequences. Tests that call `handleTranscriptBlockClick` directly remain unchanged. |
| 🟡 **Out-of-bounds coordinates on terminal exit** | Clamp all mouse coordinates to `[0, width)` × `[0, height)`. |
| 🟡 **Modal opens mid-drag** | Cancel drag in all modal-open paths. |
| 🟡 **`pendingCopyCmd` unnecessary** | Return `copyToClipboardCmd` directly as part of `Update()` return batch. |
| 🟡 **Drag copy diverges from right-click copy text extraction** | Reuse `selectedBlockCopyText` logic (extract to a `blockSourceText(id)` helper). |

---

## Architecture

### Gesture State Machine

```
MousePress (button=left, action=press)
  ├─ In transcript zone: set selectedBlockID, renderVP, setFocus(scrollback)
  │   Record dragState: {origin, startBlockID, dragging:false, blockIDs:[startBlock]}
  │
  ├─ In composer zone: record composerDrag: {origin, active:true}
  │
  └─ Other zones: ignore

MouseMotion (button=left, action=motion)
  ├─ dragState active (transcript):
  │   If !dragging: check Manhattan dist from origin
  │     If >= threshold(4): dragging=true
  │   If dragging: hit-test Y → blockID, accumulate ordered set (cap 10)
  │     Apply drag highlight in-place on m.messages (NO renderVP)
  │
  ├─ composerDrag active:
  │   If Manhattan dist >= threshold(4): composerDragging=true
  │
  └─ No drag active: ignore (existing scroll-drag behavior unchanged)

MouseRelease (action=release)
  ├─ dragState active (transcript):
  │   If dragging && len(blockIDs) > 0:
  │     Extract source text for all blockIDs via blockSourceText()
  │     Return copyToClipboardCmd(text) in Update batch
  │     Show "copied N chars" notice
  │   If NOT dragging (below threshold):
  │     Call handleTranscriptBlockClick(startBlockID)
  │     (fires single-click select + double-click toggle logic)
  │   Clear dragState, renderVP (to clear drag chrome)
  │
  ├─ composerDrag active:
  │   If composerDragging:
  │     Copy entire m.textarea.Value() via copyToClipboardCmd
  │     Show "copied N chars" notice
  │   Clear composerDrag
  │
  └─ No drag active: ignore
```

---

### New State Fields on `tuiModel`

```go
// In tui.go, after selectedBlockID (line ~96):
dragState *dragState

// Drag-to-copy state for transcript zone.
type dragState struct {
    originX, originY int       // press cell (clamped to viewport)
    startBlockID      string    // block at press point
    dragging          bool      // true after threshold exceeded
    blockIDs          []string  // ordered, deduped block IDs (max 10)
}
```

### New State Fields for Composer

```go
// In tui.go:
composerDragActive bool       // true between press and release in composer
composerDragging   bool       // true after threshold exceeded
composerDragOriginX int
composerDragOriginY int
```

---

### New/Modified Functions

| File | Function | Purpose |
|------|----------|---------|
| `tui_selection.go` | `applyDragHighlight(lines, ranges, blockIDs)` | In-place ANSI highlight on m.messages for all dragged blocks |
| `tui_selection.go` | `blockSourceText(id string) string` | Extract source text (or stripANSI fallback) for a block ID |
| `tui_message.go` | `handleDragMotion(msg)` | Accumulate blocks during drag, apply highlight |
| `tui_message.go` | `handleDragRelease()` | Finalize: copy or click, clear state |
| `tui_message.go` | `clampMouseCoords(msg)` | Clamp X/Y to [0, width/height) |
| `tui.go` | `cancelDrag()` | Reset dragState + composerDrag state |

---

### Changes to `tui_message.go` (Mouse Dispatch)

**Current** (line 93-110, 156-210): Only checks `msg.Type == tea.MouseLeft`/`tea.MouseRight`/`tea.MouseWheel*`.

**New dispatch order** (inside `updateMessageImpl`, the `tea.MouseMsg` case):

```go
case tea.MouseMsg:
    m.disarmQuit()

    // 1. Clamp coordinates
    msg = clampMouseCoords(msg)

    // 2. Modal gating (unchanged — handleModalMouse first)
    if m.handleModalMouse(msg, &skipViewport) { break }

    // 3. Right-click (unchanged)
    if msg.Type == tea.MouseRight { ... }

    // 4. Wheel (unchanged)
    if msg.Type == tea.MouseWheelUp || msg.Type == tea.MouseWheelDown { ... }

    // 5. NEW: Mouse motion during drag
    if msg.Action == tea.MouseActionMotion {
        if m.dragState != nil {
            if cmd := m.handleDragMotion(msg); cmd != nil {
                cmds = append(cmds, cmd)
            }
            break
        }
        // existing scroll-drag behavior for non-drag motions
        if m.handleMouseMsg(msg, &skipViewport) { break }
        break
    }

    // 6. NEW: Mouse release during drag
    if msg.Action == tea.MouseActionRelease {
        if m.dragState != nil || m.composerDragActive {
            cmds = append(cmds, m.handleDragRelease(msg))
            break
        }
        break
    }

    // 7. Left press (existing + NEW drag initiation)
    if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
        if m.handleMouseMsg(msg, &skipViewport) { break }
        break
    }

    // 8. Fallback: existing handleMouseMsg (for legacy Type-based dispatch)
    if m.handleMouseMsg(msg, &skipViewport) { break }
```

### Changes to `handleMouseMsg` (line 156):

```go
func (m *tuiModel) handleMouseMsg(msg tea.MouseMsg, skipViewport *bool) bool {
    // NEW: On left press in transcript, initiate drag tracking
    if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
        zone, hit := m.hitMap.hit(msg.Y)
        if hit && zone.kind == hitTranscript && zone.blockID != "" {
            // Start drag state
            m.dragState = &dragState{
                originX:    msg.X,
                originY:    msg.Y,
                startBlockID: zone.blockID,
                blockIDs:    []string{zone.blockID},
            }
            // Immediate visual feedback (existing behavior)
            m.selectedBlockID = zone.blockID
            m.setFocus(focusScrollback)
            m.renderVP()
            return true
        }
        // NEW: On left press in composer, initiate composer drag
        if hit && zone.kind == hitComposer {
            m.composerDragActive = true
            m.composerDragging = false
            m.composerDragOriginX = msg.X
            m.composerDragOriginY = msg.Y
            m.setFocus(focusComposer)
            return true
        }
    }

    // ... existing Type-based wheel and click handling ...
    // NOTE: The existing msg.Type == tea.MouseLeft branch must be
    // guarded: only fire if m.dragState == nil (press already handled above)
}
```

---

### `applyDragHighlight` — New Function

```go
// In tui_selection.go
// applyDragHighlight mutates lines in-place, applying selection chrome
// to all line ranges belonging to the given block IDs.
func applyDragHighlight(lines []string, ranges map[string][2]int, blockIDs []string) {
    if len(blockIDs) == 0 {
        return
    }

    // Build set of line indices to highlight
    highlightRows := make(map[int]bool)
    for _, id := range blockIDs {
        r, ok := ranges[id]
        if !ok { continue }
        for i := r[0]; i < r[1] && i < len(lines); i++ {
            highlightRows[i] = true
        }
    }

    sel := lipgloss.NewStyle().Background(lipgloss.Color(themeColorSelBg))
    for i := range highlightRows {
        plain := stripANSI(lines[i])
        if strings.TrimSpace(plain) == "" { continue }
        // Pad to visible width to avoid repaint artifacts
        vis := visibleWidth(plain)
        if vis < 1 { continue }
        lines[i] = sel.Render(plain) // replaces line with highlighted version
    }
}
```

### `blockSourceText` — New Helper

```go
// In clipboard.go (or tui_selection.go)
// blockSourceText returns the source text for a block, reusing the same
// logic as selectedBlockCopyText but parameterized by ID.
func (m *tuiModel) blockSourceText(id string) string {
    for i := range m.blocks {
        if m.blocks[i].ID != id { continue }
        b := m.blocks[i]
        if b.Text != "" { return b.Text }
        return stripANSI(b.Rendered)
    }
    return ""
}
```

### `handleDragRelease` — New Function

```go
// In tui_message.go
func (m *tuiModel) handleDragRelease(msg tea.MouseMsg) tea.Cmd {
    // --- Transcript drag ---
    if m.dragState != nil {
        ds := m.dragState
        m.dragState = nil // clear BEFORE any re-render

        if ds.dragging && len(ds.blockIDs) > 0 {
            // Auto-copy: concatenate source text of all dragged blocks
            var text strings.Builder
            for _, id := range ds.blockIDs {
                src := m.blockSourceText(id)
                if src != "" {
                    if text.Len() > 0 { text.WriteString("\n\n") }
                    text.WriteString(src)
                }
            }
            m.renderVP() // clear drag chrome
            result := text.String()
            if result != "" {
                m.selectedBlockID = ds.blockIDs[0] // keep first block selected
                return copyToClipboardCmd(result)
            }
            return nil
        }

        // Below threshold — treat as click
        m.renderVP() // clear any partial chrome
        m.handleTranscriptBlockClick(ds.startBlockID)
        return nil
    }

    // --- Composer drag ---
    if m.composerDragActive {
        m.composerDragActive = false
        if m.composerDragging {
            val := m.textarea.Value()
            m.composerDragging = false
            if val != "" {
                return copyToClipboardCmd(val)
            }
        }
        return nil
    }

    return nil
}
```

---

## Edge Cases & Guards

| Edge Case | Where handled |
|---|---|
| Modal opens mid-drag | `cancelDrag()` called in `setOverlay()`, `closeModal()`, any modal-open path |
| Window resize during drag | `cancelDrag()` in `tea.WindowSizeMsg` handler |
| Mouse exits terminal (out-of-bounds coords) | `clampMouseCoords()` clamps to `[0, width) × [0, height)` |
| Double-click fires on below-threshold release | `handleDragRelease` calls `handleTranscriptBlockClick` which has existing 400ms timer |
| Dividers/empty blocks in drag | `blockSourceText` returns `""` for empty — no text contributed |
| Cap at 10 blocks | `handleDragMotion` checks `len(ds.blockIDs) >= 10` before appending |
| Drag during streaming | Hit map is rebuilt on every `View()` — motion events use current frame's hit map. Safe because bubbletea is single-threaded. |

---

## Hint Text Updates

**File:** `tui_view.go` (or wherever hint bar is rendered)

| Current | New |
|---|---|
| `"shift+drag or F2 to select"` | `"drag to copy · F2 select mode"` |

**Select mode hint stays unchanged** (F2 releases mouse capture to terminal).

---

## Test Updates

| Test | Action |
|---|---|
| `TestTUIMouseHitTranscriptBlockClick` | **Update**: send press + below-threshold release (two `tea.MouseMsg` events with `Action` field) |
| `TestTUIMouseHitComposerClick` | **Update**: send press + release pair |
| `TestTUIMouseDoubleClickTogglesWorkGroup` | **No change** — calls `handleTranscriptBlockClick` directly |
| `TestTUIMouseDoubleClickTogglesToolBlock` | **No change** — same |
| `TestApplySelectionChrome_LineCountStable` | **No change** — `applySelectionChrome` signature unchanged |
| `TestTUIMouseStaleCoordinates` | **No change** |

### New Tests

| Test | What it verifies |
|---|---|
| `TestTUIMouseDragSingleBlock` | Press, move ≥4, release on same block → returns `copyToClipboardCmd` with that block's text |
| `TestTUIMouseDragMultipleBlocks` | Press on A, drag through B, release on C → copies A+B+C concatenated |
| `TestTUIMouseDragBelowThresholdIsClick` | Press + release within 3 cells → fires `handleTranscriptBlockClick` (not copy) |
| `TestTUIMouseDragBlockCap` | Drag across >10 blocks → only first 10 accumulated |
| `TestTUIMouseDragComposer` | Press in composer, move ≥4, release → copies entire textarea value |
| `TestTUIMouseDragCancelOnResize` | Window resize during drag → `dragState == nil` |
| `TestTUIMouseDragCancelOnModal` | Modal opens during drag → `dragState == nil` |
| `TestApplyDragHighlight_MaintainsLineCount` | `applyDragHighlight` doesn't change `len(lines)` |
| `TestApplyDragHighlight_HighlightsCorrectRows` | Only rows in block ranges get highlight styling |

---

## Files Changed (Final List)

| File | Change |
|---|---|
| `internal/cli/tui.go` | Add `dragState *dragState`, `composerDragActive/Dragging/OriginX/Y` fields |
| `internal/cli/tui_message.go` | Add Action-based dispatch (motion/release/press). Guard Type-based dispatch. Add `handleDragMotion`, `handleDragRelease`, `clampMouseCoords`, `cancelDrag` |
| `internal/cli/tui_selection.go` | Add `applyDragHighlight(lines, ranges, blockIDs)`, `blockSourceText(id)` |
| `internal/cli/clipboard.go` | (Optional) refactor `selectedBlockCopyText` to use `blockSourceText` |
| `internal/cli/tui_view.go` | Update hint text |
| `internal/cli/tui_mouse_test.go` | Update 2 existing tests; add 9 new tests |
| `internal/cli/tui_selection_test.go` | Add 2 new tests |

### Files NOT Changed

| File | Why |
|---|---|
| `applySelectionChrome` | Signature unchanged — separate `applyDragHighlight` added |
| `chatblock.go` | Callers of `applySelectionChrome` untouched |
| `tui_hitmap.go` | Already provides `hit(y) → (zone, bool)` — sufficient |
| `tui_composer.go` | No character-level selection — whole-value copy on drag |
| `clipboard.go` (mostly) | `copyToClipboardCmd(text)` already works for arbitrary text |
| `tui_run.go` | Mouse mode (`WithMouseCellMotion`) already provides press/motion/release |

---

## Implementation Order

1. **Add state fields** to `tuiModel` — purely additive, no behavior change
2. **Add `clampMouseCoords` + `cancelDrag`** — pure helpers
3. **Add `blockSourceText`** — pure function, test independently
4. **Add `applyDragHighlight`** — pure function, test independently
5. **Modify `handleMouseMsg`** — add press-initiated drag tracking with `msg.Action`
6. **Add `handleDragMotion` + `handleDragRelease`** — new dispatch paths
7. **Modify top-level dispatch** in `updateMessageImpl` — route motion/release
8. **Add modal/resize cancel guards**
9. **Update hint text**
10. **Update existing tests + add new tests**

---

## Residual Risks

| Risk | Severity | Mitigation |
|---|---|---|
| `msg.Type` conflates press and motion in bubbletea v1 | 🟡 Medium | Use `msg.Action` exclusively for new code; existing `msg.Type` paths remain gated by `dragState == nil` |
| Some terminals don't report motion events | 🟡 Medium | If no motion events arrive, drag never activates — falls back to click behavior. Graceful degradation. |
| Composer whole-value copy (not character-level) may surprise users | 🟡 Low | F2/select mode exists for character-level terminal selection. Can add character-level composer in v2 if needed. |
| 64KB OSC 52 cap on very long drags | 🟢 Low | Existing `copyToClipboardCmd` already handles this with local binary fallback + honest failure notice |
| Existing `msg.Type == tea.MouseLeft` path could still fire for motion events in v1 compat mode | 🟡 Medium | Guard: `if m.dragState == nil && msg.Action == tea.MouseActionPress` before Type-based path |
