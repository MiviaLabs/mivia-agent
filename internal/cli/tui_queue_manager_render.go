package cli

import (
	"fmt"
	"strings"
)

// renderQueuePanel renders the pending-message queue manager: a windowed
// selection popup of the queued items in send order, headed by the item that
// will auto-send next. It is a modal surface with popup placement (above the
// composer, like suggest/history), so the live turn stays visible.
func (m *tuiModel) renderQueuePanel(termW, maxH int) (string, Rect) {
	if !m.queueMgr.open || m.queueCount() == 0 || termW <= 0 || maxH < 1 {
		return "", Rect{}
	}
	rows := make([]string, 0, m.queueCount())
	for i := 0; i < m.queueCount(); i++ {
		item := m.queueItemAt(i)
		glyph := "•"
		if item.skill != nil {
			glyph = glyphLozenge
		}
		text := item.display
		if text == "" {
			text = item.sent
		}
		text = SafeChatBlockText(text, 0) // strips CSI/OSC escapes and NUL
		text = strings.ReplaceAll(text, "\n", "⏎")
		row := glyph + " " + text
		if i == 0 {
			// The head item is what empty-enter / turn-end auto-drain sends.
			row = TUIDimStyle.Render("next ") + row
		}
		rows = append(rows, row)
	}
	title := fmt.Sprintf(" queue (%d) ", m.queueCount())
	footer := "↑↓ select · enter send now · d delete · e edit · esc close "
	if sel := m.queueItemAt(m.queueMgr.selected); sel.skill != nil {
		// Skill bodies are workspace-controlled and hidden by design; they
		// can be steered or deleted but never edited as text. Keep the footer
		// short enough to survive the frame's fitDialogRow truncation.
		footer = "↑↓ select · enter send now · d delete · esc close (skills: no edit) "
	}
	// Wider than the 72-col suggest cap: queued messages need room.
	width := max(24, min(termW-4, 90))
	return renderOverlayWindow(rows, m.queueMgr.selected, 8, termW, maxH, title, footer, width)
}

// queueManagerHint is the hint-line segment shown while the queue manager is
// open; empty when closed. The manager is a modal that consumes every key, so
// this hint beats the waiting hint, which would advertise keys the modal makes
// impossible.
func (m *tuiModel) queueManagerHint() string {
	if !m.queueMgr.open {
		return ""
	}
	return " ↑↓ select · enter send now · d delete · e edit · esc close "
}

// queueCountHint is the hint-line segment advertising the queued-message
// affordances; empty when the queue is empty.
func (m *tuiModel) queueCountHint() string {
	if len(m.pendingQueue) == 0 {
		return ""
	}
	if m.queueMgr.open {
		return fmt.Sprintf("· %d queued ", len(m.pendingQueue))
	}
	return fmt.Sprintf("· %d queued · ctrl+up manage · empty enter=send next ", len(m.pendingQueue))
}
