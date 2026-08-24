package render

import (
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

func dialogTheme(t *testing.T) theme.Theme {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if th.Name == "mivia-dark" {
			return th
		}
	}
	t.Fatal("mivia-dark theme not found")
	return theme.Theme{}
}

// TestDialogCentersAndFrames: the box is centered, exactly one box wide,
// and the whole frame is the given size.
func TestDialogCentersAndFrames(t *testing.T) {
	got := Dialog(dialogTheme(t), theme.TierTrueColor, 60, 20, "title", "body line", "hint")
	rows := strings.Split(got, "\n")
	if len(rows) != 20 {
		t.Fatalf("%d rows, want 20", len(rows))
	}
	var boxTop, boxBottom int
	for i, r := range rows {
		if w := ansi.StringWidth(r); w != 60 {
			t.Errorf("row %d width %d, want 60", i, w)
		}
		if strings.Contains(r, "╭") {
			boxTop = i
		}
		if strings.Contains(r, "╰") {
			boxBottom = i
		}
	}
	if boxTop == 0 || boxBottom == 0 || boxTop >= boxBottom {
		t.Fatalf("box rows %d..%d, want a centered frame", boxTop, boxBottom)
	}
	plain := ansi.Strip(got)
	for _, want := range []string{"title", "body line", "hint"} {
		if !strings.Contains(plain, want) {
			t.Errorf("dialog missing %q:\n%s", want, plain)
		}
	}
	if !strings.Contains(got, "\x1b[") {
		t.Error("true-color dialog carries no colour: inset background and border must colour it")
	}
}

// TestDialogDegradesByTier: ASCII and NoTTY keep the frame and the
// content with no colour escapes.
func TestDialogDegradesByTier(t *testing.T) {
	th := dialogTheme(t)
	for _, tier := range []theme.Tier{theme.TierASCII, theme.TierNoTTY} {
		got := Dialog(th, tier, 50, 16, "t", "b", "h")
		// Structural emphasis (the bold title, the dim hint) survives
		// NO_COLOR by design; colour fills must not.
		for _, colour := range []string{"38;2", "38;5", "48;2", "48;5", "\x1b[3"} {
			if strings.Contains(got, colour) {
				t.Errorf("tier %v carries colour %q", tier, colour)
			}
		}
		if !strings.Contains(got, "╭") || !strings.Contains(got, "b") {
			t.Errorf("tier %v lost the frame or the body", tier)
		}
	}
}

// TestDialogCapsLongContent: lines wider than the inner width clip, and
// surplus rows drop off the bottom rather than overflow the terminal.
func TestDialogCapsLongContent(t *testing.T) {
	longLine := strings.Repeat("x", 300)
	var manyRows []string
	for i := 0; i < 100; i++ {
		manyRows = append(manyRows, "row")
	}
	got := Dialog(dialogTheme(t), theme.TierASCII, 40, 12, "t", longLine+"\n"+strings.Join(manyRows, "\n"), "h")
	rows := strings.Split(got, "\n")
	if len(rows) != 12 {
		t.Fatalf("%d rows, want 12: the dialog must fill, not overflow, its frame", len(rows))
	}
	// The overflow must not swallow the hint: it is the one row the
	// clip guarantees, because it carries the keys and the mouse
	// override a caller must keep on screen.
	if !strings.Contains(ansi.Strip(got), "h") {
		t.Errorf("overflowing dialog lost its hint:\n%s", got)
	}
	for i, r := range rows {
		if w := ansi.StringWidth(r); w > 40 {
			t.Errorf("row %d width %d exceeds 40", i, w)
		}
	}
}

// TestDialogUnsizedRendersEverything: width or height 0 (exact-string
// tests, non-terminal renders) neither clips nor centers.
func TestDialogUnsizedRendersEverything(t *testing.T) {
	got := Dialog(dialogTheme(t), theme.TierASCII, 0, 0, "t", strings.Repeat("row\n", 50), "h")
	if n := strings.Count(got, "row"); n != 50 {
		t.Errorf("unsized dialog kept %d of 50 body rows", n)
	}
}

