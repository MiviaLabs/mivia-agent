package cli

import (
	"strings"
)

// formatUserMessageCard renders a user message as a bordered "you" card for
// history reload and live send. Each element is one display line (ANSI ok).
// width is the outer card width in terminal columns.
func formatUserMessageCard(text string, width int) []string {
	if width < 16 {
		width = 16
	}
	inner := width - 4 // "│ " + content + " │"
	if inner < 8 {
		inner = 8
		width = inner + 4
	}

	// Top: ╭─ you ────────╮
	// Fixed visible (without dashes): "╭─ " (3) + "you" (3) + " " (1) + "╮" (1) = 8
	dashN := width - 8
	if dashN < 1 {
		dashN = 1
		width = 9
		inner = width - 4
	}
	top := tuiDimStyle.Render("╭─ ") +
		tuiUserStyle.Render("you") +
		tuiDimStyle.Render(" "+strings.Repeat("─", dashN)+"╮")

	bot := tuiDimStyle.Render("╰" + strings.Repeat("─", width-2) + "╯")

	wrapped := wrapANSIv2(text, inner)
	if wrapped == "" {
		wrapped = " "
	}
	parts := strings.Split(wrapped, "\n")
	out := make([]string, 0, len(parts)+2)
	out = append(out, top)
	for _, line := range parts {
		// Hard-cap long tokens (URLs/CJK) so the card never exceeds outer width.
		if visibleWidth(line) > inner {
			line = hardTruncateANSI(line, inner)
		}
		pad := inner - visibleWidth(line)
		if pad < 0 {
			pad = 0
		}
		out = append(out, tuiDimStyle.Render("│ ")+line+strings.Repeat(" ", pad)+tuiDimStyle.Render(" │"))
	}
	out = append(out, bot)
	return out
}

// formatModelFooter renders a closing line for the model's chat card:
// ╰─────────────────────────╯
// The total visible width (including box characters) matches the header width.
func formatModelFooter(width int) string {
	if width < 16 {
		width = 16
	}
	return tuiHeaderStyle.Render("╰" + strings.Repeat("─", max(1, width-2)) + "╯")
}

// formatModelHeader renders a light open-ended model chrome line consistent
// with the user card family: ╭─ modelname ────
func formatModelHeader(modelName string, width int) string {
	if width < 16 {
		width = 16
	}
	name := modelName
	if name == "" {
		name = "model"
	}
	// "╭─ " (3) + name + " " (1) + dashes
	fixed := 3 + visibleWidth(name) + 1
	dashN := width - fixed
	if dashN < 1 {
		dashN = 1
	}
	return tuiHeaderStyle.Render("╭─ " + name + " " + strings.Repeat("─", dashN))
}
