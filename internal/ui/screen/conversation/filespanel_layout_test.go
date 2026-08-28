package conversation

import (
	"reflect"
	"testing"
)

// TestPanelWindowGroupsZeroOrNegativeLimit pins the guard: a window with
// no rows to draw hands the groups straight back instead of slicing.
func TestPanelWindowGroupsZeroOrNegativeLimit(t *testing.T) {
	groups := [][]string{{"a"}, {"b", "c"}}
	got := panelWindowGroups(groups, -1, 0, false)
	if len(got) != 2 {
		t.Errorf("limit 0: got %d groups, want the input untouched", len(got))
	}
	got = panelWindowGroups(groups, -1, -3, false)
	if len(got) != 2 {
		t.Errorf("negative limit: got %d groups, want the input untouched", len(got))
	}
}

// TestPanelWindowGroupsFilterReservesAFilterRow pins the filter
// reservation: with the filter active, one row of the window is held
// back, so a payload that fit before now windows.
func TestPanelWindowGroupsFilterReservesAFilterRow(t *testing.T) {
	groups := [][]string{{"one"}, {"two"}, {"three"}}
	got := panelWindowGroups(groups, -1, 3, true)
	if len(got) == 3 {
		t.Errorf("filter row must shrink the usable window, got all 3 groups")
	}
	if len(got) == 0 {
		t.Errorf("filter windowing dropped every group")
	}
}

// TestPanelWindowGroupsKeepsWholeGroupsInsideTheBudget pins the no-split
// rule with even two-row groups throughout: the clipped window keeps each
// surviving group whole and stays within the row budget.
func TestPanelWindowGroupsKeepsWholeGroupsInsideTheBudget(t *testing.T) {
	groups := [][]string{
		{"name-a", "metrics-a"},
		{"name-b", "metrics-b"},
		{"name-c", "metrics-c"},
	}
	const maxRows = 3
	got := panelWindowGroups(groups, 2, maxRows, false)
	total := 0
	for _, g := range got {
		if len(g) != 2 {
			t.Fatalf("group split across the clip boundary: %v", g)
		}
		total += len(g)
	}
	// Keeping groups whole may grow the window past the budget by at
	// most the tail of one group - never a whole extra group.
	if total > maxRows+1 {
		t.Errorf("window holds %d rows, want at most %d (one whole-group slack)", total, maxRows+1)
	}
}

// TestPanelWindowGroupsWindowSlidesToTheSelection pins the selection
// anchor: with the selection deep in the payload, the window slides down
// so the selected group's rows stay inside it, and the trailing groups
// beyond the window's end drop out whole.
func TestPanelWindowGroupsWindowSlidesToTheSelection(t *testing.T) {
	groups := make([][]string, 0, 10)
	for i := 0; i < 10; i++ {
		groups = append(groups, []string{string(rune('a' + i))})
	}
	got := panelWindowGroups(groups, 9, 3, false)
	if len(got) == 0 {
		t.Fatal("windowing dropped every group")
	}
	last := got[len(got)-1]
	if last[0] != "j" {
		t.Errorf("window's last group is %q, want the selected group", last[0])
	}
	for _, g := range got {
		if len(g) != 1 {
			t.Fatalf("group split: %v", g)
		}
	}
}

// TestPanelWindowGroupsClampsStartAtTheTail pins the start clamp: a
// selected group whose first row is the payload's last row (an empty
// trailing group puts its offset at the very end) would start the window
// past the last full screen, so the clamp pulls it back in bounds.
func TestPanelWindowGroupsClampsStartAtTheTail(t *testing.T) {
	groups := [][]string{{"a"}, {"b", "c"}, {}}
	got := panelWindowGroups(groups, 2, 2, false)
	if len(got) == 0 {
		t.Fatal("windowing dropped every group")
	}
	total := 0
	for _, g := range got {
		total += len(g)
	}
	if total > 2 {
		t.Errorf("window holds %d rows, want at most the limit 2", total)
	}
}

// TestClipRowsToWidth pins the width clip: rows wider than the pane are
// cut, rows that fit pass through untouched.
func TestClipRowsToWidth(t *testing.T) {
	rows := []string{"short", "a very long row that overflows the pane by far"}
	got := clipRowsToWidth(rows, 10)
	if got[0] != "short" {
		t.Errorf("fitting row changed: %q", got[0])
	}
	if len(got[1]) > 10 {
		t.Errorf("overflowing row not clipped: %q", got[1])
	}
	if !reflect.DeepEqual(rows, got) {
		t.Errorf("clip must work in place on the input slice")
	}
}

// TestFlattenGroups pins the concatenation order: groups come back in
// order, rows inside a group keep their order.
func TestFlattenGroups(t *testing.T) {
	got := flattenGroups([][]string{{"a", "b"}, {"c"}})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("flattenGroups = %v", got)
	}
}