// TestDialogShrinksMarginsBeforeContent: a small terminal gives up the
// floating margin to keep the body, not the other way round.
func TestDialogShrinksMarginsBeforeContent(t *testing.T) {
	got := Dialog(dialogTheme(t), theme.TierASCII, 40, 10, "t", "> fast\n  deep", "h")
	plain := ansi.Strip(got)
	for _, want := range []string{"t", "fast", "deep"} {
		if !strings.Contains(plain, want) {
			t.Errorf("small dialog lost %q:\n%s", want, plain)
		}
	}
	if len(strings.Split(got, "\n")) != 10 {
		t.Errorf("small dialog does not fill its 10-row frame")
	}
}

// TestDialogAt16ColorsColoursTheBorder covers the ANSI16 arm: the
// border must carry the theme's authored 16-colour index (bright white
// renders as a 9x foreground), not skip colour entirely.
func TestDialogAt16ColorsColoursTheBorder(t *testing.T) {
	got := Dialog(dialogTheme(t), theme.Tier16, 50, 16, "t", "b", "h")
	if !strings.Contains(got, "38;5") && !strings.Contains(got, "\x1b[9") && !strings.Contains(got, "\x1b[3") {
		t.Errorf("16-colour dialog has no border colour:\n%s", got)
	}
}

// TestDialogNarrowWidthShrinksTheSideMargin covers the marginX arm: a
// terminal too narrow for the comfortable margin keeps the content
// rather than the floating look.
func TestDialogNarrowWidthShrinksTheSideMargin(t *testing.T) {
	got := Dialog(dialogTheme(t), theme.TierASCII, 20, 14, "t", "body text", "h")
	plain := ansi.Strip(got)
	for _, want := range []string{"t", "body text", "h"} {
		if !strings.Contains(plain, want) {
			t.Errorf("narrow dialog lost %q:\n%s", want, plain)
		}
	}
	for i, r := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(r); w > 20 {
			t.Errorf("row %d width %d exceeds 20", i, w)
		}
	}
}

// TestDialogOnADegenerateTerminal: heights too small for any content
// still render a full frame without panicking, and the clip's
// one-row-floor keeps exactly one row.
func TestDialogOnADegenerateTerminal(t *testing.T) {
	for _, h := range []int{1, 3, 4, 6} {
		got := Dialog(dialogTheme(t), theme.TierASCII, 40, h, "t", "one\ntwo\nthree", "h")
		rows := strings.Split(got, "\n")
		if len(rows) != h {
			t.Errorf("height %d: %d rows, want %d", h, len(rows), h)
		}
		for i, r := range rows {
			if w := ansi.StringWidth(r); w > 40 {
				t.Errorf("height %d row %d width %d exceeds 40", h, i, w)
			}
		}
	}
}

// TestDialogBodyRowsStaysInsideWhatDialogShows: the exported body-row
// count a scroller uses must never exceed the rows a real Dialog of the
// same height actually renders - an overcount makes the tail of the
// body unreachable.
func TestDialogBodyRowsStaysInsideWhatDialogShows(t *testing.T) {
	for _, h := range []int{10, 14, 20, 30} {
		fit := DialogBodyRows(h)
		if fit < 1 {
			t.Fatalf("DialogBodyRows(%d) = %d, want at least 1", h, fit)
		}
		// Render a dialog whose body is exactly one row longer than the
		// claimed fit; the first fit rows must be visible and the
		// overflow row clipped.
		body := make([]string, fit+1)
		for i := range body {
			body[i] = "row-" + strings.Repeat("x", i+1)
		}
		got := Dialog(dialogTheme(t), theme.TierASCII, 60, h, "t", strings.Join(body, "\n"), "hint")
		plain := ansi.Strip(got)
		if !strings.Contains(plain, body[0]) {
			t.Errorf("height %d: first body row missing:\n%s", h, plain)
		}
		if strings.Contains(plain, body[fit]) {
			t.Errorf("height %d: DialogBodyRows(%d)=%d overcounts - the overflow row is still shown", h, h, fit)
		}
		if DialogBodyRows(h+10) <= fit {
			t.Errorf("DialogBodyRows(%d) not smaller than DialogBodyRows(%d); more height must show more body", h+10, h)
		}
	}
}

