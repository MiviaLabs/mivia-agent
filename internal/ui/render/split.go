package render

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// SplitLeftShare is the width share the navigation pane takes in a
// two-pane split, in percent. 30 keeps a path list readable and leaves
// the content pane the reading measure (gh-dash's list/detail ratio is
// the same shape).
const SplitLeftShare = 30

// SplitLeftMax caps the navigation pane's absolute width: a share of a
// very wide terminal is still too much list (30% of 300 columns is 90
// columns of paths). Past the cap the content pane takes everything
// extra.
const SplitLeftMax = 60

// Side names the focused pane of a split.
type Side int

// Left and Right are the two panes of a split.
const (
	Left Side = iota
	Right
)

// Split composes two bordered panes side by side: the left pane at
// SplitLeftShare of width, the right at the rest. The focused pane
// takes RoleBorderFocus, the unfocused RoleBorder - the same convention
// as the approval prompt. Both boxes draw exactly height rows: content
// shorter than the pane is padded, and a pane with more content than
// height must be windowed by its caller first (Split never scrolls -
// scrolling belongs to the pane's owner, so there stays one
// implementation of it).
//
// This is the codebase's only side-by-side composition; it exists so
// the ratio, the height contract, and the focus-border convention live
// in one place when more panes arrive.
func Split(t theme.Theme, tier theme.Tier, width, height int, focus Side, left, right string) string {
	leftW := width * SplitLeftShare / 100
	if leftW > SplitLeftMax {
		leftW = SplitLeftMax
	}
	rightW := width - leftW
	if leftW < 8 || rightW < 8 {
		// Too narrow to frame two panes: callers collapse to one pane
		// below BreakpointWide; this guard only keeps a degenerate
		// width from rendering broken boxes.
		leftW, rightW = width/2, width/2
	}
	return lipgloss.JoinHorizontal(lipgloss.Top,
		paneBox(t, tier, focus == Left, leftW, height, left),
		paneBox(t, tier, focus == Right, rightW, height, right),
	)
}

// paneBox frames one pane. Content rows beyond height are dropped, not
// scrolled: the caller windows long content.
func paneBox(t theme.Theme, tier theme.Tier, focused bool, inner, height int, content string) string {
	role := theme.RoleBorder
	if focused {
		role = theme.RoleBorderFocus
	}
	st := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Width(inner).
		Height(height)
	if s := t.Resolve(role, tier); s.Hex != "" {
		st = st.BorderForeground(lipgloss.Color(s.Hex))
	} else if s.ANSI16 >= 0 {
		st = st.BorderForeground(lipgloss.Color(strconv.Itoa(s.ANSI16)))
	}
	rows := strings.Split(content, "\n")
	if len(rows) > height {
		content = strings.Join(rows[:height], "\n")
	}
	return st.Render(content)
}
