package cli

import (
	"strings"
	"testing"
	"time"
)

func TestOrderToolIndicesRunningFirst(t *testing.T) {
	t.Parallel()
	rows := []toolRow{
		{Name: "a", Done: true},
		{Name: "b", Done: false},
		{Name: "c", Done: true},
		{Name: "d", Done: false},
	}
	got := orderToolIndices(rows)
	// Running in original order first: b(1), d(3); then done newest-first: c(2), a(0)
	want := []int{1, 3, 2, 0}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order=%v want %v", got, want)
		}
	}
}

func TestOrderToolIndicesDoneRecentFirstAmongMany(t *testing.T) {
	t.Parallel()
	// 20 tools: indices 0..14 done, 15..19 running.
	rows := make([]toolRow, 20)
	for i := range rows {
		rows[i] = toolRow{Name: "t", Done: i < 15}
	}
	got := orderToolIndices(rows)
	if len(got) != 20 {
		t.Fatalf("len=%d want 20", len(got))
	}
	// Running first in original order: 15..19
	for i := 0; i < 5; i++ {
		if got[i] != 15+i {
			t.Fatalf("running[%d]=%d want %d full=%v", i, got[i], 15+i, got)
		}
	}
	// Done most recent first: 14,13,...,0
	for i := 0; i < 15; i++ {
		want := 14 - i
		if got[5+i] != want {
			t.Fatalf("done[%d]=%d want %d full=%v", i, got[5+i], want, got)
		}
	}
}

