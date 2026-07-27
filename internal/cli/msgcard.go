package cli

import (
	"strings"
	"time"
)

// formatUserMessageCard renders a user message without box borders.
// Keeps the user-card background on every line. Label is the local send time
// (SentAt); when zero, only the body is shown with background.
// Layout:
//
//	[bg] 15:04:05  first line of text…
//	[bg]            wrapped continuation…
func formatUserMessageCard(text string, width int, sentAt time.Time) []string {
	if width < 16 {
		width = 16
	}
	label := ""
	if !sentAt.IsZero() {
		label = sentAt.In(time.Local).Format("15:04:05")
	}

	// Content width: full width minus a small left pad (2).
	const leftPad = 2
	inner := width - leftPad
	if inner < 8 {
		inner = 8
		width = inner + leftPad
	}

	body := strings.TrimSpace(text)
	if body == "" {
		body = " "
	}

	// First line: optional "HH:MM:SS  " + text; remaining lines indented under text.
	prefix := ""
	indent := ""
	if label != "" {
		prefix = label + "  "
		indent = strings.Repeat(" ", visibleWidth(prefix))
	}
	firstBudget := inner - visibleWidth(prefix)
	if firstBudget < 4 {
		// Narrow width: put label on its own line, body below.
		return formatUserMessageCardStacked(body, label, width, leftPad, inner)
	}

	// Wrap full body against first line budget, then re-prefix.
	// Simple approach: wrap body to firstBudget for all lines, then add prefix
	// only on first and indent on rest (may re-wrap long first line).
	wrapped := wrapANSIv2(body, firstBudget)
	if wrapped == "" {
		wrapped = " "
	}
	parts := strings.Split(wrapped, "\n")
	out := make([]string, 0, len(parts))
	for i, line := range parts {
		if visibleWidth(line) > firstBudget {
			line = hardTruncateANSI(line, firstBudget)
		}
		padN := firstBudget - visibleWidth(line)
		if padN < 0 {
			padN = 0
		}
		var row string
		if i == 0 {
			row = strings.Repeat(" ", leftPad) +
				tuiUserLabel.Render(prefix) +
				line + strings.Repeat(" ", padN)
		} else {
			row = strings.Repeat(" ", leftPad) +
				indent +
				line + strings.Repeat(" ", padN)
		}
		// Pad to full outer width so background is a solid bar.
		vis := visibleWidth(row)
		if vis < width {
			row += strings.Repeat(" ", width-vis)
		}
		out = append(out, tuiUserCardBg.Render(row))
	}
	return out
}

func formatUserMessageCardStacked(body, label string, width, leftPad, inner int) []string {
	out := make([]string, 0, 4)
	if label != "" {
		lab := strings.Repeat(" ", leftPad) + tuiUserLabel.Render(label)
		if vis := visibleWidth(lab); vis < width {
			lab += strings.Repeat(" ", width-vis)
		}
		out = append(out, tuiUserCardBg.Render(lab))
	}
	wrapped := wrapANSIv2(body, inner)
	if wrapped == "" {
		wrapped = " "
	}
	for _, line := range strings.Split(wrapped, "\n") {
		if visibleWidth(line) > inner {
			line = hardTruncateANSI(line, inner)
		}
		row := strings.Repeat(" ", leftPad) + line
		if vis := visibleWidth(row); vis < width {
			row += strings.Repeat(" ", width-vis)
		}
		out = append(out, tuiUserCardBg.Render(row))
	}
	return out
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
