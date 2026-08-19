package render

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// Text renders assistant text with a minimal markdown treatment: fenced
// code blocks (```...```) and CommonMark-style indented code blocks
// (4+ leading spaces or a leading tab) get a tinted background;
// everything else is passed through verbatim.
//
// Full markdown/syntax highlighting (headings, emphasis, tables, inline
// code) is intentionally out of scope for this slice: pulling in a
// markdown engine has no driver beyond "would be nice" against today's
// fixture-only content. The trigger to revisit: wiring the real chat
// harness, when actual model output starts exercising richer markdown.
func Text(t theme.Theme, tier theme.Tier, s string) string {
	fence := WithBg(Role(t, tier, theme.RoleFGMuted), t, tier, theme.RoleBGInset)
	lines := strings.Split(s, "\n")
	inFence := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			lines[i] = fence.Render(line)
			continue
		}
		if inFence || isIndentedCode(line) {
			lines[i] = fence.Render(line)
		}
	}
	return strings.Join(lines, "\n")
}

// isIndentedCode reports whether line is a CommonMark-style indented
// code line: 4+ leading spaces or a leading tab. This is a heuristic,
// not a parser - a coincidentally-indented paragraph would also match,
// the same false-positive CommonMark itself accepts (it's why fenced
// blocks exist). An empty line is never flagged on its own; a blank
// line inside an indented block simply renders untinted.
func isIndentedCode(line string) bool {
	if strings.HasPrefix(line, "\t") {
		return true
	}
	trimmed := strings.TrimLeft(line, " ")
	return trimmed != "" && len(line)-len(trimmed) >= 4
}