func TestOrderToolIndicesAllRunningPreservesOrder(t *testing.T) {
	t.Parallel()
	rows := []toolRow{
		{Name: "a"}, {Name: "b"}, {Name: "c"},
	}
	got := orderToolIndices(rows)
	want := []int{0, 1, 2}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestOrderToolIndicesAllDoneRecentFirst(t *testing.T) {
	t.Parallel()
	rows := []toolRow{
		{Name: "a", Done: true},
		{Name: "b", Done: true},
		{Name: "c", Done: true},
	}
	got := orderToolIndices(rows)
	want := []int{2, 1, 0}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestClampToolScrollKeepsSelectedVisible(t *testing.T) {
	t.Parallel()
	ordered := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	maxVis := 6

	// Selected near end should push scroll so it's in window.
	scroll := clampToolScroll(0, 9, ordered, maxVis)
	if scroll != 9-maxVis+1 {
		t.Fatalf("scroll=%d want %d", scroll, 9-maxVis+1)
	}
	// Selected at start keeps scroll 0.
	if got := clampToolScroll(5, 0, ordered, maxVis); got != 0 {
		t.Fatalf("scroll=%d want 0", got)
	}
	// Empty ordered.
	if got := clampToolScroll(3, 1, nil, maxVis); got != 0 {
		t.Fatalf("empty ordered scroll=%d", got)
	}
	// Selected already in window: scroll unchanged (within max).
	if got := clampToolScroll(2, 4, ordered, maxVis); got != 2 {
		// selected pos=4 is in [2, 8); keep 2
		t.Fatalf("in-window scroll=%d want 2", got)
	}
	// Scroll past max clamps.
	if got := clampToolScroll(100, -1, ordered, maxVis); got != 10-maxVis {
		t.Fatalf("maxScroll got=%d want %d", got, 10-maxVis)
	}
	// Negative scroll clamps to 0.
	if got := clampToolScroll(-3, -1, ordered, maxVis); got != 0 {
		t.Fatalf("neg scroll=%d want 0", got)
	}
	// Selected not in ordered list: only bound scroll.
	if got := clampToolScroll(1, 99, ordered, maxVis); got != 1 {
		t.Fatalf("unknown selected scroll=%d want 1", got)
	}
}

func TestClampToolScrollWithTwentyTools(t *testing.T) {
	t.Parallel()
	// ordered as display order (identity for this unit).
	ordered := make([]int, 20)
	for i := range ordered {
		ordered[i] = i
	}
	maxVis := toolMaxVisibleRows
	// Select last toolRows index (19) → scroll so pos 19 visible.
	scroll := clampToolScroll(0, 19, ordered, maxVis)
	if scroll != 19-maxVis+1 {
		t.Fatalf("scroll=%d want %d", scroll, 19-maxVis+1)
	}
	// Window must cover selected.
	if !(scroll <= 19 && 19 < scroll+maxVis) {
		t.Fatalf("selected 19 not in window scroll=%d maxVis=%d", scroll, maxVis)
	}
}

func TestToolPanelReindexOrdersAndClamps(t *testing.T) {
	t.Parallel()
	// 12 rows: 6 done, 6 running. Display order puts running first, then done recent-first.
	// Selected toolRows index 0 lands at the end of ordered (out of the top window).
	rows := make([]toolRow, 12)
	for i := range rows {
		rows[i] = toolRow{Name: "t", Done: i < 6} // 0..5 done, 6..11 running
	}
	st := toolPanelState{Selected: 0, Scroll: 0}
	st.reindex(rows)
	wantOrdered := orderToolIndices(rows)
	if len(st.ordered) != len(wantOrdered) {
		t.Fatalf("ordered len=%d want %d got=%v want=%v", len(st.ordered), len(wantOrdered), st.ordered, wantOrdered)
	}
	for i := range wantOrdered {
		if st.ordered[i] != wantOrdered[i] {
			t.Fatalf("ordered=%v want %v", st.ordered, wantOrdered)
		}
	}
	wantScroll := clampToolScroll(0, 0, wantOrdered, toolMaxVisibleRows)
	if st.Scroll != wantScroll {
		t.Fatalf("Scroll=%d want %d ordered=%v", st.Scroll, wantScroll, st.ordered)
	}
	pos := -1
	for i, idx := range st.ordered {
		if idx == st.Selected {
			pos = i
			break
		}
	}
	if pos < 0 {
		t.Fatalf("selected %d missing from ordered %v", st.Selected, st.ordered)
	}
	if pos < st.Scroll || pos >= st.Scroll+toolMaxVisibleRows {
		t.Fatalf("selected pos %d not in window scroll=%d maxVis=%d", pos, st.Scroll, toolMaxVisibleRows)
	}
}

func TestToolPanelReindexEmptyRows(t *testing.T) {
	t.Parallel()
	st := toolPanelState{Selected: 3, Scroll: 7, ordered: []int{1, 2}}
	st.reindex(nil)
	if len(st.ordered) != 0 {
		t.Fatalf("ordered=%v want empty", st.ordered)
	}
	if st.Scroll != 0 {
		t.Fatalf("Scroll=%d want 0", st.Scroll)
	}
}

func TestToolPanelReindexIdempotent(t *testing.T) {
	t.Parallel()
	rows := []toolRow{
		{Name: "a", Done: true},
		{Name: "b", Done: false},
		{Name: "c", Done: true},
		{Name: "d", Done: false},
		{Name: "e", Done: true},
		{Name: "f", Done: false},
		{Name: "g", Done: true},
		{Name: "h", Done: false},
	}
	st := toolPanelState{Selected: 0, Scroll: 99}
	st.reindex(rows)
	ord1 := append([]int(nil), st.ordered...)
	scroll1 := st.Scroll
	st.reindex(rows)
	if st.Scroll != scroll1 {
		t.Fatalf("second Scroll=%d want %d", st.Scroll, scroll1)
	}
	if len(st.ordered) != len(ord1) {
		t.Fatalf("second ordered len changed: %v vs %v", st.ordered, ord1)
	}
	for i := range ord1 {
		if st.ordered[i] != ord1[i] {
			t.Fatalf("second ordered=%v want %v", st.ordered, ord1)
		}
	}
}

func TestToolPanelReindexMatchesOldIdiom(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		rows     []toolRow
		selected int
		scroll   int
	}{
		{"empty", nil, 0, 5},
		{"all_running", []toolRow{{Name: "a"}, {Name: "b"}, {Name: "c"}}, 2, 0},
		{"all_done", []toolRow{{Name: "a", Done: true}, {Name: "b", Done: true}, {Name: "c", Done: true}}, 0, 10},
		{"mixed_out_of_view", func() []toolRow {
			rows := make([]toolRow, 15)
			for i := range rows {
				rows[i] = toolRow{Name: "t", Done: i%2 == 0}
			}
			return rows
		}(), 0, 0},
		{"selected_none", []toolRow{{Name: "a"}, {Name: "b"}}, -1, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Old idiom
			old := toolPanelState{Selected: tc.selected, Scroll: tc.scroll}
			old.ordered = orderToolIndices(tc.rows)
			old.Scroll = clampToolScroll(old.Scroll, old.Selected, old.ordered, toolMaxVisibleRows)
			// New helper
			got := toolPanelState{Selected: tc.selected, Scroll: tc.scroll}
			got.reindex(tc.rows)
			if got.Scroll != old.Scroll {
				t.Fatalf("Scroll got=%d old=%d", got.Scroll, old.Scroll)
			}
			if len(got.ordered) != len(old.ordered) {
				t.Fatalf("ordered len got=%d old=%d got=%v old=%v", len(got.ordered), len(old.ordered), got.ordered, old.ordered)
			}
			for i := range old.ordered {
				if got.ordered[i] != old.ordered[i] {
					t.Fatalf("ordered got=%v old=%v", got.ordered, old.ordered)
				}
			}
			if got.Selected != tc.selected {
				t.Fatalf("Selected mutated: %d want %d", got.Selected, tc.selected)
			}
		})
	}
}

