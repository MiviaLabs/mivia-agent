package cli

import (
	"regexp"
	"strings"
)

var gfmSepCell = regexp.MustCompile(`^:?-{3,}:?$`)

func joinNonEmpty(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "\n" + b
	}
}

// isTableLine reports whether a line can be part of a GFM table.
//
// Outer pipes are optional in GFM ("a | b" is a valid row) and models emit
// that form constantly; requiring them made those tables fall through as raw
// text. A bare pipe line is only *candidate* material - a table is committed
// only when a separator row follows (see MarkdownWriter.tableBuf handling),
// so prose containing a pipe still renders as prose.
func isTableLine(trimmed string) bool {
	if len(trimmed) < 2 {
		return false
	}
	if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") {
		return true
	}
	return strings.Contains(trimmed, "|")
}

// isSeparatorLine reports whether a line is a GFM alignment row.
func isSeparatorLine(trimmed string) bool {
	if !strings.Contains(trimmed, "-") {
		return false
	}
	return isGFMSeparator(splitTableRow(trimmed))
}

func isGFMSeparator(cells []string) bool {
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		if !gfmSepCell.MatchString(strings.TrimSpace(c)) {
			return false
		}
	}
	return true
}

type tableAlign int

const (
	alignLeft tableAlign = iota
	alignCenter
	alignRight
)

func parseTableAlign(cell string) tableAlign {
	c := strings.TrimSpace(cell)
	left := strings.HasPrefix(c, ":")
	right := strings.HasSuffix(c, ":")
	switch {
	case left && right:
		return alignCenter
	case right:
		return alignRight
	default:
		return alignLeft
	}
}

func splitTableRow(line string) []string {
	s := strings.TrimSpace(line)
	if strings.HasPrefix(s, "|") {
		s = s[1:]
	}
	if strings.HasSuffix(s, "|") {
		s = s[:len(s)-1]
	}
	raw := strings.Split(s, "|")
	cells := make([]string, len(raw))
	for i, c := range raw {
		cells[i] = strings.TrimSpace(c)
	}
	return cells
}
