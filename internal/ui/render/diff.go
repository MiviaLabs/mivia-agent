package render

import (
	"fmt"
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
	if len(d.Hunks) == 0 {
		return ""
	}
	var b strings.Builder
	for i, hunk := range d.Hunks {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(Role(t, tier, theme.RoleDiffHunk).Render(hunk.Header))
		for _, line := range hunk.Lines {
			b.WriteByte('\n')
			b.WriteString(diffLine(t, tier, line))
		}
	}
	return b.String()
}

// DiffPreview renders at most maxLines diff lines (hunk headers count)
// followed by a subtle "N more lines" note when the diff is longer. It
// is Diff with a cap, for surfaces that cannot grow with the diff -
// the approval prompt, which owns a fixed slice of the cockpit.
func DiffPreview(t theme.Theme, tier theme.Tier, d uievent.Diff, maxLines int) string {
	if maxLines <= 0 || len(d.Hunks) == 0 {
		return ""
	}
	var b strings.Builder
	shown := 0
	more := diffLineCount(d) - maxLines
	if more < 0 {
		more = 0
	}
outer:
	for i, hunk := range d.Hunks {
		if i > 0 && shown < maxLines {
			b.WriteByte('\n')
		}
		if shown < maxLines {
			b.WriteString(Role(t, tier, theme.RoleDiffHunk).Render(hunk.Header))
			shown++
		}
		for _, line := range hunk.Lines {
			if shown >= maxLines {
				break outer
			}
			b.WriteByte('\n')
			b.WriteString(diffLine(t, tier, line))
			shown++
		}
	}
	if more > 0 {
		b.WriteByte('\n')
		b.WriteString(Role(t, tier, theme.RoleFGSubtle).
			Render(fmt.Sprintf("%d more lines", more)))
	}
	return b.String()
}

// diffLineCount is every line Diff would render: one per hunk header
// plus one per diff line.
func diffLineCount(d uievent.Diff) int {
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
