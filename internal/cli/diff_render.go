package cli

import (
	"fmt"
	"strings"
)

func renderDiffBody(body string, width, maxLines int) []string {
	if maxLines < 1 {
		return nil
	}
	lines := strings.Split(redactPreview(body), "\n")
	if len(lines) > maxLines {
		lines = changeCentricWindow(lines, maxLines)
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "@@"):
			out = append(out, clipPreviewLine(colorDiffLine(line), width))
		case strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-"):
			out = append(out, clipPreviewLine(colorDiffLine(line), width))
		case strings.HasPrefix(line, " "):
			out = append(out, clipPreviewLine(toolDiffCtx.Render("  "+line), width))
		case strings.HasPrefix(line, "… "):
			out = append(out, toolDimStyle.Render("    "+line))
		default:
			out = append(out, clipPreviewLine(toolDiffCtx.Render("  "+line), width))
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
	end := start + maxLines
	if end > len(lines) {
		end = len(lines)
		start = end - maxLines
		if start < 0 {
			start = 0
		}
	}
	window := append([]string(nil), lines[start:end]...)
	if start > 0 {
		window = append([]string{fmt.Sprintf("… %d lines omitted", start)}, window...)
	}
	if end < len(lines) {
		window = append(window, fmt.Sprintf("… %d lines omitted", len(lines)-end))
	}
	if len(window) > maxLines {
		window = append(window[:maxLines-1], fmt.Sprintf("… %d lines omitted", len(lines)-maxLines+1))
	}
	return window
}

// renderCollapsedEditBlock renders a file edit in history: a summary row
// (file, ± stat, duration, status) followed by a short peek of the diff.
// The full diff is one keypress away in the detail overlay.
func renderCollapsedEditBlock(block ChatBlock, text, agentPart string, width int) []string {
	const peekLines = 6
	path := parseToolPath("", text)
	if path == "" {
		path = parseToolPath(block.Text, "")
	}
	added, removed := diffStat(text)

	head := "  ▸ " + toolIconForName(block.ToolName) + " " + agentPart +
		toolNameStyle.Render(block.ToolName)
	if path != "" {
		head += " " + toolPathStyle.Render(" "+path+" ")
	}
	if added > 0 || removed > 0 {
		head += " " + toolDiffAdd.Render(fmt.Sprintf("+%d", added)) +
			" " + toolDiffDel.Render(fmt.Sprintf("−%d", removed))
	}
	if block.Elapsed > 0 {
		head += " " + toolTimeStyle.Render(formatDuration(block.Elapsed))
	}
	if block.Failed {
		head += " " + toolErrStyle.Render(glyphCross)
	} else if block.Elapsed > 0 {
		head += " " + toolOkStyle.Render(glyphCheck)
	}

	out := []string{head}
	// The peek budget goes to the change itself: the summary line and the
	// ---/+++ file headers already appear in the row above, and spending
	// rows on them used to push every real +/- line out of view.
	for _, line := range renderDiffBody(stripDiffPreamble(text), max(20, width-4), peekLines) {
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