func TestRenderToolPanelWindowMaxSixRows(t *testing.T) {
	t.Parallel()
	now := time.Now()
	rows := make([]toolRow, 12)
	for i := range rows {
		rows[i] = toolRow{
			Name:  "tool",
			Done:  true,
			Start: now.Add(-time.Second),
			End:   now,
		}
	}
	st := toolPanelState{Selected: -1}
	out, n, st2 := renderToolPanelWindow(rows, 80, now, st, 0, phaseTools, toolMaxVisibleRows, 0)
	if out == "" || n < 1 {
		t.Fatalf("empty panel n=%d", n)
	}
	// Without expand: header + optional hint + at most toolMaxVisibleRows tool lines.
	// Focused=false and len>maxVis → hint line is present.
	lines := strings.Split(out, "\n")
	// Count non-header/hint lines that look like tool rows (have ✓ or ✗ or brand).
	toolLines := 0
	for _, ln := range lines {
		if strings.Contains(ln, "✓") || strings.Contains(ln, "✗") {
			toolLines++
		}
	}
	if toolLines > toolMaxVisibleRows {
		t.Fatalf("toolLines=%d > max %d\nout=%q", toolLines, toolMaxVisibleRows, out)
	}
	if len(st2.visible) > toolMaxVisibleRows {
		t.Fatalf("visible=%d > max", len(st2.visible))
	}
	if len(st2.visible) != toolMaxVisibleRows {
		t.Fatalf("visible=%d want %d", len(st2.visible), toolMaxVisibleRows)
	}
}

