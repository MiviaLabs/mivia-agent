package cli

import (
	"strings"
	"unicode/utf8"
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func truncateStr(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// visibleWidth returns the visible (display) width of a string, ignoring
// ANSI escape sequences (which are zero-width). Multi-byte CJK chars count
// as 2, everything else as 1.
func visibleWidth(s string) int {
	w := 0
	i := 0
	for i < len(s) {
		if s[i] == '\033' {
			// Skip ANSI escape sequence — zero-width.
			i++
			for i < len(s) && !((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z')) {
				i++
			}
			if i < len(s) {
				i++ // skip terminator
			}
			continue
		}
		// Count visual width — CJK and wide chars = 2, ASCII = 1.
		r, size := utf8.DecodeRuneInString(s[i:])
		if isWideRune(r) {
			w += 2
		} else {
			w++
		}
		i += size
	}
	return w
}

// stripAnsiOut removes ANSI escape sequences from a string.
func stripAnsiOut(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\033' {
			i++
			for i < len(s) && !((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z')) {
				i++
			}
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

// wrapANSIv2 wraps a string containing ANSI escape sequences to a maximum
// visible width. ANSI sequences are zero-width and preserved in the output.
// It breaks lines at word boundaries (spaces). If no space is found within
// maxWidth, the line is output as-is (no hard break of words).
func wrapANSIv2(s string, maxWidth int) string {
	if maxWidth < 5 {
		maxWidth = 5
	}
	var out strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(wrapLineV2(line, maxWidth))
	}
	return out.String()
}

// isRenderedTableRow reports whether a line is a markdown-rendered table row
// (spaces + box-drawing │ borders). Those must not soft-wrap mid-row.
func isRenderedTableRow(line string) bool {
	plain := stripAnsiOut(line)
	if !strings.Contains(plain, "│") {
		return false
	}
	trimmed := strings.TrimLeft(plain, " \t")
	return strings.HasPrefix(trimmed, "│")
}

// hardTruncateANSI truncates a line to maxWidth visible columns, appends … if
// cut, and always ends with ansiReset so colors do not bleed.
func hardTruncateANSI(line string, maxWidth int) string {
	if maxWidth < 1 {
		return ansiReset
	}
	if visibleWidth(line) <= maxWidth {
		if strings.HasSuffix(line, ansiReset) {
			return line
		}
		return line + ansiReset
	}
	budget := maxWidth
	if budget > 1 {
		budget-- // reserve for …
	}
	var b strings.Builder
	w := 0
	i := 0
	for i < len(line) {
		if line[i] == '\033' {
			start := i
			i++
			for i < len(line) && !((line[i] >= 'A' && line[i] <= 'Z') || (line[i] >= 'a' && line[i] <= 'z')) {
				i++
			}
			if i < len(line) {
				i++
			}
			b.WriteString(line[start:i])
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		rw := 1
		if isWideRune(r) {
			rw = 2
		}
		if w+rw > budget {
			break
		}
		b.WriteString(line[i : i+size])
		w += rw
		i += size
	}
	if maxWidth > 1 {
		b.WriteString("…")
	}
	b.WriteString(ansiReset)
	return b.String()
}

// wrapLineV2 wraps a single line (no embedded newlines) to maxWidth visible
// columns. ANSI sequences are zero-width. CJK chars are properly counted.
// Rendered table rows (│ borders) are never soft-wrapped; they hard-truncate.
// Returns the wrapped line.
func wrapLineV2(line string, maxWidth int) string {
	if len(line) == 0 {
		return ""
	}
	// Quick check: if visible width is within limit, return as-is.
	if visibleWidth(line) <= maxWidth {
		return line
	}

	// Table rows: keep one physical line — hard truncate with … if needed.
	if isRenderedTableRow(line) {
		return hardTruncateANSI(line, maxWidth)
	}

	// We need to wrap. Walk byte by byte, using visibleWidth for width.
	var out strings.Builder
	var currentLine strings.Builder
	lastSpaceByte := -1 // byte position of last space in currentLine

	flushLine := func() {
		prefix := currentLine.String()[:lastSpaceByte]
		out.WriteString(prefix)
		out.WriteByte('\n')
		remainder := currentLine.String()[lastSpaceByte+1:] // skip the space
		currentLine.Reset()
		currentLine.WriteString(remainder)
		lastSpaceByte = -1
	}

	i := 0
	for i < len(line) {
		// ANSI escape sequence: copy verbatim, zero-width.
		if line[i] == '\033' {
			start := i
			i++
			for i < len(line) && !((line[i] >= 'A' && line[i] <= 'Z') || (line[i] >= 'a' && line[i] <= 'z')) {
				i++
			}
			if i < len(line) {
				i++
			}
			currentLine.WriteString(line[start:i])
			continue
		}

		// Regular character byte.
		currentLine.WriteByte(line[i])

		// Track space positions for word wrap.
		if line[i] == ' ' || line[i] == '\t' {
			lastSpaceByte = currentLine.Len() - 1
		}

		// Check if we've exceeded maxWidth by measuring the full currentLine
		// (which accumulates characters and ANSI codes).
		if visibleWidth(currentLine.String()) > maxWidth && lastSpaceByte >= 0 {
			flushLine()
			i++
			continue
		}

		i++
	}

	// Write remaining content.
	out.WriteString(currentLine.String())
	return out.String()
}

// wrapANSI is the public wrapper. It uses wrapANSIv2 internally.
func wrapANSI(s string, maxWidth int) string {
	return wrapANSIv2(s, maxWidth)
}

// Markdown help content for /help in TUI.
