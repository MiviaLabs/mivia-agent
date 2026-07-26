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
