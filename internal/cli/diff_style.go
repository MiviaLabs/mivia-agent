package cli

import (
	"fmt"
	"strings"
)

// Diff-highlight SGR (highlight only; not duplicated elsewhere). Relocated
// from theme.go: this file was their sole caller.
const (
	ansiBgDiffDel = "\033[48;5;88m" // dark red background for deletions
	ansiBgDiffAdd = "\033[48;5;22m" // dark green background for additions
)

// renderDiffLine renders a single unified-diff line using theme tokens.
// Classification by leading marker (+++/---/@@/+/-/context) is centralized here;
// every diff surface in the package must route through this function.
//
// The + and - prefixes are preserved verbatim in the output (existing gutter tests
// assert their presence). Leading indentation ("  ") matches the highlight/markdown
// surfaces. @@ hunk headers use the magenta/hunk token (not dim/context).
func renderDiffLine(line string) string {
	switch {
	case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
		// File header: bold cyan on dark background.
		return fmt.Sprintf("  %s%s%s%s", AnsiBgDark, AnsiBold, AnsiCyan, line)
	case strings.HasPrefix(line, "@@"):
		// Hunk header: magenta on dark background (unified decision: not dim).
		return fmt.Sprintf("  %s%s%s", AnsiBgDark, AnsiMagenta, line)
	case strings.HasPrefix(line, "+"):
		// Added line: green text on dark green background. Keep + prefix.
		return fmt.Sprintf("  %s%s%s", ansiBgDiffAdd, AnsiGreen, line)
	case strings.HasPrefix(line, "-"):
		// Removed line: red text on dark red background. Keep - prefix.
		return fmt.Sprintf("  %s%s%s", ansiBgDiffDel, AnsiRed, line)
	default:
		// Context line: dim text on dark background.
		return fmt.Sprintf("  %s%s%s", AnsiBgDark, AnsiDim, line)
	}
}
