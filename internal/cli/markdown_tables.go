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

func isTableLine(trimmed string) bool {
	return len(trimmed) >= 2 && strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|")
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
