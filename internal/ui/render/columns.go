package render

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Columns aligns a table of pre-styled cells into columns: every column
// is padded to the widest cell that occupies it across the whole row
// set, so status/count fields land in the same screen column on every
// row instead of drifting with each row's own content width (the
// settings sections' failure mode this replaces - see
// docs/design/settings-screen.md section 1's aligned layout).
//
// gap is the number of plain spaces between one column's padded content
// and the next. Rows may carry a different number of cells (a "ragged"
// table): a column only exists where some row supplies it, and a
// shorter row is never padded past its own last cell, so it produces no
// trailing whitespace.
//
// Cells may already carry ANSI styling; width is measured with
// ansi.StringWidth so padding accounts for display width, not byte
// length. Pure: input in, string slice out, no I/O and no package
// state.
func Columns(gap int, rows [][]string) []string {
	if len(rows) == 0 {
		return nil
	}
	if gap < 0 {
		gap = 0
	}

	maxCols := 0
	for _, r := range rows {
		if len(r) > maxCols {
			maxCols = len(r)
		}
	}
	widths := make([]int, maxCols)
	for _, r := range rows {
		for i, c := range r {
			if w := ansi.StringWidth(c); w > widths[i] {
				widths[i] = w
			}
		}
	}

	gapStr := strings.Repeat(" ", gap)
	out := make([]string, len(rows))
	for ri, r := range rows {
		var sb strings.Builder
		for i, c := range r {
			sb.WriteString(c)
			if i == len(r)-1 {
				// The row's own last cell: nothing follows, so no pad and
				// no gap. A shorter row than its neighbours ends here,
				// which is what keeps a ragged table free of trailing
				// whitespace instead of padding to a column it has no
				// content for.
				break
			}
			if pad := widths[i] - ansi.StringWidth(c); pad > 0 {
				sb.WriteString(strings.Repeat(" ", pad))
			}
			sb.WriteString(gapStr)
		}
		out[ri] = sb.String()
	}
	return out
}
