package sel

import (
	"strings"
	"testing"
)

func TestHighlightLinesSingleRow(t *testing.T) {
	got := HighlightLines([]string{"hello world"}, Cell{0, 0}, Cell{0, 4})
	want := "\x1b[7mhello\x1b[27m world"
	if got[0] != want {
		t.Fatalf("got %q, want %q", got[0], want)
	}
}

func TestHighlightLinesReversedDrag(t *testing.T) {
	lines := []string{"abcdefgh", "ijklmnop"}
	forward := HighlightLines(lines, Cell{0, 2}, Cell{1, 3})
	backward := HighlightLines(lines, Cell{1, 3}, Cell{0, 2})
	if forward[0] != backward[0] || forward[1] != backward[1] {
		t.Fatalf("reversed selection must paint identically: %q vs %q", forward, backward)
	}
	if !strings.Contains(forward[0], "\x1b[7mcdefgh\x1b[27m") {
		t.Fatalf("anchor row tail not highlighted: %q", forward[0])
	}
	if !strings.HasPrefix(forward[1], "\x1b[7mijkl\x1b[27m") {
		t.Fatalf("focus row head not highlighted: %q", forward[1])
	}
}

func TestHighlightLinesPreservesStylesAndWideCells(t *testing.T) {
	// A styled prefix and a wide cell must survive the cut boundaries.
	line := "\x1b[31mab你好cd"
	got := HighlightLines([]string{line}, Cell{0, 2}, Cell{0, 5})[0]
	if !strings.HasPrefix(got, "\x1b[31mab") {
		t.Fatalf("styled prefix lost: %q", got)
	}
	if !strings.Contains(got, "\x1b[7m") && !strings.Contains(got, "你好") {
		t.Fatalf("cells not highlighted: %q", got)
	}
	if stripped := stripSGR(got); stripped != "ab你好cd" {
		t.Fatalf("text content changed under highlight: %q", stripped)
	}
}

func stripSGR(s string) string {
	out := strings.NewReplacer("\x1b[7m", "", "\x1b[27m", "", "\x1b[31m", "").Replace(s)
	return out
}

func TestHighlightLinesOutOfRangeRowsClamp(t *testing.T) {
	got := HighlightLines([]string{"only"}, Cell{0, 0}, Cell{9, 9})
	if got[0] != "\x1b[7monly\x1b[27m" {
		t.Fatalf("row beyond slice must clamp to last row: %q", got[0])
	}
}

