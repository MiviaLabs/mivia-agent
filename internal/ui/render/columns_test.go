package render

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func TestColumnsPadsEachColumnToItsWidestCell(t *testing.T) {
	got := Columns(2, [][]string{
		{"openrouter", "active", "4 models"},
		{"ollama", "loopback", "2 models"},
		{"deepseek", "key missing", "0 models"},
	})
	want := []string{
		"openrouter  active       4 models",
		"ollama      loopback     2 models",
		"deepseek    key missing  0 models",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestColumnsRowsAllAlignToSameColumnStarts(t *testing.T) {
	got := Columns(2, [][]string{
		{"short", "x"},
		{"a much longer name", "y"},
	})
	// The second column ("x"/"y") must start at the same display column
	// on every row - that is the whole point of the primitive.
	if colAt(got[0], "x") != colAt(got[1], "y") {
		t.Errorf("columns do not align:\n%q\n%q", got[0], got[1])
	}
}

func TestColumnsLastCellIsNeverPadded(t *testing.T) {
	got := Columns(2, [][]string{
		{"a", "bb"},
		{"aaaa", "b"},
	})
	for i, row := range got {
		if row[len(row)-1] == ' ' {
			t.Errorf("row %d ends with trailing padding: %q", i, row)
		}
	}
}

func TestColumnsRaggedRowsDoNotBorrowAnotherRowsColumn(t *testing.T) {
	// A row with fewer cells than its neighbours ends at its own last
	// cell - it is not padded out to a column it has no content for,
	// and it produces no gap the neighbour's extra column would need.
	got := Columns(2, [][]string{
		{"name", "status", "4 models"},
		{"  model-a", "8k ctx"},
	})
	if got[1] != "  model-a  8k ctx" {
		t.Errorf("got %q, want the two-cell row unpadded past its own content", got[1])
	}
}

func TestColumnsEmptyInputReturnsNil(t *testing.T) {
	if got := Columns(2, nil); got != nil {
		t.Errorf("got %v, want nil for no rows", got)
	}
}

func TestColumnsMeasuresDisplayWidthNotByteLength(t *testing.T) {
	// ANSI-styled cells must not inflate the measured width by their
	// escape-sequence byte length - the padding must line up on the
	// VISIBLE width, matching every other width computation in this
	// package (ansi.StringWidth, not len).
	th := loadTheme(t)
	styled := Role(th, theme.TierTrueColor, theme.RoleSuccess).Render("ok")
	got := Columns(2, [][]string{
		{styled, "tail"},
		{"long-name", "tail"},
	})
	if colAt(got[0], "tail") != colAt(got[1], "tail") {
		t.Errorf("styled cell misaligned the following column:\n%q\n%q", got[0], got[1])
	}
}
