package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
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
