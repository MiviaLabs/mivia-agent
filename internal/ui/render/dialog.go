package render

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"charm.land/lipgloss/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// Dialog margins: the box never runs closer than this to the terminal
// edge, so a centered dialog always reads as floating on the surface
// rather than filling it.
const (
	dialogMarginX = 4
	dialogMarginY = 2
)

// Dialog renders title, body, and hint inside a bordered, inset-filled
// box centered on a width x height terminal. It is the one centered
// dialog primitive: this renderer has no compositing layer, so the
// dialog is the whole frame and the padding around the box carries the
// base background - the conversation behind it is not shown.
//
// Content that does not fit is truncated, not scrolled: lines clip to
// the inner width and surplus body rows drop off the bottom. A
// scrolling body is deliberately out of scope for v1; every caller
// today (theme preview, model picker, help) fits or is better served by
// the pager for full-height reading.
//
// Degrade tiers follow the same ladder as WithBg and Bordered: at
// ASCII/NoTTY the inset and base backgrounds contribute nothing (a
// colour fill without colour is broken), and the border stays as plain
// glyphs - structure survives NO_COLOR.
func Dialog(t theme.Theme, tier theme.Tier, width, height int, title, body, hint string) string {
	content := dialogContent(t, tier, title, body, hint)

	// Size and clip the content first, then frame it: a line lipgloss
	// wraps would add rows the cap below cannot see. Unsized callers
	// (width or height 0, e.g. exact-string tests) get the full content
	// framed but neither clipped nor centered.
	rows := strings.Split(strings.Join(content, "\n"), "\n")
	if width > 0 && height > 0 {
		// Comfortable margins by default, but give them up before the
		// content: a small terminal keeps the box (and its body) and
		// loses the floating look, not the dialog.
		marginX, marginY := dialogMarginX, dialogMarginY
		for marginX > 0 && width-2*marginX-2-4 < 12 {
			marginX--
		}
		for marginY > 0 && height-2*marginY-2-2 < 5 {
			marginY--
		}
		inner := width - 2*marginX - 2 - 4
		maxRows := height - 2*marginY - 2 - 2
		rows = dialogClip(rows, inner, maxRows)
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(1, 2)
	if s := t.Resolve(theme.RoleBorderFocus, tier); s.Hex != "" {
		box = box.BorderForeground(lipgloss.Color(s.Hex))
	} else if s.ANSI16 >= 0 {
		box = box.BorderForeground(lipgloss.Color(strconv.Itoa(s.ANSI16)))
	}
	if s := t.Resolve(theme.RoleBGInset, tier); s.Hex != "" {
		box = box.Background(lipgloss.Color(s.Hex))
	} else if s.ANSI16 >= 0 {
		box = box.Background(lipgloss.Color(strconv.Itoa(s.ANSI16)))
	}
	framed := box.Render(strings.Join(rows, "\n"))

	if width <= 0 || height <= 0 {
		return framed
	}
	ws := lipgloss.NewStyle()
	opts := []lipgloss.WhitespaceOption{}
	if s := t.Resolve(theme.RoleBG, tier); s.Hex != "" {
		opts = append(opts, lipgloss.WithWhitespaceStyle(ws.Background(lipgloss.Color(s.Hex))))
	} else if s.ANSI16 >= 0 {
		opts = append(opts, lipgloss.WithWhitespaceStyle(ws.Background(lipgloss.Color(strconv.Itoa(s.ANSI16)))))
	} else {
		opts = append(opts, lipgloss.WithWhitespaceStyle(ws))
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, framed, opts...)
}

// dialogContent stacks the three parts with breathing room between
// them; empty parts drop out with their separator.
func dialogContent(t theme.Theme, tier theme.Tier, title, body, hint string) []string {
	var parts []string
	if title != "" {
		parts = append(parts, Role(t, tier, theme.RoleFG).Bold(true).Render(title))
	}
	if body != "" {
		parts = append(parts, body)
	}
	if hint != "" {
		parts = append(parts, Role(t, tier, theme.RoleFGSubtle).Render(hint))
	}
	return strings.Split(strings.Join(parts, "\n\n"), "\n")
}

// dialogClip truncates every row to inner columns and drops rows beyond
// maxRows from the bottom.
func dialogClip(rows []string, inner, maxRows int) []string {
	if maxRows < 1 {
		return []string{""}
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if ansi.StringWidth(r) > inner {
			r = ansi.Truncate(r, inner, "")
		}
		out = append(out, r)
	}
	if len(out) <= maxRows {
		return out
	}
	// Overflow drops rows off the bottom, hint included: the body is
	// the dialog, and a caller whose hint must survive sizes the box
	// for it. No truncation marker - the box is capped, not a pager.
	return out[:maxRows]
}
