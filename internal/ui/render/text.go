package render

import (
	"fmt"
	"strings"

	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

// ProseMeasure is the wrap width for prose at a given terminal width.
// Prose wraps to a measure, not to the terminal, because a full-width
// line on a wide terminal is hard to read (wireframes-panes.md 14).
func ProseMeasure(width int) int {
	if width >= uikitconfig.BreakpointWide {
		return uikitconfig.ProseMeasureWide
	}
	if width <= 0 {
		return uikitconfig.ProseMeasureNarrow
	}
	if width < uikitconfig.ProseMeasureNarrow {
		return width
	}
	return uikitconfig.ProseMeasureNarrow
}

// Wrap breaks text at the given measure on word boundaries, preserving
// existing newlines. A word longer than the measure is left whole rather
// than split: breaking an identifier or a URL mid-token hurts more than
// one long line does.
//
// Pure: input in, string slice out.
func Wrap(text string, measure int) []string {
	var out []string
	for _, para := range strings.Split(text, "\n") {
		if measure <= 0 || len(para) <= measure {
			out = append(out, para)
			continue
		}
		out = append(out, wrapLine(para, measure)...)
	}
	return out
}

func wrapLine(line string, measure int) []string {
	// Keep the leading indent: an indented code line stays indented on
	// every continuation row.
	indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{line}
	}

	var out []string
	cur := indent
	for _, w := range words {
		switch {
		case cur == indent:
			cur += w
		case len(cur)+1+len(w) <= measure:
			cur += " " + w
		default:
			out = append(out, cur)
			cur = indent + w
		}
	}
	return append(out, cur)
}

// ProgressBar draws the subagent progress bar from the section 3 glyph
// table: "#" for done, "." for remaining, with the percentage beside it.
// Both glyphs are ASCII, so the bar is identical at every colour tier.
func ProgressBar(width, step, total int) string {
	if total <= 0 || width <= 0 {
		return ""
	}
	if step < 0 {
		step = 0
	}
	if step > total {
		step = total
	}
	filled := width * step / total
	pct := 100 * step / total
	return fmt.Sprintf("[%s%s] %3d%%",
		strings.Repeat("#", filled),
		strings.Repeat(".", width-filled),
		pct)
}