// dialogInterior returns the part of a dialog row between its left and
// right border glyphs, and whether the row has an interior at all. Only
// that region is filled with the inset background; the border glyph and
// the centering whitespace around the box carry other roles.
func dialogInterior(row string) (string, bool) {
	first := strings.Index(row, "\u2502")
	last := strings.LastIndex(row, "\u2502")
	if first < 0 || last <= first {
		return "", false
	}
	return row[first+len("\u2502") : last], true
}

// unpaintedCells counts the printable cells in a dialog row's interior
// that are drawn while no background is set, so the terminal's own
// colour shows through. Every such run is a rectangle of the wrong
// colour behind the text - the black boxes a light theme's preview
// showed. It replays the row's SGR state rather than pattern-matching
// escape sequences, so a reset that only precedes more escapes (or ends
// the row) correctly counts for nothing.
func unpaintedCells(interior string) int {
	n, painted := 0, false
	for i := 0; i < len(interior); {
		if seq, ok := sgrAt(interior, i); ok {
			painted = sgrSetsBackground(seq, painted)
			i += len(seq)
			continue
		}
		_, size := utf8.DecodeRuneInString(interior[i:])
		if !painted {
			n++
		}
		i += size
	}
	return n
}

// sgrAt returns the SGR sequence starting at i, if there is one.
func sgrAt(s string, i int) (string, bool) {
	if !strings.HasPrefix(s[i:], "\x1b[") {
		return "", false
	}
	end := strings.IndexByte(s[i:], 'm')
	if end < 0 {
		return "", false
	}
	return s[i : i+end+1], true
}

