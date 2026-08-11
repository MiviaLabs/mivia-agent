package cli

import (
	"fmt"
	"strings"
)

// renderHistoryPanel renders the composer message-history picker: a 3-row
// windowed popup of the user's sent messages, newest first.
func renderHistoryPanel(state historyState, entries []string, termW, maxH int) (string, rect) {
	if !state.open || len(entries) == 0 || termW <= 0 || maxH < 1 {
		return "", rect{}
	}
	rows := make([]string, 0, len(entries))
	for _, entry := range entries {
		safe := SafeChatBlockText(entry, 0) // strips CSI/OSC escapes and NUL
		safe = strings.ReplaceAll(safe, "\n", "⏎")
		rows = append(rows, safe)
	}
	return renderOverlayWindow(rows, state.selected, 3, termW, maxH, " history "+fmt.Sprintf("(%d)", len(entries)), "↑↓ select · enter recall · esc dismiss")
}
