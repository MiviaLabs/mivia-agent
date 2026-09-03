package conversation

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The sidebar's formatting helpers, at their degenerate inputs. Each of
// these arms decides what a row does when there is no room to draw it or
// no number worth drawing, and each one is reached in the real product
// only on a terminal narrow enough that nobody runs the test by hand.

// TestTokensShortNeverPrintsANegativeCount: a calibration ratio can drive
// a bucket below zero for one frame, and "-3k" beside a token count reads
// as a bug in the accounting rather than a rounding artefact.
func TestTokensShortNeverPrintsANegativeCount(t *testing.T) {
	for _, n := range []int64{-1, -999, -1_500_000} {
		if got := tokensShort(n); got != "0" {
			t.Errorf("tokensShort(%d) = %q, want \"0\"", n, got)
		}
	}
	// The ladder itself, so the negative arm cannot be "fixed" by
	// flattening everything to zero.
	for _, tc := range []struct {
		in   int64
		want string
	}{{0, "0"}, {940, "940"}, {21_000, "21k"}, {1_000_000, "1M"}, {1_200_000, "1.2M"}} {
		if got := tokensShort(tc.in); got != tc.want {
			t.Errorf("tokensShort(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPanelSpreadRowGivesTheValueTheColumnsWhenBothCannotFit: the value
// is the number the row exists to report, so a squeezed row keeps it and
// clips the label - which stays readable from its first letters.
func TestPanelSpreadRowGivesTheValueTheColumnsWhenBothCannotFit(t *testing.T) {
	plain := lipgloss.NewStyle()

	// No room at all: nothing, rather than a row wider than the pane.
	for _, inner := range []int{0, -1} {
		if got := panelSpreadRow(inner, "context", "42%", plain, plain); got != "" {
			t.Errorf("inner=%d produced %q, want an empty row", inner, got)
		}
	}

	// The value alone is wider than the pane: it is truncated to the pane
	// and the label is dropped, never the other way round.
	got := ansi.Strip(panelSpreadRow(4, "messages", "1234567", plain, plain))
	if ansi.StringWidth(got) > 4 {
		t.Errorf("row %q is %d columns wide, want at most 4", got, ansi.StringWidth(got))
	}
	if strings.Contains(got, "messages") {
		t.Errorf("row %q kept the label at the value's expense", got)
	}

	// With room for both, the value ends the row.
	got = ansi.Strip(panelSpreadRow(20, "messages", "42k", plain, plain))
	if !strings.HasPrefix(got, "messages") || !strings.HasSuffix(got, "42k") {
		t.Errorf("row %q does not lay the label left against the value right", got)
	}
	if ansi.StringWidth(got) != 20 {
		t.Errorf("row %q is %d columns, want exactly 20", got, ansi.StringWidth(got))
	}
}

// TestPanelContextBarDrawsNothingWithNoColumns: the bar fills the pane's
// width, so a pane with no width must produce no bar rather than a
// one-glyph stub that reads as an empty context.
func TestPanelContextBarDrawsNothingWithNoColumns(t *testing.T) {
	s := foldScreen(t, 1)
	for _, inner := range []int{0, -3} {
		if got := s.panelContextBar(inner, 50, 10); got != "" {
			t.Errorf("inner=%d produced %q, want an empty bar", inner, got)
		}
	}
	if got := ansi.Strip(s.panelContextBar(10, 50, 10)); ansi.StringWidth(got) != 10 {
		t.Errorf("bar %q is %d columns, want exactly 10", got, ansi.StringWidth(got))
	}
}
