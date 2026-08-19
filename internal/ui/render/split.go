package render

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"

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

// Side names the pane of a split that holds keyboard focus.
type Side int

// Left and Right are the two panes of a split.
const (
	Left Side = iota
	Right
)

// SplitWidths is the pane geometry Split assigns: nav is the right
// pane's width including its divider column, at SplitNavShare of width
// capped at SplitNavMax; reading is the left pane's. Exported so a
// caller that renders content INTO a pane sizes that content to the
// same numbers the frame draws around it - blocks pad and clip, they
// never re-wrap, so wide rows are the caller's to cut.
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

// Split composes the reading column and the nav sidebar side by side,
// separated by ONE vertical rule on the sidebar's left edge - the only
// frame the split draws. The reading column keeps the cockpit's open
// look (a full box around the conversation reads as a second terminal
// inside the terminal), and the rule is what says "pane boundary".
//
// The focused pane names the rule's colour, carrying the same
// RoleBorderFocus/RoleBorder convention the approval prompt uses. Both
// columns draw exactly height rows: content is padded, and surplus rows
// clip from the bottom (Split never scrolls - windowing belongs to the
// pane's owner, so there stays one implementation of it).
//
// This is the codebase's only side-by-side composition; it exists so
// the ratio, the height contract, and the focus-colour convention live
// in one place when more panes arrive.
func Split(t theme.Theme, tier theme.Tier, width, height int, focus Side, left, right string) string {
	reading, nav := SplitWidths(width)
	return joinWithRule(t, tier, focus == Right, height,
		clipBlock(left, reading, height), clipBlock(right, nav-1, height))
}

// SplitDialog is the split with its reading column replaced by a
// centered dialog: the dialog is sized to the reading column's whole
// area, so the blocks compose with no gap, and the nav pane stays
// visible and legible beside it instead of hiding behind a
// full-surface dialog. navFocused names the rule's colour, so a caller
// whose list keeps keyboard focus under the dialog can keep saying so.
func SplitDialog(t theme.Theme, tier theme.Tier, width, height int, navFocused bool, title, body, hint, nav string) string {
	reading, navW := SplitWidths(width)
	dlg := Dialog(t, tier, reading, height, title, body, hint)
	return joinWithRule(t, tier, navFocused, height, dlg, clipBlock(nav, navW-1, height))
}

// joinWithRule stacks block, one rule column, block.
func joinWithRule(t theme.Theme, tier theme.Tier, focus bool, height int, reading, nav string) string {
	return lipgloss.JoinHorizontal(lipgloss.Top, reading, verticalRule(t, tier, focus, height), nav)
}

// verticalRule is the sidebar's left edge: one column of rule glyphs,
// focus-coloured. It is the split's only frame.
func verticalRule(t theme.Theme, tier theme.Tier, focus bool, height int) string {
	role := theme.RoleBorder
	if focus {
		role = theme.RoleBorderFocus
	}
	glyph := strings.Repeat("│\n", height)
	return Role(t, tier, role).Render(strings.TrimSuffix(glyph, "\n"))
}

// clipBlock normalizes content into an exact width x height block:
// every row clipped (never wrapped) to width, surplus rows dropped, and
// short blocks padded with blank rows.
func clipBlock(content string, width, height int) string {
	rows := strings.Split(content, "\n")
	if len(rows) > height {
		rows = rows[:height]
	}
	for i, r := range rows {
		if w := ansi.StringWidth(r); w > width {
			rows[i] = ansi.Truncate(r, width, "")
		}
	}
	for len(rows) < height {
		rows = append(rows, "")
	}
	st := lipgloss.NewStyle().Width(width)
	for i, r := range rows {
		rows[i] = st.Render(r)
	}
	return strings.Join(rows, "\n")
}