func TestRenderToolPanelWindowTwentyToolsMaxSix(t *testing.T) {
	t.Parallel()
	now := time.Now()
	rows := make([]toolRow, 20)
	for i := range rows {
		// Mix done/running so order is non-identity.
		rows[i] = toolRow{
			Name:  "tool",
			Done:  i%3 != 0,
			Start: now.Add(-time.Duration(i) * time.Second),
			End:   now,
		}
	}
	st := toolPanelState{Selected: -1, Scroll: 0}
	out, n, st2 := renderToolPanelWindow(rows, 80, now, st, 0, phaseTools, toolMaxVisibleRows, 0)
	if out == "" || n < 1 {
		t.Fatalf("empty panel n=%d", n)
	}
	if len(st2.ordered) != 20 {
		t.Fatalf("ordered len=%d want 20", len(st2.ordered))
	}
	if len(st2.visible) != toolMaxVisibleRows {
		t.Fatalf("visible=%d want %d", len(st2.visible), toolMaxVisibleRows)
	}
	// Header should advertise window range when > maxVis.
	if !strings.Contains(out, "1–6/20") && !strings.Contains(out, "1-6/20") {
		// en-dash in format string
		if !strings.Contains(out, "/20") {
			t.Fatalf("expected scroll hint for 20 tools, out=%q", out)
		}
	}
	// Every visible entry must be a valid toolRows index from ordered window.
	for _, vi := range st2.visible {
		if vi < 0 || vi >= 20 {
			t.Fatalf("visible index out of range: %d", vi)
		}
	}
	// Scroll deeper: select last done (oldest running may be early).
	// Pin selection to ordered[19] (last display slot) and re-render.
	st3 := toolPanelState{Selected: st2.ordered[19], Scroll: 0}
	_, _, st4 := renderToolPanelWindow(rows, 80, now, st3, 0, phaseTools, toolMaxVisibleRows, 0)
	if len(st4.visible) != toolMaxVisibleRows {
		t.Fatalf("scrolled visible=%d", len(st4.visible))
	}
	// Selected must appear in visible window.
	found := false
	for _, vi := range st4.visible {
		if vi == st4.Selected {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("selected %d not in visible %v scroll=%d", st4.Selected, st4.visible, st4.Scroll)
	}
}

func TestSelectNextBounds(t *testing.T) {
	t.Parallel()
	st := toolPanelState{
		ordered:  []int{1, 0, 2},
		Selected: -1,
	}
	st.selectNext(+1, toolMaxVisibleRows)
	if st.Selected != 1 {
		t.Fatalf("first +1 Selected=%d want 1", st.Selected)
	}
	if !st.Focused {
		t.Fatal("expected Focused")
	}
	st.selectNext(+1, toolMaxVisibleRows)
	if st.Selected != 0 {
		t.Fatalf("second +1 Selected=%d want 0", st.Selected)
	}
	st.selectNext(+1, toolMaxVisibleRows)
	if st.Selected != 2 {
		t.Fatalf("third +1 Selected=%d want 2", st.Selected)
	}
	// At end: clamp, no wrap past.
	st.selectNext(+1, toolMaxVisibleRows)
	if st.Selected != 2 {
		t.Fatalf("clamp +1 Selected=%d want 2", st.Selected)
	}
	// Back to start, clamp at 0.
	st.selectNext(-1, toolMaxVisibleRows)
	st.selectNext(-1, toolMaxVisibleRows)
	st.selectNext(-1, toolMaxVisibleRows)
	if st.Selected != 1 {
		t.Fatalf("clamp -1 Selected=%d want 1 (first in ordered)", st.Selected)
	}
}

func TestSelectNextScrollsWindow(t *testing.T) {
	t.Parallel()
	ordered := make([]int, 20)
	for i := range ordered {
		ordered[i] = i
	}
	st := toolPanelState{
		ordered:  ordered,
		Selected: 0,
		Scroll:   0,
		Focused:  true,
	}
	// Walk forward until selection near end; scroll must follow.
	for i := 0; i < 15; i++ {
		st.selectNext(+1, toolMaxVisibleRows)
	}
	if st.Selected != 15 {
		t.Fatalf("Selected=%d want 15", st.Selected)
	}
	// selected pos=15 must be in [Scroll, Scroll+maxVis)
	if st.Selected < st.Scroll || st.Selected >= st.Scroll+toolMaxVisibleRows {
		t.Fatalf("Selected %d not in window scroll=%d", st.Selected, st.Scroll)
	}
	// From top with Selected=-1 and delta -1: jump to last.
	st2 := toolPanelState{ordered: ordered, Selected: -1}
	st2.selectNext(-1, toolMaxVisibleRows)
	if st2.Selected != 19 {
		t.Fatalf("from none -1 Selected=%d want 19", st2.Selected)
	}
}

func TestScrollWindowDoesNotChangeSelection(t *testing.T) {
	t.Parallel()
	ordered := make([]int, 20)
	for i := range ordered {
		ordered[i] = i
	}
	st := toolPanelState{
		ordered:  ordered,
		Selected: 2, // keep selected near top
		Scroll:   0,
		Focused:  true,
	}
	prevSel := st.Selected
	st.scrollWindow(+3, toolMaxVisibleRows)
	if st.Selected != prevSel {
		t.Fatalf("scrollWindow changed Selected %d → %d", prevSel, st.Selected)
	}
	// clampToolScroll keeps selected visible, so scroll cannot move past
	// leaving selected 2 out of window - max scroll with selected 2 is 2.
	if st.Scroll > 2 {
		t.Fatalf("scroll=%d would hide selected 2 (maxVis=%d)", st.Scroll, toolMaxVisibleRows)
	}
	// Free scroll when no selection.
	st3 := toolPanelState{ordered: ordered, Selected: -1, Scroll: 0}
	st3.scrollWindow(+5, toolMaxVisibleRows)
	if st3.Scroll != 5 {
		t.Fatalf("free scroll=%d want 5", st3.Scroll)
	}
	if st3.Selected != -1 {
		t.Fatalf("Selected should stay -1, got %d", st3.Selected)
	}
	// Empty ordered is no-op.
	st4 := toolPanelState{Selected: 1, Scroll: 2}
	st4.scrollWindow(+1, toolMaxVisibleRows)
	if st4.Scroll != 2 || st4.Selected != 1 {
		t.Fatalf("empty ordered mutated state: %+v", st4)
	}
}

func TestToolIndexAtY(t *testing.T) {
	t.Parallel()
	st := toolPanelState{
		Selected: 2,
		Y0:       10,
		Y1:       20,
		rowY: map[int]int{
			0: 12,
			2: 13,
			5: 14,
		},
	}
	if got := st.toolIndexAtY(13); got != 2 {
		t.Fatalf("at 13 got %d want 2", got)
	}
	if got := st.toolIndexAtY(12); got != 0 {
		t.Fatalf("at 12 got %d want 0", got)
	}
	// Expand body under selected row.
	if got := st.toolIndexAtY(15); got != 2 {
		t.Fatalf("expand body at 15 got %d want 2", got)
	}
	if got := st.toolIndexAtY(9); got != -1 {
		t.Fatalf("outside got %d want -1", got)
	}
	if !st.inPanel(10) || !st.inPanel(20) || st.inPanel(21) {
		t.Fatalf("inPanel range broken Y0=%d Y1=%d", st.Y0, st.Y1)
	}
}

func TestInPanelAndToolIndexAtYFromRender(t *testing.T) {
	t.Parallel()
	now := time.Now()
	rows := make([]toolRow, 8)
	for i := range rows {
		rows[i] = toolRow{
			Name:  "tool",
			Done:  true,
			Start: now.Add(-time.Second),
			End:   now,
		}
	}
	yBase := 5
	st := toolPanelState{Selected: -1}
	_, n, st2 := renderToolPanelWindow(rows, 80, now, st, 0, phaseTools, toolMaxVisibleRows, yBase)
	if n < 2 {
		t.Fatalf("lines=%d", n)
	}
	if st2.Y0 != yBase {
		t.Fatalf("Y0=%d want %d", st2.Y0, yBase)
	}
	if st2.Y1 != yBase+n-1 {
		t.Fatalf("Y1=%d want %d", st2.Y1, yBase+n-1)
	}
	if !st2.inPanel(st2.Y0) || !st2.inPanel(st2.Y1) {
		t.Fatal("inPanel false on edges")
	}
	if st2.inPanel(st2.Y0-1) || st2.inPanel(st2.Y1+1) {
		t.Fatal("inPanel true outside")
	}
	// Hit each painted tool row.
	for ti, ry := range st2.rowY {
		if got := st2.toolIndexAtY(ry); got != ti {
			t.Fatalf("toolIndexAtY(%d)=%d want toolRows %d", ry, got, ti)
		}
	}
	// Header Y (yBase) is in panel but not a tool row (unless expand logic).
	if got := st2.toolIndexAtY(yBase); got != -1 {
		t.Fatalf("header y should not map to tool, got %d", got)
	}
	// Invalid Y1 < Y0.
	bad := toolPanelState{Y0: 10, Y1: 5}
	if bad.inPanel(7) {
		t.Fatal("inPanel true when Y1 < Y0")
	}
	// nil rowY
	empty := toolPanelState{}
	if empty.toolIndexAtY(0) != -1 {
		t.Fatal("nil rowY should return -1")
	}
}
