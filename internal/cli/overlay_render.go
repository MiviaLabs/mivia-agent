package cli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// RenderOverlayWindow renders a windowed selection popup (item rows, selection
// marker, +N more footer) inside a dialog frame. It is the shared engine behind
// the slash-command suggest panel, the composer history picker, and the queue
// manager. An optional width argument (width[0] > 0) widens the window beyond
// the default 72-col cap; callers that pass nothing keep today's behaviour.
func RenderOverlayWindow(rows []string, selected, windowRows, termW, maxH int, title, footer string, width ...int) (string, Rect) {
	if len(rows) == 0 || termW <= 0 || maxH < 1 {
		return "", Rect{}
	}
	w := Min(termW, Max(24, Min(72, termW-4)))
	if len(width) > 0 && width[0] > 0 {
		w = Min(termW, Max(24, Min(width[0], termW-4)))
	}
	visible := Min(windowRows, len(rows))
	// Single-row fallback when the terminal is too short for a framed popup.
	if (footer == "" && maxH < 3) || (footer != "" && maxH < 4) {
		// The caller clamps selection after every drain/delete, but the
		// fallback must survive a stale index on its own: an out-of-range
		// selection here panics the whole render.
		row := "› " + rows[Min(selected, len(rows)-1)]
		if len(rows) > 1 {
			row += fmt.Sprintf("  +%d", len(rows)-1)
		}
		return FitDialogRow(row, w), Rect{W: w, H: 1}
	}
	frameRows := 2
	if footer != "" {
		frameRows = 3
	}
	h := Min(maxH, visible+frameRows)
	if h < frameRows+1 {
		return "", Rect{}
	}
	pageRows := Max(0, h-frameRows)
	start := 0
	if selected >= pageRows && pageRows > 0 {
		start = selected - pageRows + 1
	}
	out := make([]string, 0, pageRows)
	for i := 0; i < pageRows && start+i < len(rows); i++ {
		index := start + i
		prefix := "  "
		if index == selected {
			prefix = "› "
		}
		out = append(out, prefix+rows[index])
	}
	if remaining := len(rows) - (start + pageRows); remaining > 0 {
		if footer != "" {
			footer += "  "
		}
		footer += fmt.Sprintf("+%d more", remaining)
	}
	l := DialogLayout{Rect: Rect{W: w, H: h}, InnerW: Max(0, w-4), PageH: pageRows, FrameCols: 4, FrameRows: frameRows}
	panel := RenderDialogFrame(title, out, footer, l)
	return panel, Rect{W: lipgloss.Width(panel), H: lipgloss.Height(panel)}
}

// renderComposerPopup renders the open composer popup (queue manager, slash
// suggestions, or the sent-message history picker) sized for the chat pane. It
// returns an empty panel when no popup is open. The queue manager is checked
// first: it is a modal, so suggest/history cannot be open at the same time.
func (m *tuiModel) renderComposerPopup() (string, Rect) {
	pane := newChatPaneLayout(m.width, m.sessionsSidebar != nil, m.workflowsSidebar != nil)
	termW := Max(1, pane.chatWidth)
	maxH := Max(0, m.suggestComposerTop()-1)
	if m.queueMgr.open {
		return m.renderQueuePanel(termW, maxH)
	}
	if m.suggest.open {
		return renderSuggestPanel(m.suggest, termW, maxH)
	}
	if m.history.Open {
		return renderHistoryPanel(m.history, m.historyEntries(), termW, maxH)
	}
	return "", Rect{}
}

// renderHistoryPanel renders the composer message-history picker: a 3-row
// windowed popup of the user's sent messages, newest first. Relocated from
// history_overlay_render.go: this file is its sole caller.
func renderHistoryPanel(state HistoryState, entries []string, termW, maxH int) (string, Rect) {
	if !state.Open || len(entries) == 0 || termW <= 0 || maxH < 1 {
		return "", Rect{}
	}
	rows := make([]string, 0, len(entries))
	for _, entry := range entries {
		safe := SafeChatBlockText(entry, 0) // strips CSI/OSC escapes and NUL
		safe = strings.ReplaceAll(safe, "\n", "⏎")
		rows = append(rows, safe)
	}
	return RenderOverlayWindow(rows, state.Selected, 3, termW, maxH, " history "+fmt.Sprintf("(%d)", len(entries)), "↑↓ select · enter recall · esc dismiss")
}
