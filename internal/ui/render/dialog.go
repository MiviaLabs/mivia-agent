package render

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"charm.land/lipgloss/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

// Dialog margins: the box never runs closer than this to the terminal
// edge, so a centered dialog always reads as floating on the surface
// rather than filling it.
const (
	dialogMarginX = 4
	dialogMarginY = 2
)

// DialogBodyRows is how many body rows a Dialog framed to height can
// show, after its margins, border, padding, title, separators, and hint.
// A caller that windows scrollable body content by rows must use this
// number: scrolling by a larger step leaves tail rows unreachable,
// because Dialog clips what does not fit.
func DialogBodyRows(height int) int {
	marginY := dialogMarginY
	for marginY > 0 && height-2*marginY-2-2 < 5 {
		marginY--
	}
	// When the clip cap bites, it drops the body's trailing blank
	// separator along with surplus rows, so the body's own share is the
	// cap less title, leading blank, and hint - three rows, not four.
	body := height - 2*marginY - 2 - 2 - 3
	if body < 1 {
		return 1
	}
	return body
}

// DialogBodyWidth is the inner width a Dialog framed to width gives its
// body rows, after the same margin shrinking the clip applies. A caller
// that renders its own surface INTO the body (an embedded chat, not a
// plain string) sizes it to this so the clip never has to cut it.
func DialogBodyWidth(width int) int {
	marginX := dialogMarginX
	for marginX > 0 && width-2*marginX-2-4 < 12 {
		marginX--
	}
	return width - 2*marginX - 2 - 4
}

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
	inner := 0
	if width > 0 && height > 0 {
		marginX := dialogMarginX
		for marginX > 0 && width-2*marginX-2-4 < 12 {
			marginX--
		}
		inner = width - 2*marginX - 2 - 4
	}
	content := dialogContent(t, tier, inner, title, body, hint)

	rows := strings.Split(strings.Join(content, "\n"), "\n")
	if width > 0 && height > 0 {
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

	box := buildDialogBox(t, tier)
	framed := box.Render(FillBG(t, tier, theme.RoleBGInset, strings.Join(rows, "\n")))

	if width <= 0 || height <= 0 {
		return framed
	}
	opts := dialogWhitespaceOptions(t, tier)
	out := lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, framed, opts...)
	if rows := strings.Split(out, "\n"); len(rows) > height {
		out = strings.Join(rows[:height], "\n")
	}
	return out
}

func buildDialogBox(t theme.Theme, tier theme.Tier) lipgloss.Style {
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(1, 2)
	if s := t.Resolve(theme.RoleBorder, tier); s.Hex != "" {
		box = box.BorderForeground(lipgloss.Color(s.Hex))
	} else if s.ANSI16 >= 0 {
		box = box.BorderForeground(lipgloss.Color(strconv.Itoa(s.ANSI16)))
	}
	if s := t.Resolve(theme.RoleBGInset, tier); s.Hex != "" {
		box = box.Background(lipgloss.Color(s.Hex))
	} else if s.ANSI16 >= 0 {
		box = box.Background(lipgloss.Color(strconv.Itoa(s.ANSI16)))
	}
	return box
}

func dialogWhitespaceOptions(t theme.Theme, tier theme.Tier) []lipgloss.WhitespaceOption {
	ws := lipgloss.NewStyle()
	if s := t.Resolve(theme.RoleBG, tier); s.Hex != "" {
		return []lipgloss.WhitespaceOption{lipgloss.WithWhitespaceStyle(ws.Background(lipgloss.Color(s.Hex)))}
	} else if s.ANSI16 >= 0 {
		return []lipgloss.WhitespaceOption{lipgloss.WithWhitespaceStyle(ws.Background(lipgloss.Color(strconv.Itoa(s.ANSI16))))}
	}
	return []lipgloss.WhitespaceOption{lipgloss.WithWhitespaceStyle(ws)}
}

// dialogContent stacks the parts with breathing room between them and
// formats the title header with a right-aligned [x] close button.
func dialogContent(t theme.Theme, tier theme.Tier, inner int, title, body, hint string) []string {
	var parts []string
	closeBtn := Role(t, tier, theme.RoleFGSubtle).Render("[") + Role(t, tier, theme.RoleDanger).Render("x") + Role(t, tier, theme.RoleFGSubtle).Render("]")

	if title != "" {
		titleStyled := Role(t, tier, theme.RoleFG).Bold(true).Render(title)
		if inner > 0 {
			titleWidth := ansi.StringWidth(title)
			gap := inner - titleWidth - 3
			if gap < 1 {
				gap = 1
			}
			parts = append(parts, titleStyled+strings.Repeat(" ", gap)+closeBtn)
		} else {
			parts = append(parts, titleStyled+"  "+closeBtn)
		}
	} else if inner > 3 {
		parts = append(parts, strings.Repeat(" ", inner-3)+closeBtn)
	}

	if body != "" {
		parts = append(parts, body)
	}
	if hint != "" {
		parts = append(parts, Role(t, tier, theme.RoleFGSubtle).Render(hint))
	}
	return strings.Split(strings.Join(parts, "\n\n"), "\n")
}

// DialogHitsClose reports whether a click at (clickX, clickY) lands on
// the [x] close button area of a dialog centered on a width x height terminal.
func DialogHitsClose(width, height, contentRowCount, clickX, clickY int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	marginX, marginY := dialogMarginX, dialogMarginY
	for marginX > 0 && width-2*marginX-2-4 < 12 {
		marginX--
	}
	for marginY > 0 && height-2*marginY-2-2 < 5 {
		marginY--
	}
	inner := width - 2*marginX - 2 - 4
	maxRows := height - 2*marginY - 2 - 2
	numRows := contentRowCount
	if numRows > maxRows {
		numRows = maxRows
	}
	boxWidth := inner + 6
	boxHeight := numRows + 4
	boxX := (width - boxWidth) / 2
	boxY := (height - boxHeight) / 2

	closeBtnX := boxX + 3 + inner - 3
	if clickY >= boxY && clickY <= boxY+2 && clickX >= closeBtnX-1 && clickX <= boxX+boxWidth {
		return true
	}
	return false
}

// DialogHitsBackdrop reports whether a click at (clickX, clickY) landed
// outside the centered dialog box.
func DialogHitsBackdrop(width, height, contentRowCount, clickX, clickY int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	marginX, marginY := dialogMarginX, dialogMarginY
	for marginX > 0 && width-2*marginX-2-4 < 12 {
		marginX--
	}
	for marginY > 0 && height-2*marginY-2-2 < 5 {
		marginY--
	}
	inner := width - 2*marginX - 2 - 4
	maxRows := height - 2*marginY - 2 - 2
	numRows := contentRowCount
	if numRows > maxRows {
		numRows = maxRows
	}
	boxWidth := inner + 6
	boxHeight := numRows + 4
	boxX := (width - boxWidth) / 2
	boxY := (height - boxHeight) / 2

	return clickX < boxX || clickX >= boxX+boxWidth || clickY < boxY || clickY >= boxY+boxHeight
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
			r = ansi.Truncate(r, inner, uikitconfig.ClipMarker)
		}
		out = append(out, r)
	}
	if len(out) <= maxRows {
		return out
	}
	// Overflow drops body rows off the bottom, but the LAST row
	// survives: it is the hint (the keys, the mouse override a caller
	// must keep on screen), and a dialog that swallows its own hint to
	// show one more body row has the wrong priority. No truncation
	// marker - the box is capped, not a pager.
	if maxRows < 2 {
		return out[:maxRows]
	}
	return append(out[:maxRows-1:maxRows-1], out[len(out)-1])
}
