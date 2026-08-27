package render

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// Diff renders the hunks of a unified diff as styled text. Hunk headers
// use RoleDiffHunk; added/removed lines use the diff fg/bg role pairs;
// context lines use RoleFGMuted with no background.
//
// It renders NO summary line. The path and the +N -M counts belong to
// the enclosing block's header, in its detail and meta columns
// (wireframes-panes.md section 11), and rendering them here too printed
// them twice.
func Diff(t theme.Theme, tier theme.Tier, d uievent.Diff) string {
	return strings.Join(DiffLines(t, tier, d), "\n")
}

// DiffLines is Diff as one string per rendered line, so surfaces that
// window into a diff (the scrollable approval preview) can slice it
// without re-parsing styled text.
func DiffLines(t theme.Theme, tier theme.Tier, d uievent.Diff) []string {
	if len(d.Hunks) == 0 {
		return nil
	}
	var out []string
	for _, hunk := range d.Hunks {
		out = append(out, Role(t, tier, theme.RoleDiffHunk).Render(hunk.Header))
		for _, line := range hunk.Lines {
			out = append(out, diffLine(t, tier, line))
		}
	}
	return out
}

// DiffLineCount is how many lines Diff renders: one per hunk header
// plus one per diff line.
func DiffLineCount(d uievent.Diff) int {
	n := len(d.Hunks)
	for _, hunk := range d.Hunks {
		n += len(hunk.Lines)
	}
	return n
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
