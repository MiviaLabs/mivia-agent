package render

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// SplitNavShare is the width share the navigation pane (a list the user
// picks from) takes in a two-pane split, in percent. 30 keeps a path
// list readable and leaves the reading pane the reading measure
// (gh-dash's list/detail ratio is the same shape).
const SplitNavShare = 30

// SplitNavMax caps the navigation pane's absolute width: a share of a
// very wide terminal is still too much list (30% of 300 columns is 90
// columns of paths). Past the cap the reading pane takes everything
// extra.
const SplitNavMax = 60

// Side names the focused pane of a split.
type Side int

// Left and Right are the two panes of a split.
const (
	Left Side = iota
	Right
)

// SplitWidths is the pane geometry Split assigns: nav is the right
// pane's total width (border included) at SplitNavShare of width capped
// at SplitNavMax, reading is the left pane's. Exported so a caller that
// renders content INTO a pane sizes that content to the same numbers
// the frame draws around it - the pane wraps content wider than
// width-minus-borders, it does not truncate it.
func SplitWidths(width int) (reading, nav int) {
	nav = width * SplitNavShare / 100
	if nav > SplitNavMax {
		nav = SplitNavMax
	}
	reading = width - nav
	if reading < 8 || nav < 8 {
		// Too narrow to frame two panes: callers collapse to one pane
		// below BreakpointWide; this guard only keeps a degenerate
		// width from rendering broken boxes.
		reading, nav = width/2, width/2
	}
	return reading, nav
}

// Split composes two bordered panes side by side: the right navigation
// pane at SplitNavShare of width, the left reading pane at the rest. The
// focused pane takes RoleBorderFocus, the unfocused RoleBorder - the
// same convention as the approval prompt. Both boxes draw exactly height
// rows: content shorter than the pane is padded, and a pane with more
// content than height must be windowed by its caller first (Split never
// scrolls - scrolling belongs to the pane's owner, so there stays one
// implementation of it).
//
// This is the codebase's only side-by-side composition; it exists so
// the ratio, the height contract, and the focus-border convention live
// in one place when more panes arrive.
func Split(t theme.Theme, tier theme.Tier, width, height int, focus Side, left, right string) string {
	leftW, rightW := SplitWidths(width)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		paneBox(t, tier, focus == Left, leftW, height, left),
		paneBox(t, tier, focus == Right, rightW, height, right),
	)
}

// SplitDialog is the split with its reading pane replaced by a centered
// dialog: the dialog is sized to the reading pane's whole area, so the
// two blocks compose with no gap, and the navigation pane stays visible
// and legible beside it instead of hiding behind a full-surface dialog.
// The dialog is the focus surface here, so the nav pane draws the
// unfocused border.
func SplitDialog(t theme.Theme, tier theme.Tier, width, height int, title, body, hint, nav string) string {
	leftW, rightW := SplitWidths(width)
	dlg := Dialog(t, tier, leftW, height, title, body, hint)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		dlg,
		paneBox(t, tier, false, rightW, height, nav),
	)
}

// paneBox frames one pane. Content rows beyond height are dropped, not
// scrolled: the caller windows long content. A content row wider than
// the pane's inner width wraps, so callers clip wide rows themselves.
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
