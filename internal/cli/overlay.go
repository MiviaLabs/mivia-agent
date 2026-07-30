// Block detail overlay: the full-screen pager every inline truncation leads
// to. Inline rendering caps tool output and thinking windows to keep the
// transcript scannable; the overlay is the doorway to the complete content —
// scrollable, redacted by the same privacy rule as inline expansion, and
// dismissed with esc. It renders instead of the chat frame while open; the
// poll/tick machinery underneath is untouched (INV-TUI-2).
package cli

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type blockOverlay struct {
	title   string
	lines   []string
	yOffset int
}

// newBlockOverlay builds the pager for one block: typed-glyph header
// (name, agent badge, duration, status) over the full redacted content.
func newBlockOverlay(block ChatBlock) *blockOverlay {
	title := toolIconForName(block.ToolName) + " " + block.ToolName
	if block.Kind == ChatBlockThinking {
		title = "▾ thinking"
	}
	if block.AgentName != "" {
		title += "  ◆ " + block.AgentName
	}
	if block.Elapsed > 0 {
		title += "  · " + formatDuration(block.Elapsed)
	}
	switch {
	case block.Failed:
		title += "  ✗"
	case block.Kind == ChatBlockTool && block.Elapsed > 0:
		title += "  ✓"
	}
	// Same privacy rule as inline expansion: redact before render.
	content := redactPreview(SafeChatBlockText(block.Text, 0))
	return &blockOverlay{title: title, lines: strings.Split(content, "\n")}
}

// overlayPageH is the content height at a terminal height (frame = 4 rows).
func overlayPageH(termH int) int {
	if termH-4 < 1 {
		return 1
	}
	return termH - 4
}

// scroll moves the window by delta lines, clamped to content bounds.
// termH is the terminal height; the content page is derived from it so
// scroll and View always agree on the window size.
func (o *blockOverlay) scroll(delta, termH int) {
	pageH := overlayPageH(termH)
	o.yOffset += delta
	max := len(o.lines) - pageH
	if max < 0 {
		max = 0
	}
	if o.yOffset > max {
		o.yOffset = max
	}
	if o.yOffset < 0 {
		o.yOffset = 0
	}
}

// View renders the overlay frame at the given terminal size.
func (o *blockOverlay) View(w, h int) string {
	if w < 20 {
		w = 20
	}
	if h < 6 {
		h = 6
	}
	inner := w - 4
	pageH := overlayPageH(h)
	border := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	var b strings.Builder
	titleLine := truncateToWidth(o.title, inner)
	b.WriteString(border.Render("┌─ ") + lipgloss.NewStyle().Bold(true).Render(titleLine))
	pad := w - 4 - lipgloss.Width(titleLine)
	if pad > 0 {
		b.WriteString(border.Render(" " + strings.Repeat("─", pad-1) + "┐"))
	} else {
		b.WriteString(border.Render("┐"))
	}
	b.WriteByte('\n')

	end := o.yOffset + pageH
	if end > len(o.lines) {
		end = len(o.lines)
	}
	start := o.yOffset
	if start > end {
		start = end
	}
	for i := start; i < end; i++ {
		line := truncateToWidth(o.lines[i], inner)
		b.WriteString(border.Render("│ ") + line)
		if fill := inner - lipgloss.Width(line); fill > 0 {
			b.WriteString(strings.Repeat(" ", fill))
		}
		b.WriteString(border.Render(" │") + "\n")
	}
	for i := end - start; i < pageH; i++ {
		b.WriteString(border.Render("│ ") + strings.Repeat(" ", inner) + border.Render(" │") + "\n")
	}

	pos := "all"
	if len(o.lines) > pageH {
		pct := 100 * (o.yOffset + pageH) / len(o.lines)
		if pct > 100 {
			pct = 100
		}
		pos = fmt.Sprintf("%d%%", pct)
	}
	footer := fmt.Sprintf(" %s · %d lines · j/k scroll · pgup/pgdn · esc close ", pos, len(o.lines))
	footer = truncateToWidth(footer, w-2)
	b.WriteString(border.Render("└" + footer + strings.Repeat("─", max(0, w-2-lipgloss.Width(footer))) + "┘"))
	return b.String()
}

// handleOverlayKey routes keys while the overlay is open. Every key is
// consumed — the overlay owns the screen until dismissed.
func (m *tuiModel) handleOverlayKey(key string) (bool, bool, []tea.Cmd) {
	termH := max(6, m.height)
	pageH := overlayPageH(termH)
	switch key {
	case "esc", "q":
		m.overlay = nil
	case "j", "down":
		m.overlay.scroll(1, termH)
	case "k", "up":
		m.overlay.scroll(-1, termH)
	case "pgdown", " ", "f":
		m.overlay.scroll(pageH, termH)
	case "pgup", "b":
		m.overlay.scroll(-pageH, termH)
	case "home", "g":
		m.overlay.scroll(-1<<30, termH)
	case "end", "G":
		m.overlay.scroll(1<<30, termH)
	}
	return true, true, nil
}

// openSelectedBlockOverlay opens the detail overlay for the selected block.
func (m *tuiModel) openSelectedBlockOverlay() bool {
	if m.selectedBlockID == "" {
		return false
	}
	for i := range m.blocks {
		if m.blocks[i].ID == m.selectedBlockID {
			m.overlay = newBlockOverlay(m.blocks[i])
			return true
		}
	}
	return false
}
