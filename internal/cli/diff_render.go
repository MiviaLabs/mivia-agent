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
