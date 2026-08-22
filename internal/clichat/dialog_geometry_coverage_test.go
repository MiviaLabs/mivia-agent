package clichat

// dialog_geometry_coverage_test.go covers the small dialog-layout
// helpers in dialog_geometry.go that the legacytui tests do not
// exercise individually: percentOf, preferredDimension, and the
// pure variant of WrapDisplayRows.

import (
	"reflect"
	"testing"
)

func TestPercentOf(t *testing.T) {
	for _, tc := range []struct {
		n, pct, want int
	}{
		{100, 50, 50},
		{200, 25, 50},
		{0, 50, 0},
		{10, 200, 20}, // clamped
	} {
		if got := percentOf(tc.n, tc.pct); got != tc.want {
			t.Errorf("percentOf(%d, %d) = %d, want %d", tc.n, tc.pct, got, tc.want)
		}
	}
}

func TestPreferredDimension(t *testing.T) {
	// preferredDimension clamps to terminal bounds and falls back to pct.
	for _, tc := range []struct {
		term, preferred, pct, content, want int
	}{
		{100, 200, 50, 80, 200}, // preferred wins
		{100, 50, 50, 80, 50},   // preferred fits, use it
		{100, 0, 50, 80, 50},    // preferred=0, percentOf(100,50)=50
	} {
		if got := preferredDimension(tc.term, tc.preferred, tc.pct, tc.content); got != tc.want {
			t.Errorf("preferredDimension(%d,%d,%d,%d) = %d, want %d",
				tc.term, tc.preferred, tc.pct, tc.content, got, tc.want)
		}
	}
}

func TestWrapDisplayRowsAndJoin(t *testing.T) {
	// Wide lines wrap, narrow lines do not.
	out := WrapDisplayRows([]string{"abc", "hello world"}, 5)
	if len(out) == 0 {
		t.Fatal("WrapDisplayRows must return at least one row")
	}
	// WrapDisplayRowsWithSources returns rows + a per-line source-index
	// array of the same length.
	rows, sources := WrapDisplayRowsWithSources([]string{"abc"}, 5)
	if len(rows) != len(sources) {
		t.Fatalf("len mismatch: rows=%d sources=%d", len(rows), len(sources))
	}
	if !reflect.DeepEqual(rows, []string{"abc"}) {
		t.Errorf("rows = %v, want [abc]", rows)
	}
	// joinDisplayRows is a thin wrapper around strings.Join.
	if got := joinDisplayRows([]string{"a", "b", "c"}); got != "a\nb\nc" {
		t.Errorf("joinDisplayRows = %q", got)
	}
}

func TestDialogRectAndLayout(t *testing.T) {
	// dialogRect / MakeDialogLayout need real prefs - assert
	// the pure-args no-panic path.
	rect := dialogRect(80, 24, DialogPrefs{PreferredW: 60, PreferredH: 20}, 40, 10)
	if rect.W <= 0 || rect.H <= 0 {
		t.Errorf("dialogRect produced non-positive dims: %+v", rect)
	}
	layout := MakeDialogLayout(80, 24, DialogPrefs{PreferredW: 60, PreferredH: 20}, func(innerW int) (int, int) {
		return innerW - 4, 10
	})
	if layout.InnerW <= 0 || layout.PageH <= 0 {
		t.Errorf("MakeDialogLayout produced non-positive inner dims: %+v", layout)
	}
}
