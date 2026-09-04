package welcome

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// TestTheWelcomeBoxIsDroppedWhenItWouldNotFit: the border costs two
// columns the content also needs. Below that width the lines are returned
// unboxed rather than boxed and overflowing, because a box wider than the
// terminal wraps into a second row of broken border glyphs.
func TestTheWelcomeBoxIsDroppedWhenItWouldNotFit(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	lines := []string{"mivia", "a second line"}

	for _, width := range []int{1, 4, 8, 12} {
		got := m.boxed(lines, width)
		if len(got) != len(lines) {
			t.Errorf("width %d: returned %d rows, want the %d unboxed lines", width, len(got), len(lines))
			continue
		}
		for i := range got {
			if got[i] != lines[i] {
				t.Errorf("width %d: row %d was rewritten to %q, want %q unchanged", width, i, got[i], lines[i])
			}
		}
	}

	// Wide enough, and the box is drawn - otherwise the assertions above
	// would pass on a function that never boxes anything.
	wide := m.boxed(lines, 60)
	if len(wide) <= len(lines) {
		t.Fatalf("a 60-column box added no border rows: %q", wide)
	}
	joined := ansi.Strip(strings.Join(wide, "\n"))
	if !strings.ContainsAny(joined, "+|-") {
		t.Errorf("the boxed form carries no border glyphs:\n%s", joined)
	}
	for i, row := range wide {
		if got := ansi.StringWidth(ansi.Strip(row)); got > 60 {
			t.Errorf("boxed row %d is %d columns, wider than the 60 it was given", i, got)
		}
	}
}
