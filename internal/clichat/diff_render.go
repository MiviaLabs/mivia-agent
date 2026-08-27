package clichat

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// Diff stat colors (foreground only - the ± counts on edit rows). Relocated
// from toolui.go: this file was their sole caller.
var (
	toolDiffAdd = lipgloss.NewStyle().Foreground(lipgloss.Color(ThemeColorDiffAdd))
	toolDiffDel = lipgloss.NewStyle().Foreground(lipgloss.Color(ThemeColorDiffDel))
	toolDiffCtx = lipgloss.NewStyle().Foreground(lipgloss.Color(ThemeColorDim)) // dim context
)

// RenderDiffBody renders a redacted, width-clamped diff body as display
// lines. Shared with internal/legacytui's tool panel.
func RenderDiffBody(body string, width, maxLines int) []string {
	if maxLines < 1 {
		return nil
	}
	lines := strings.Split(RedactPreview(body), "\n")
	// The body's final line terminator leaves a trailing empty split element
	// that is not a real diff line; drop it so truncation counts are honest
	// (a genuinely empty diff line always carries a +, -, or space prefix).
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > maxLines {
		lines = changeCentricWindow(lines, maxLines)
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "@@"):
			out = append(out, ClipPreviewLine(ColorDiffLine(line), width))
		case strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-"):
			out = append(out, ClipPreviewLine(ColorDiffLine(line), width))
		case strings.HasPrefix(line, " "):
			out = append(out, ClipPreviewLine(toolDiffCtx.Render("  "+line), width))
		case strings.HasPrefix(line, "… "):
			out = append(out, ToolDimStyle.Render("    "+line))
		default:
			out = append(out, ClipPreviewLine(toolDiffCtx.Render("  "+line), width))
		}
	}
	return out
}

func changeCentricWindow(lines []string, maxLines int) []string {
	first := -1
	for i, line := range lines {
		if (strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++")) || (strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---")) {
			first = i
			break
		}
	}
	if first < 0 {
		first = 0
	}
	start := first - 3
	if start < 0 {
		start = 0
	}
	for start > 0 && !strings.HasPrefix(lines[start], "@@") {
		start--
	}
	if start+maxLines > len(lines) {
		start = len(lines) - maxLines
		if start < 0 {
			start = 0
		}
	}

	leading, content := reserveContentBudget(start, maxLines, len(lines))
	if content > 0 && first >= start+content {
		// A tight budget can spend every content slot on leading context
		// before ever reaching the line that changed - a "change-centric"
		// window must never do that. Re-anchor start so the change is the
		// last content line shown, using whatever budget is left for
		// leading context; content lines fall as low as 0 the closer start
		// gets to first (see reserveContentBudget), so first is always
		// covered by the time start reaches it.
		for s := Max(0, first-maxLines+1); s <= first; s++ {
			l, c := reserveContentBudget(s, maxLines, len(lines))
			if c > 0 && first < s+c {
				start, leading, content = s, l, c
				break
			}
		}
	}

	window := append([]string(nil), lines[start:start+content]...)
	if leading {
		window = append([]string{fmt.Sprintf("… %d lines omitted", start)}, window...)
	}
	if start+content < len(lines) {
		omitted := len(lines) - (start + content)
		if !leading {
			omitted += start // the dropped leading marker's lines are omitted too
		}
		window = append(window, fmt.Sprintf("… %d lines omitted", omitted))
	}
	return window
}

// reserveContentBudget decides whether a window starting at start needs a
// leading omission marker, and returns the content-line budget left after
// reserving marker slots from maxLines - both markers' slots are taken out
// of the budget before slicing, so the final window never exceeds maxLines
// and the reported omission counts always cover the rest of the body
// exactly once. total is the full body's line count.
func reserveContentBudget(start, maxLines, total int) (leading bool, content int) {
	leading = start > 0
	content = maxLines
	if leading {
		content--
	}
	if start+content < total {
		content-- // lines follow the shown span; a trailing marker is needed
	}
	if content < 1 {
		// Markers would crowd out every content line: drop the leading marker
		// so the content plus a single trailing marker fit the budget.
		leading = false
		content = maxLines
		if start+content < total {
			content--
		}
		if content < 0 {
			content = 0
		}
		if start > 0 && start+content >= total {
			// The dropped leading marker's start lines are only accounted
			// for if the trailing marker actually fires - its "omitted +=
			// start" compensation (in the caller) is what covers them.
			// Without this, content reaching all the way to total leaves
			// no trailing marker and the start lines vanish uncounted.
			// Force room for the marker instead.
			content = total - start - 1
			if content < 0 {
				content = 0
			}
		}
	}
	if content > total-start {
		content = total - start
	}
	return leading, content
}

// renderCollapsedEditBlock renders a file edit in history: a summary row
// (file, ± stat, duration, status) followed by a short peek of the diff.
// The full diff is one keypress away in the detail overlay.
func renderCollapsedEditBlock(block ChatBlock, text, agentPart string, width int) []string {
	const peekLines = 6
	path := ParseToolPath("", text)
	if path == "" {
		path = ParseToolPath(block.Text, "")
	}
	added, removed := diffStat(text)

	head := "  ▸ " + ToolIconForName(block.ToolName) + " " + agentPart +
		ToolNameStyle.Render(block.ToolName)
	if path != "" {
		head += " " + ToolPathStyle.Render(" "+path+" ")
	}
	if added > 0 || removed > 0 {
		head += " " + toolDiffAdd.Render(fmt.Sprintf("+%d", added)) +
			" " + toolDiffDel.Render(fmt.Sprintf("−%d", removed))
	}
	if block.Elapsed > 0 {
		head += " " + ToolTimeStyle.Render(FormatDuration(block.Elapsed))
	}
	if block.Failed {
		head += " " + ToolErrStyle.Render(GlyphCross)
	} else if block.Elapsed > 0 {
		head += " " + ToolOkStyle.Render(GlyphCheck)
	}

	out := []string{head}
	// The peek budget goes to the change itself: the summary line and the
	// ---/+++ file headers already appear in the row above, and spending
	// rows on them used to push every real +/- line out of view.
	for _, line := range RenderDiffBody(stripDiffPreamble(text), Max(20, width-4), peekLines) {
		out = append(out, "  "+line)
	}
	return out
}

// stripDiffPreamble drops the tool's summary line and unified-diff file
// headers, keeping hunks and content lines.
func stripDiffPreamble(body string) string {
	lines := strings.Split(body, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "--- ") || strings.HasPrefix(t, "+++ ") {
			continue
		}
		if len(kept) == 0 && !strings.HasPrefix(t, "@@") &&
			!strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "-") {
			continue // leading summary/prose before the first hunk
		}
		kept = append(kept, line)
	}
	if len(kept) == 0 {
		return body
	}
	return strings.Join(kept, "\n")
}

// diffStat counts added/removed lines in a unified diff body.
func diffStat(body string) (added, removed int) {
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}
