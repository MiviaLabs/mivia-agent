package cli

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// renderOverlayWindow renders a windowed selection popup (item rows, selection
// marker, +N more footer) inside a dialog frame. It is the shared engine behind
// the slash-command suggest panel and the composer history picker.
func renderOverlayWindow(rows []string, selected, windowRows, termW, maxH int, title, footer string) (string, rect) {
	if len(rows) == 0 || termW <= 0 || maxH < 1 {
		return "", rect{}
	}
	w := min(termW, max(24, min(72, termW-4)))
	visible := min(windowRows, len(rows))
	// Single-row fallback when the terminal is too short for a framed popup.
	if (footer == "" && maxH < 3) || (footer != "" && maxH < 4) {
		row := "› " + rows[selected]
		if len(rows) > 1 {
			row += fmt.Sprintf("  +%d", len(rows)-1)
		}
		return fitDialogRow(row, w), rect{w: w, h: 1}
	}
	frameRows := 2
	if footer != "" {
		frameRows = 3
	}
	h := min(maxH, visible+frameRows)
	if h < frameRows+1 {
		return "", rect{}
	}
	pageRows := max(0, h-frameRows)
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
	l := dialogLayout{rect: rect{w: w, h: h}, innerW: max(0, w-4), pageH: pageRows, frameCols: 4, frameRows: frameRows}
	panel := renderDialogFrame(title, out, footer, l)
	return panel, rect{w: lipgloss.Width(panel), h: lipgloss.Height(panel)}
}

// renderComposerPopup renders the open composer popup (slash suggestions or
// the sent-message history picker) sized for the chat pane. It returns an
// empty panel when neither popup is open.
func (m *tuiModel) renderComposerPopup() (string, rect) {
	pane := newChatPaneLayout(m.width, m.sessionsSidebar != nil, m.workflowsSidebar != nil)
	termW := max(1, pane.chatWidth)
	maxH := max(0, m.suggestComposerTop()-1)
	if m.suggest.open {
		return renderSuggestPanel(m.suggest, termW, maxH)
	}
	if m.history.open {
		return renderHistoryPanel(m.history, m.historyEntries(), termW, maxH)
	}
	return "", rect{}
}