// sgrSetsBackground replays one SGR sequence over the current
// background state: a reset clears it, a 48/4x/10x parameter sets it,
// anything else leaves it alone.
func sgrSetsBackground(seq string, painted bool) bool {
	params := strings.Split(strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m"), ";")
	for i := 0; i < len(params); i++ {
		switch p := params[i]; {
		case p == "" || p == "0":
			painted = false
		case p == "48":
			painted = true
			i = len(params) // the rest of the sequence is this colour's arguments
		case p == "49":
			painted = false
		case len(p) == 2 && p[0] == '4' && p[1] >= '0' && p[1] <= '7':
			painted = true // 40-47, the 8 basic backgrounds
		case len(p) == 3 && p[0] == '1' && p[1] == '0' && p[2] >= '0' && p[2] <= '7':
			painted = true // 100-107, the bright backgrounds
		}
	}
	return painted
}

// TestDialogHoldsItsInsetBackgroundAcrossSegments is the regression for
// the dark rectangles inside a light dialog: body text built from more
// than one styled run ends each run with a reset, and lipgloss re-opens
// the box background only for the first run. Every later run therefore
// drew on the terminal's own background.
func TestDialogHoldsItsInsetBackgroundAcrossSegments(t *testing.T) {
	th := dialogTheme(t)
	tier := theme.TierTrueColor
	body := Role(th, tier, theme.RoleKeyword).Render("func") +
		Role(th, tier, theme.RoleFG).Render(" (u *") +
		Role(th, tier, theme.RoleType).Render("Uploader") +
		Role(th, tier, theme.RoleFG).Render(") error {")

	// Unsized: the output is the box alone, with no centering whitespace
	// around it, so every reset in it belongs to the box's interior.
	got := Dialog(th, tier, 0, 0, "title", body, "hint")
	bg := ansi.NewStyle().BackgroundColor(lipgloss.Color(th.Colors[theme.RoleBGInset])).String()
	if bg == "" {
		t.Fatal("mivia-dark defines no bg-inset colour; this test needs one")
	}
	for i, row := range strings.Split(got, "\n") {
		interior, ok := dialogInterior(row)
		if !ok {
			continue
		}
		if n := unpaintedCells(interior); n > 0 {
			t.Errorf("row %d draws %d cell(s) on the terminal background: %q", i, n, row)
		}
	}
}

// TestDialogAddsNoBackgroundWithoutColour holds the degradation ladder:
// the background continuation must contribute nothing at a tier that
// has no colour to contribute.
func TestDialogAddsNoBackgroundWithoutColour(t *testing.T) {
	th := dialogTheme(t)
	for _, tier := range []theme.Tier{theme.TierASCII, theme.TierNoTTY} {
		got := Dialog(th, tier, 60, 20, "title", "body line", "hint")
		if strings.Contains(got, "\x1b[4") {
			t.Errorf("tier %v drew a background: %q", tier, got)
		}
	}
}

// TestDialogClipsAWideBodyRowWithTheClipMarker pins wireframes-panes.md
// section 8: "the renderer must clip a row wider than the panel...
// with a `~` marking the clip." dialogClip previously truncated with
// no marker, so a cut row was visually indistinguishable from one that
// just happened to end there.
func TestDialogClipsAWideBodyRowWithTheClipMarker(t *testing.T) {
	wide := strings.Repeat("x", 60)
	got := Dialog(dialogTheme(t), theme.TierASCII, 30, 14, "t", wide, "h")
	if !strings.Contains(got, uikitconfig.ClipMarker) {
		t.Errorf("got %q, want the clip marker %q on the truncated body row", got, uikitconfig.ClipMarker)
	}
}

// TestDialogBorderUsesTheDecorativeRoleNotFocus pins the "thin dim
// border" the dialog frame and the approval prompt's own frame both
// use: RoleBorder, the decorative role (wireframes-panes.md section 18:
// #52525b on mivia-dark), not RoleBorderFocus, the brighter
// state-carrying role (#fafafa) - approval.go's own BorderedWithHint
// call already makes this choice explicitly ("RoleBorder, the
// decorative role, not RoleBorderFocus"); Dialog previously used
// RoleBorderFocus, so the two bordered surfaces read as two different
// weights instead of one consistent frame.
func TestDialogBorderUsesTheDecorativeRoleNotFocus(t *testing.T) {
	th := dialogTheme(t)
	got := Dialog(th, theme.TierTrueColor, 50, 16, "t", "b", "h")

	// ansiFGPrefix is the SGR escape lipgloss emits for a truecolor
	// foreground of hex, up to (not including) the styled content - the
	// same prefix Border(...).BorderForeground(lipgloss.Color(hex))
	// would produce for the border glyphs.
	ansiFGPrefix := func(hex string) string {
		styled := lipgloss.NewStyle().Foreground(lipgloss.Color(hex)).Render("X")
		return strings.SplitN(styled, "X", 2)[0]
	}
	borderSeq := ansiFGPrefix(th.Resolve(theme.RoleBorder, theme.TierTrueColor).Hex)
	focusSeq := ansiFGPrefix(th.Resolve(theme.RoleBorderFocus, theme.TierTrueColor).Hex)
	if borderSeq == focusSeq {
		t.Fatal("test setup: RoleBorder and RoleBorderFocus resolve to the same escape on mivia-dark, cannot distinguish")
	}
	if !strings.Contains(got, borderSeq) {
		t.Errorf("dialog border does not use RoleBorder's colour %q:\n%s", borderSeq, got)
	}
	if strings.Contains(got, focusSeq) {
		t.Errorf("dialog border still uses RoleBorderFocus's colour %q:\n%s", focusSeq, got)
	}
}

func TestDialogCloseButtonAndHitTesting(t *testing.T) {
	th := dialogTheme(t)
	width, height := 80, 24
	got := Dialog(th, theme.TierTrueColor, width, height, "Test Title", "Body content", "esc to cancel")
	plain := ansi.Strip(got)

	if !strings.Contains(plain, "[x]") {
		t.Errorf("dialog missing [x] close button:\n%s", plain)
	}

	// Hit test close button (top-right of dialog box)
	// Dialog box inner width is 80 - 2*4 - 6 = 66, boxWidth = 72, boxX = 4, boxY = 7
	// closeBtnX is roughly 4 + 3 + 66 - 3 = 70
	if !DialogHitsClose(width, height, 5, 70, 8) {
		t.Errorf("expected DialogHitsClose to return true for click on close button")
	}

	// Click on top-left of dialog (title area) should NOT hit close button
	if DialogHitsClose(width, height, 5, 10, 8) {
		t.Errorf("expected DialogHitsClose to return false for click on title")
	}

	// Click on backdrop (far outside dialog box)
	if !DialogHitsBackdrop(width, height, 5, 1, 1) {
		t.Errorf("expected DialogHitsBackdrop to return true for click outside box")
	}

	// Click inside dialog body should NOT hit backdrop
	if DialogHitsBackdrop(width, height, 5, 40, 12) {
		t.Errorf("expected DialogHitsBackdrop to return false for click inside dialog")
	}
}