func TestStreamSelectSingleRowInclusive(t *testing.T) {
	got := StreamSelect([]string{"hello world"}, Cell{0, 6}, Cell{0, 10})
	if want := "world"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStreamSelectMultiRow(t *testing.T) {
	rows := []string{"aaa bbb", "ccc ddd", "eee fff"}
	got := StreamSelect(rows, Cell{0, 4}, Cell{2, 2})
	want := "bbb\nccc ddd\neee"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStreamSelectStripsStyles(t *testing.T) {
	rows := []string{"\x1b[1mhello\x1b[22m"}
	got := StreamSelect(rows, Cell{0, 0}, Cell{0, 4})
	if got != "hello" {
		t.Fatalf("styles must be stripped: %q", got)
	}
}

func TestStreamSelectEmptyWhenDegenerate(t *testing.T) {
	rows := []string{"abc"}
	if got := StreamSelect(rows, Cell{5, 0}, Cell{9, 0}); got != "" {
		t.Fatalf("all rows past the end must yield empty: %q", got)
	}
}

func TestRectContainsHalfOpen(t *testing.T) {
	r := Rect{MinX: 1, MinY: 2, MaxX: 11, MaxY: 5}
	if !r.Contains(1, 2) || !r.Contains(10, 4) {
		t.Fatal("min corner and max-minus-one must contain")
	}
	if r.Contains(11, 2) || r.Contains(1, 5) || r.Contains(0, 2) {
		t.Fatal("max edge is exclusive, min edge inclusive")
	}
	// Mutant boundary kills: the row exactly at MaxY-1 contains, MaxY not.
	if !r.Contains(5, 4) || r.Contains(5, 5) {
		t.Fatalf("MaxY boundary wrong: %+v", r)
	}
}

func TestHighlightLinesMultiRowInnerRowsWhole(t *testing.T) {
	// The middle row of a three-row selection must be painted whole; a
	// mutant that drops it (continue -> no-op or <= bound) leaves a gap.
	lines := []string{"aaaa", "bbbb", "cccc"}
	got := HighlightLines(lines, Cell{Row: 0, Col: 2}, Cell{Row: 2, Col: 1})
	if got[1] != "\x1b[7mbbbb\x1b[27m" {
		t.Fatalf("inner row must paint whole: %q", got[1])
	}
	if !strings.HasPrefix(got[0], "aa\x1b[7maa\x1b[27m") {
		t.Fatalf("anchor row tail wrong: %q", got[0])
	}
	if !strings.HasPrefix(got[2], "\x1b[7mcc\x1b[27mc") {
		t.Fatalf("focus row head wrong: %q", got[2])
	}
}

func TestStreamSelectMultiRowInnerRowsWhole(t *testing.T) {
	rows := []string{"aaaa", "bbbb", "cccc"}
	got := StreamSelect(rows, Cell{Row: 0, Col: 2}, Cell{Row: 2, Col: 1})
	want := "aa\nbbbb\ncc"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHighlightLinesRowBeforeFromSkipped(t *testing.T) {
	// from.Row > to.Row normalizes; a negative row cannot survive it.
	lines := []string{"zero", "one"}
	got := HighlightLines(lines, Cell{Row: -2, Col: 0}, Cell{Row: -1, Col: 1})
	if got[0] != "zero" || got[1] != "one" {
		t.Fatalf("negative rows must paint nothing: %q", got)
	}
}

func TestHighlightLinesEmptySpanSkipsPaint(t *testing.T) {
	// right <= left on the focus row (focus column before anchor start
	// of an empty middle) must leave the line untouched.
	lines := []string{"abcdef"}
	got := HighlightLines(lines, Cell{Row: 0, Col: 3}, Cell{Row: 0, Col: 3})
	if got[0] == "" {
		t.Fatal("row content lost")
	}
	// col+1 makes the span one cell wide, so this paints exactly "d".
	if !strings.Contains(got[0], "\x1b[7md\x1b[27m") {
		t.Fatalf("single-cell highlight wrong: %q", got[0])
	}
}

func TestStreamSelectInnerRowsWholeAndEndExclusive(t *testing.T) {
	rows := []string{"aaaaaa", "bbbbbb", "cccccc"}
	got := StreamSelect(rows, Cell{Row: 0, Col: 6}, Cell{Row: 2, Col: 0})
	want := "\nbbbbbb\nc" // anchor at row end yields empty first row
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStreamSelectReversedPointsNormalized(t *testing.T) {
	rows := []string{"hello world"}
	forward := StreamSelect(rows, Cell{Row: 0, Col: 0}, Cell{Row: 0, Col: 4})
	backward := StreamSelect(rows, Cell{Row: 0, Col: 4}, Cell{Row: 0, Col: 0})
	if forward != backward || forward != "hello" {
		t.Fatalf("reversal must normalize: %q vs %q", forward, backward)
	}
}

// Boundary kills for the ordering/clamping comparisons.

func TestHighlightLinesOrderingSameRowAndMultiRow(t *testing.T) {
	lines := []string{"abcdef", "ghijkl"}
	// Same-row reversed equals forward ordering.
	fwd := HighlightLines(lines, Cell{Row: 0, Col: 1}, Cell{Row: 0, Col: 3})
	bwd := HighlightLines(lines, Cell{Row: 0, Col: 3}, Cell{Row: 0, Col: 1})
	if fwd[0] != bwd[0] {
		t.Fatalf("same-row reversal must normalize: %q vs %q", fwd[0], bwd[0])
	}
	// Multi-row reversed equals forward ordering.
	fwd = HighlightLines(lines, Cell{Row: 0, Col: 2}, Cell{Row: 1, Col: 1})
	bwd = HighlightLines(lines, Cell{Row: 1, Col: 1}, Cell{Row: 0, Col: 2})
	if fwd[0] != bwd[0] || fwd[1] != bwd[1] {
		t.Fatalf("multi-row reversal must normalize: %q vs %q", fwd, bwd)
	}
}

func TestHighlightLinesEmptySpanSkipsPaintButKeepsRows(t *testing.T) {
	// A focus row whose column lands before the anchor's start produces
	// right <= left on that row: skipped, but the OTHER rows still paint.
	lines := []string{"aaaa", "bbbb"}
	got := HighlightLines(lines, Cell{Row: 1, Col: 3}, Cell{Row: 1, Col: 3})
	if !strings.Contains(got[1], "\x1b[7mb\x1b[27m") {
		t.Fatalf("single-cell span must paint one cell: %q", got[1])
	}
}

func TestStreamSelectNegativeFromRowClampsToZero(t *testing.T) {
	rows := []string{"abc", "def"}
	got := StreamSelect(rows, Cell{Row: -5, Col: 0}, Cell{Row: 0, Col: 2})
	if got != "abc" {
		t.Fatalf("negative from.Row must clamp to 0: %q", got)
	}
}

func TestSelectionOrderedBoundaryCases(t *testing.T) {
	// Equal rows, greater col swaps; equal cells do not.
	s := Selection{Anchor: Cell{Row: 1, Col: 5}, Focus: Cell{Row: 1, Col: 2}}
	from, to := s.Ordered()
	if from != (Cell{Row: 1, Col: 2}) || to != (Cell{Row: 1, Col: 5}) {
		t.Fatalf("same-row ordering failed: %+v %+v", from, to)
	}
	s = Selection{Anchor: Cell{Row: 2, Col: 0}, Focus: Cell{Row: 2, Col: 0}}
	from, to = s.Ordered()
	if from != to {
		t.Fatalf("identical cells must stay identical: %+v %+v", from, to)
	}
}

func TestRectContainsColumnBoundary(t *testing.T) {
	r := Rect{MinX: 2, MinY: 1, MaxX: 6, MaxY: 3}
	if r.Contains(6, 1) || !r.Contains(5, 1) {
		t.Fatal("MaxX boundary must be exclusive")
	}
	if r.Contains(1, 1) {
		t.Fatal("MinX boundary must be inclusive")
	}
}

func TestFromScreenAllFourEdgesClamp(t *testing.T) {
	r := Rect{MinX: 2, MinY: 3, MaxX: 6, MaxY: 7}
	if c := FromScreen(r, 0, 0); c != (Cell{Row: 0, Col: 0}) {
		t.Fatalf("top-left clamp: %+v", c)
	}
	if c := FromScreen(r, 99, 99); c != (Cell{Row: 3, Col: 3}) {
		t.Fatalf("bottom-right clamp: %+v", c)
	}
	if c := FromScreen(r, 99, 0); c != (Cell{Row: 0, Col: 3}) {
		t.Fatalf("right+top clamp: %+v", c)
	}
	if c := FromScreen(r, 0, 99); c != (Cell{Row: 3, Col: 0}) {
		t.Fatalf("left+bottom clamp: %+v", c)
	}
}

// Boundary kills for the comparison operators in highlight/stream.

func TestHighlightLinesReversalOnlyWhenStrictlyAfter(t *testing.T) {
	lines := []string{"abcdef"}
	// to == from must NOT swap (a <= mutant would): paints one cell at col 2.
	got := HighlightLines(lines, Cell{Row: 0, Col: 2}, Cell{Row: 0, Col: 2})
	if !strings.Contains(got[0], "\x1b[7mc\x1b[27m") {
		t.Fatalf("equal cells paint exactly one cell: %q", got[0])
	}
	// to.Row == from.Row && to.Col < from.Col must swap.
	a := HighlightLines(lines, Cell{Row: 0, Col: 4}, Cell{Row: 0, Col: 1})
	b := HighlightLines(lines, Cell{Row: 0, Col: 1}, Cell{Row: 0, Col: 4})
	if a[0] != b[0] {
		t.Fatalf("same-row reversed must normalize: %q vs %q", a[0], b[0])
	}
}

func TestStreamSelectNegativeToRowReturnsEmpty(t *testing.T) {
	rows := []string{"abc"}
	if got := StreamSelect(rows, Cell{Row: -5, Col: 0}, Cell{Row: -2, Col: 0}); got != "" {
		t.Fatalf("to.Row clamped below from.Row must yield empty: %q", got)
	}
}

func TestStreamSelectZeroWidthSpanIsEmptyLine(t *testing.T) {
	// Anchor at the end of row 0 (col == width): the anchor row
	// contributes an empty string, and StreamSelect still joins rows.
	rows := []string{"abcd", "efgh"}
	got := StreamSelect(rows, Cell{Row: 0, Col: 4}, Cell{Row: 1, Col: 1})
	want := "\nef"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestClampIntBelowLoAndAboveHi(t *testing.T) {
	if clampInt(-3, 0, 5) != 0 {
		t.Fatal("below lo must clamp to lo")
	}
	if clampInt(9, 0, 5) != 5 {
		t.Fatal("above hi must clamp to hi")
	}
	if clampInt(2, 0, 5) != 2 {
		t.Fatal("in-range value passes through")
	}
}

func TestFromScreenClampsToRegion(t *testing.T) {
	r := Rect{MinX: 2, MinY: 3, MaxX: 6, MaxY: 7}
	c := FromScreen(r, -10, 100)
	if c != (Cell{Row: 3, Col: 0}) {
		t.Fatalf("out-of-grid press must clamp into region: %+v", c)
	}
	c = FromScreen(r, 4, 5)
	if c != (Cell{Row: 2, Col: 2}) {
		t.Fatalf("in-region cell must translate: %+v", c)
	}
}

func TestSelectionOrdered(t *testing.T) {
	s := Selection{Active: true, Anchor: Cell{2, 5}, Focus: Cell{1, 0}}
	from, to := s.Ordered()
	if from != (Cell{1, 0}) || to != (Cell{2, 5}) {
		t.Fatalf("ordering wrong: %+v %+v", from, to)
	}
	s = Selection{Anchor: Cell{1, 5}, Focus: Cell{1, 2}}
	from, to = s.Ordered()
	if from != (Cell{1, 2}) || to != (Cell{1, 5}) {
		t.Fatalf("same-row ordering wrong: %+v %+v", from, to)
	}
}

func TestHighlightLinesEqualCellsPaintsOneCell(t *testing.T) {
	// to == from must NOT swap (a <= mutant would): paints exactly one
	// cell at the anchor column.
	lines := []string{"abcdef"}
	got := HighlightLines(lines, Cell{Row: 0, Col: 2}, Cell{Row: 0, Col: 2})
	if got[0] != "ab\x1b[7mc\x1b[27mdef" {
		t.Fatalf("equal cells paint one cell: %q", got[0])
	}
}

func TestStreamSelectToRowClampAndEmptyDegenerate(t *testing.T) {
	rows := []string{"abc", "def"}
	// to.Row beyond the end clamps to the last row; the focus column
	// still bounds that row (col+1 inclusive), so this takes "d".
	got := StreamSelect(rows, Cell{Row: 1, Col: 0}, Cell{Row: 9, Col: 0})
	if got != "d" {
		t.Fatalf("clamped to-row must bound at the focus column: %q", got)
	}
	// A focus past the row width takes the clamped row whole.
	got = StreamSelect(rows, Cell{Row: 1, Col: 0}, Cell{Row: 9, Col: 99})
	if got != "def" {
		t.Fatalf("wide focus on clamped row takes it whole: %q", got)
	}
}

func TestHighlightNegativeRowsSkipEntirely(t *testing.T) {
	lines := []string{"zero", "one"}
	got := HighlightLines(lines, Cell{Row: -3, Col: 0}, Cell{Row: -1, Col: 1})
	if got[0] != "zero" || got[1] != "one" {
		t.Fatalf("negative rows paint nothing: %q", got)
	}
}

func TestStreamSelectFromRowNegativeClampsToZero(t *testing.T) {
	rows := []string{"abc", "def"}
	got := StreamSelect(rows, Cell{Row: -5, Col: 0}, Cell{Row: 0, Col: 2})
	if got != "abc" {
		t.Fatalf("negative from.Row clamps to 0: %q", got)
	}
}

func TestClampIntBoundaryValues(t *testing.T) {
	if clampInt(0, 0, 5) != 0 || clampInt(5, 0, 5) != 5 {
		t.Fatal("exact boundary values pass through")
	}
	if clampInt(-1, 0, 5) != 0 {
		t.Fatal("below lo clamps to lo")
	}
	if clampInt(6, 0, 5) != 5 {
		t.Fatal("above hi clamps to hi")
	}
}

func TestHighlightLinesLastRowPainted(t *testing.T) {
	// The to.Row bound must include the final row: a `<` mutant drops it.
	lines := []string{"aaa", "bbb"}
	got := HighlightLines(lines, Cell{Row: 1, Col: 0}, Cell{Row: 1, Col: 2})
	if got[1] != "\x1b[7mbbb\x1b[27m" {
		t.Fatalf("last row must paint whole: %q", got[1])
	}
}

func TestStreamSelectLastRowIncluded(t *testing.T) {
	rows := []string{"aaa", "bbb"}
	got := StreamSelect(rows, Cell{Row: 1, Col: 0}, Cell{Row: 1, Col: 2})
	if got != "bbb" {
		t.Fatalf("to.Row bound must include the last row: %q", got)
	}
}

func TestHighlightZeroWidthSpanSkipsWithoutCorrupting(t *testing.T) {
	// right == left on the focus row skips painting but keeps the row's
	// original bytes (a dropped `continue` would corrupt it).
	lines := []string{"abcdef"}
	got := HighlightLines(lines, Cell{Row: 0, Col: 6}, Cell{Row: 0, Col: 6})
	// col+1 clamps to width, so this paints nothing and leaves the row.
	if got[0] != "abcdef" {
		t.Fatalf("zero-width span at row end must leave the row intact: %q", got[0])
	}
}

func TestHighlightSkipGuardKeepsRowsIntact(t *testing.T) {
	// The `if right <= left { continue }` guard is a defensive skip: the
	// normalized from/to ordering guarantees right > left wherever both
	// land on the same row, and inner rows take the whole line. This
	// pins the observable contract across every span shape: no highlight
	// may ever change the visible text (strip SGR and the row is what it
	// was), and the boundary cases paint exactly one cell or nothing.
	cases := []struct {
		name      string
		from, to  Cell
		wantPaint bool
	}{
		{"single cell", Cell{Row: 0, Col: 2}, Cell{Row: 0, Col: 2}, true},
		{"row end zero width", Cell{Row: 0, Col: 10}, Cell{Row: 0, Col: 10}, false},
		{"reversed pair normalizes", Cell{Row: 0, Col: 4}, Cell{Row: 0, Col: 1}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := []string{"abcdefghij"}
			got := HighlightLines(lines, tc.from, tc.to)
			vis := strings.ReplaceAll(strings.ReplaceAll(got[0], "\x1b[7m", ""), "\x1b[27m", "")
			if vis != "abcdefghij" {
				t.Fatalf("visible text changed: %q", vis)
			}
			if (strings.Contains(got[0], "\x1b[7m")) != tc.wantPaint {
				t.Fatalf("paint=%v want=%v for %q", !tc.wantPaint, tc.wantPaint, got[0])
			}
		})
	}
}

func TestHighlightLinesReversalStrictlyAfterOnly(t *testing.T) {
	lines := []string{"aaaa", "bbbb"}
	// to.Row == from.Row && to.Col == from.Col: NO swap (a <= mutant
	// would swap and paint nothing different here, so pin the multi-row
	// reversed case where the swap is observable).
	fwd := HighlightLines(lines, Cell{Row: 0, Col: 1}, Cell{Row: 1, Col: 2})
	bwd := HighlightLines(lines, Cell{Row: 1, Col: 2}, Cell{Row: 0, Col: 1})
	if fwd[0] != bwd[0] || fwd[1] != bwd[1] {
		t.Fatalf("multi-row reversal must normalize identically: %q vs %q", fwd, bwd)
	}
	// Equal cells on the same row paint exactly one cell.
	eq := HighlightLines(lines, Cell{Row: 0, Col: 1}, Cell{Row: 0, Col: 1})
	if eq[0] != "a\x1b[7ma\x1b[27maa" {
		t.Fatalf("equal cells paint one cell at col 1: %q", eq[0])
	}
}

func TestStreamSelectReversalStrictlyAfterOnly(t *testing.T) {
	rows := []string{"aaaa", "bbbb"}
	fwd := StreamSelect(rows, Cell{Row: 0, Col: 1}, Cell{Row: 1, Col: 2})
	bwd := StreamSelect(rows, Cell{Row: 1, Col: 2}, Cell{Row: 0, Col: 1})
	if fwd != bwd {
		t.Fatalf("reversal must normalize: %q vs %q", fwd, bwd)
	}
}

func TestStreamSelectNegativeFromClampsAndPaintsFirstRow(t *testing.T) {
	rows := []string{"abc", "def"}
	got := StreamSelect(rows, Cell{Row: -1, Col: 0}, Cell{Row: 0, Col: 1})
	if got != "ab" {
		t.Fatalf("from.Row -1 clamps to 0 and paints row 0: %q", got)
	}
}

func TestClampIntExactBoundaries(t *testing.T) {
	if clampInt(0, 0, 5) != 0 || clampInt(5, 0, 5) != 5 {
		t.Fatal("exact boundary values pass through unchanged")
	}
	if clampInt(-1, 0, 5) != 0 || clampInt(6, 0, 5) != 5 {
		t.Fatal("out-of-range values clamp to the edges")
	}
}

func TestStreamSelectNegativeToRowClampsAndEmpty(t *testing.T) {
	rows := []string{"abc"}
	// A selection entirely above the first row clamps to Row 0; the
	// focus column bounds it to one cell.
	got := StreamSelect(rows, Cell{Row: -1, Col: 0}, Cell{Row: 0, Col: 0})
	if got != "a" {
		t.Fatalf("clamped selection must copy the bounded cell: %q", got)
	}
}
