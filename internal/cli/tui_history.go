package cli

import (
	"strings"
)

// VisualLineCount returns how many viewport lines the given content slots occupy.
// Each string may itself contain newlines after markdown/wrap.
func VisualLineCount(lines []string) int {
	n := 0
	for _, line := range lines {
		n += strings.Count(line, "\n") + 1
	}
	return n
}
