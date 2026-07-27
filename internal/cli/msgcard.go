package cli

import (
	"time"
)

// formatUserMessageCard renders a user message without box borders.
// Delegates to UserBubble so padding (top/right/bottom/left) and background
// fill are the single production path for TUI history + classic history.
//
// Layout (default UserBubble: dark-gray bg, no left rail, no vertical pad):
//
//	[bg]  15:04:05               ← time on first line
//	[bg]  first line of message…
//	[bg]  continuation…
//	                             ← empty lane after bubble (appendRenderedBlockMem)
func formatUserMessageCard(text string, width int, sentAt time.Time) []string {
	return UserBubble.Render(text, width, sentAt)
}

// formatModelHeader is kept for API compatibility; model messages no longer
// use bordered chrome. Returns empty so callers can append unconditionally.
func formatModelHeader(modelName string, width int) string {
	_ = modelName
	_ = width
	return ""
}

// formatModelFooter is kept for API compatibility; no border footer.
func formatModelFooter(width int) string {
	_ = width
	return ""
}
