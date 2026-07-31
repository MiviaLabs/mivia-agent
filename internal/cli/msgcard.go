package cli

import (
	"strings"
	"time"
)

// formatUserMessageCard renders a user message without box borders.
// Delegates to UserBubble so padding (top/right/bottom/left) and background
// fill are the single production path for TUI history + classic history.
//
// Layout (default UserBubble: dark-gray bg, no left rail, no vertical pad):
//
//	[bg]  first line of message…
//	[bg]  continuation…
//	[bg]            [ 10:30PM ]  ← dim trailing meta, no seconds
//	                             ← empty lane after bubble (appendRenderedBlockMem)
//
// formatUserMessageCard renders a user message as a compact rail block.
//
// It used to be a full-width background bar with the timestamp alone on a
// trailing line: a nine-character message painted a dark band across the
// whole terminal and burned an extra row on a clock. The rail version is
// content-sized, reads as a quotation of what you said, and puts the
// timestamp inline on the label row where it costs nothing.
func formatUserMessageCard(text string, width int, sentAt time.Time) []string {
	body := strings.TrimSpace(text)
	if body == "" {
		body = " "
	}
	rail := userRailStyle.Render("▌")
	label := userLabelStyle.Render("you")
	if !sentAt.IsZero() {
		label += tuiDimStyle.Render("  " + sentAt.In(time.Local).Format("3:04PM"))
	}
	out := []string{"  " + rail + " " + label}
	for _, line := range strings.Split(wrapLineV2(body, max(20, width-6)), "\n") {
		out = append(out, "  "+rail+" "+line)
	}
	return out
}

// userRailStyle / userLabelStyle aliases live in theme.go.
