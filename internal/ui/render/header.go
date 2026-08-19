package render

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

// HeaderSpec is the content of a block header row, in four columns.
// wireframes-panes.md section 2: label at column 1 (column 3 when a
// collapse marker is present), the detail after it, then a right-aligned
// meta and a right-aligned state.
type HeaderSpec struct {
	// Marker is the collapse glyph, "v", ">" or " ".
	Marker string
	Label  string
	Detail string
	Meta   string
	State  string
	// StateRole colours the state word. The word is always rendered, so
	// meaning never depends on colour alone.
	StateRole theme.Role
}

// Header renders a block header, right-aligning meta and state against
// width. When the columns cannot all fit, the DETAIL is clipped and
// marked - never the state, which carries meaning, and never the label,
// which identifies the block.
//
// Pure: input in, string out, no I/O and no package state.
func Header(t theme.Theme, tier theme.Tier, width int, spec HeaderSpec) string {
	left := spec.Marker + " " + spec.Label
	if spec.Detail != "" {
		left += " " + spec.Detail
	}
	right := spec.Meta
	if spec.State != "" {
		if right != "" {
			right += "  "
		}
		right += spec.State
	}

	// A width of zero means "unknown": fall back to single spaces rather
	// than inventing a column layout for a terminal we have not measured.
	if width <= 0 {
		return styleHeader(t, tier, spec, left, right, "  ")
	}

	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < minHeaderGap {
		clipped, ok := clipLeft(spec, left, width-lipgloss.Width(right)-minHeaderGap)
		if ok {
			left = clipped
			gap = width - lipgloss.Width(left) - lipgloss.Width(right)
		}
	}
	if gap < minHeaderGap {
		gap = minHeaderGap
	}
	return styleHeader(t, tier, spec, left, right, strings.Repeat(" ", gap))
}

// minHeaderGap keeps at least this much space between the detail and the
// right-aligned columns, so they never read as one run of text.
const minHeaderGap = 2

// clipLeft shortens the detail so the header fits, marking the cut. It
// reports false when there is no detail to give up.
func clipLeft(spec HeaderSpec, left string, budget int) (string, bool) {
	if spec.Detail == "" || budget <= 0 {
		return left, false
	}
	prefix := spec.Marker + " " + spec.Label + " "
	room := budget - lipgloss.Width(prefix) - lipgloss.Width(uikitconfig.ClipMarker)
	if room < 1 {
		return left, false
	}
	detail := spec.Detail
	if lipgloss.Width(detail) <= room {
		return left, false
	}
	return prefix + truncate(detail, room) + uikitconfig.ClipMarker, true
}

// truncate cuts s to n display columns, counting runes so multi-byte
// text is not split mid-character.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func styleHeader(t theme.Theme, tier theme.Tier, spec HeaderSpec, left, right, gap string) string {
	// The marker and label are dim; the detail is normal weight; meta is
	// subtle; the state carries its own role.
	out := Role(t, tier, theme.RoleFGMuted).Render(spec.Marker + " " + spec.Label)
	if rest := strings.TrimPrefix(left, spec.Marker+" "+spec.Label); rest != "" {
		out += Role(t, tier, theme.RoleFG).Render(rest)
	}
	if right == "" {
		return out
	}
	out += gap
	if spec.Meta != "" {
		out += Role(t, tier, theme.RoleFGSubtle).Render(spec.Meta)
		if spec.State != "" {
			out += "  "
		}
	}
	if spec.State != "" {
		role := spec.StateRole
		if role == "" {
			role = theme.RoleFGMuted
		}
		out += Role(t, tier, role).Render(spec.State)
	}
	return out
}
