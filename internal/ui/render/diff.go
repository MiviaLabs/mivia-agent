package render

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// Diff renders a structured unified diff as styled text. Hunk headers use
// RoleDiffHunk; added/removed lines use the diff fg/bg role pairs;
// context lines use RoleFGMuted with no background.
func Diff(t theme.Theme, tier theme.Tier, d uievent.Diff) string {
	if len(d.Hunks) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(Role(t, tier, theme.RoleFGMuted).Render(fmt.Sprintf("%s  +%d -%d", d.Path, d.Added, d.Removed)))
	for _, hunk := range d.Hunks {
		b.WriteByte('\n')
		b.WriteString(Role(t, tier, theme.RoleDiffHunk).Render(hunk.Header))
		for _, line := range hunk.Lines {
			b.WriteByte('\n')
			b.WriteString(diffLine(t, tier, line))
		}
	}
	return b.String()
}

func diffLine(t theme.Theme, tier theme.Tier, l uievent.DiffLine) string {
	switch l.Kind {
	case uievent.DiffLineAdd:
		st := WithBg(Role(t, tier, theme.RoleDiffAddFG), t, tier, theme.RoleDiffAddBG)
		return st.Render("+ " + l.Text)
	case uievent.DiffLineDel:
		st := WithBg(Role(t, tier, theme.RoleDiffDelFG), t, tier, theme.RoleDiffDelBG)
		return st.Render("- " + l.Text)
	default:
		return Role(t, tier, theme.RoleFGMuted).Render("  " + l.Text)
	}
}
