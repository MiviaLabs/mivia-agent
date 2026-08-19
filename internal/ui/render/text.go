package render

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

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
// Every width here is a DISPLAY width, not a byte count. A byte count
// wraps accented prose at half the measure and CJK at a third of it.
func Wrap(text string, measure int) []string {
	var out []string
	for _, para := range strings.Split(text, "\n") {
		if measure <= 0 || ansi.StringWidth(para) <= measure {
			out = append(out, para)
			continue
		}
		out = append(out, wrapLine(para, measure)...)
	}
	return out
}

func wrapLine(line string, measure int) []string {
	// Keep the leading indent: an indented code line stays indented on
	// every continuation row. Tabs indent as often as spaces do.
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{line}
	}

	var out []string
	cur := indent
	for _, w := range words {
		// The candidate row is measured WHOLE, not as the sum of its
		// parts. Invalid UTF-8 - which tool output does contain - can
		// combine across a join, so width(a)+width(b) is not always
		// width(a+b), and summing produced rows over the measure.
		next := cur + " " + w
		switch {
		case cur == indent:
			cur += w
		case ansi.StringWidth(next) <= measure:
			cur = next
		default:
			out = append(out, cur)
			cur = indent + w
		}
	}
	return append(out, cur)
}

// HardWrap breaks one logical line into the terminal rows it occupies at
// width, cutting mid-token. Tool output and code are not prose: a break
// on a word boundary would misrepresent the bytes the tool produced.
//
// It is ANSI-aware, so a styled line keeps its escapes and they cost no
// display columns.
func HardWrap(line string, width int) []string {
	if width <= 0 || ansi.StringWidth(line) <= width {
		return []string{line}
	}
	return strings.Split(ansi.Hardwrap(line, width, false), "\n")
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
