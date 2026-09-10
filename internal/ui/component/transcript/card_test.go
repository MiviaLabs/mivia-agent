package transcript

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestCardBodyTintHasMarginOnBothSides pins the card's left AND right
// margin: the tinted background covers one column BEFORE the text
// starts and stops one plain column BEFORE the row's own right edge, so
// the card reads as padded on both sides rather than text glued to the
// fill's edges. The visible text position must not move - only where
// the tint itself starts and ends.
func TestCardBodyTintHasMarginOnBothSides(t *testing.T) {
	th := loadTheme(t)
	b := Block{
		Kind:   uievent.KindToolEnd,
		Header: Header{Label: "read_file", Role: theme.RoleSuccess},
		Body:   []string{"hello"},
	}
	rows := strings.Split(b.Render(th, theme.TierTrueColor, 80), "\n")
	if len(rows) < 3 {
		t.Fatalf("got %d rows, want header, blank, body: %q", len(rows), rows)
	}
	bodyRow := rows[2]
	// padToWidth fills the row to the card's content width, so compare
	// only the leading run: the indent, then the text, unaffected by the
	// trailing padding that reaches the card's right edge.
	if plain := ansi.Strip(bodyRow); !strings.HasPrefix(plain, "    hello") {
		t.Fatalf("visible text position moved: got %q, want it to start with %q", plain, "    hello")
	}
	if len(bodyRow) < 4 || bodyRow[:3] != "   " {
		t.Fatalf("the first 3 columns must stay plain indent, got %q", bodyRow)
	}
	if bodyRow[3] != 0x1b {
		t.Errorf("the tint must start at column 4, one column left of the text (\"hello\" at column 5): row %q", bodyRow)
	}
	// Symmetric right margin: the tint's own reset must be followed by
	// one plain, untinted space, so the fill does not touch the row's
	// right edge either.
	if !strings.HasSuffix(bodyRow, ansi.ResetStyle+" ") {
		t.Errorf("the tint must end with a plain trailing space (a right margin), got %q", bodyRow)
	}
}

// TestPadToWidthTruncatesOverWidthAnsiContent pins the fix for a real
// row-overflow bug: block.go's Render prepends a margin space to an
// already-wrapped line before calling padToWidth, and a diff line -
// render.FormatDiffLines pre-pads every row to bodyRows' own wrap
// ceiling, with zero slack - lands exactly one column over that ceiling
// once the margin is added. padToWidth must clip it back down, ANSI
// escapes intact, rather than leaving it one column too wide (which
// TestReplayDrivesTranscript's width contract catches downstream, but
// this pins the cause directly).
func TestPadToWidthTruncatesOverWidthAnsiContent(t *testing.T) {
	th := loadTheme(t)
	styled := render.Role(th, theme.TierTrueColor, theme.RoleDiffAddFG).Render("0123456789")
	if w := ansi.StringWidth(styled); w != 10 {
		t.Fatalf("precondition: styled content is 10 display columns, got %d", w)
	}
	got := padToWidth(styled, 8)
	if w := ansi.StringWidth(got); w != 8 {
		t.Fatalf("padToWidth(_, 8) on a 10-column styled string = %d columns, want exactly 8", w)
	}
	if plain := ansi.Strip(got); plain != "01234567" {
		t.Errorf("truncation cut the wrong end: got %q, want the first 8 runes kept", plain)
	}
}

// TestCardBodyTintNeverOverflowsAMaxWidthDiffLine reproduces the exact
// failure this session hit: a diff whose lines are pre-padded to the
// card's full body-wrap width (a live single-line addition, the
// simplest case that reaches the ceiling) must still render every row
// at or under the terminal width once the left+right tint margins are
// added.
func TestCardBodyTintNeverOverflowsAMaxWidthDiffLine(t *testing.T) {
	th := loadTheme(t)
	const width = 80
	diff := &uievent.Diff{Path: "file.go", Hunks: []uievent.DiffHunk{{
		Header: "@@ -1,1 +1,1 @@",
		Lines:  []uievent.DiffLine{{Kind: uievent.DiffLineAdd, Text: "first"}},
	}}}
	b := Block{
		Kind:   uievent.KindToolEnd,
		Header: Header{Label: "edit", Detail: "file.go", Role: theme.RoleSuccess},
		Diff:   diff,
		Body:   render.FormatDiffLines(th, theme.TierTrueColor, width-groupIndent-uikitconfig.BodyIndent, *diff),
	}
	for _, row := range strings.Split(b.Render(th, theme.TierTrueColor, width-groupIndent), "\n") {
		if w := ansi.StringWidth(row); w > width-groupIndent {
			t.Errorf("row is %d columns, wider than the %d-column budget: %q", w, width-groupIndent, row)
		}
	}
}

// TestCardHeightAgreesWithRenderRowCount pins the contract Height and
// Render must never violate for a tool card (C4): Height(width) is
// exactly the row count Render(...) actually draws, for a collapsed
// (windowed) card, a fully expanded one, and a focused collapsed one -
// the three shapes whose row math differs (a window-plus-hint, a full
// body with no hint, and a hint row with the extra "space to expand"
// suffix that must not add a row of its own).
func TestCardHeightAgreesWithRenderRowCount(t *testing.T) {
	th := loadTheme(t)
	const width = 80
	body := make([]string, uikitconfig.CollapseThresholdLines+5)
	for i := range body {
		body[i] = fmt.Sprintf("line-%d", i)
	}
	base := Block{
		Kind:        uievent.KindToolEnd,
		Header:      Header{Label: "run_command", Detail: "go test ./...", Meta: "4.1s", Role: theme.RoleSuccess},
		Body:        body,
		Collapsible: true,
	}

	for _, tc := range []struct {
		name string
		b    Block
	}{
		{"collapsed", func() Block { b := base; b.Collapsed = true; return b }()},
		{"expanded", func() Block { b := base; b.Collapsed = false; return b }()},
		{"focused collapsed", func() Block { b := base; b.Collapsed, b.Focused = true, true; return b }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			height := tc.b.Height(width)
			rows := strings.Split(tc.b.Render(th, theme.TierASCII, width), "\n")
			if height != len(rows) {
				t.Errorf("Height(%d) = %d, but Render drew %d rows:\n%s", width, height, len(rows), strings.Join(rows, "\n"))
			}
		})
	}
}

// TestCardHeightAgreesWithRenderRowCountShortBody covers a body that
// fits inside the window (no hint row): the same Height/Render
// agreement, for the case where collapsed and expanded draw identically.
func TestCardHeightAgreesWithRenderRowCountShortBody(t *testing.T) {
	th := loadTheme(t)
	const width = 80
	base := Block{
		Kind:        uievent.KindToolEnd,
		Header:      Header{Label: "read_file", Detail: "main.go", Meta: "12ms", Role: theme.RoleSuccess},
		Body:        []string{"package main", "func main() {}"},
		Collapsible: true,
	}
	for _, collapsed := range []bool{true, false} {
		b := base
		b.Collapsed = collapsed
		height := b.Height(width)
		rows := strings.Split(b.Render(th, theme.TierASCII, width), "\n")
		if height != len(rows) {
			t.Errorf("collapsed=%v: Height(%d) = %d, but Render drew %d rows:\n%s",
				collapsed, width, height, len(rows), strings.Join(rows, "\n"))
		}
	}
}
