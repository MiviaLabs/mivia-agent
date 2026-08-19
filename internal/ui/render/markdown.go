package render

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// Text renders assistant text with a minimal markdown treatment: fenced
// code blocks (```...```) get a tinted background; everything else is
// passed through verbatim.
//
// Full markdown/syntax highlighting (headings, emphasis, tables, inline
// code) is intentionally out of scope for this slice: there is no real
// assistant response to render it against yet (that lands with the real
// harness adapter), and pulling in a markdown engine now has no driver
// beyond "would be nice". The trigger to revisit: wiring the real chat
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
		if inFence {
			lines[i] = fence.Render(line)
		}
	}
	return strings.Join(lines, "\n")
}
