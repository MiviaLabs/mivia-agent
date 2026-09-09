package conversation

import "testing"

// planFor builds a nav plan with the given number of files and agents.
func planFor(t *testing.T, files, agents int, contextRows int) ([]navGroup, panel) {
	t.Helper()
	var p panel
	for i := 0; i < files; i++ {
		p.entries = append(p.entries, fileEntry{Path: string(rune('a'+i)) + ".go"})
	}
	for i := 0; i < agents; i++ {
		p.agents = append(p.agents, subagentRow{ID: string(rune('a' + i)), Name: "agent"})
	}
	return p.navGroups(contextRows), p
}

// TestNavPlanMapsEveryGroupToTheRowItSelects pins both directions of the
// map at once. The two used to be separate hand-written arithmetic, and a
// change to the row order had to be made identically in both or clicks
// landed on the wrong row with nothing failing.
func TestNavPlanMapsEveryGroupToTheRowItSelects(t *testing.T) {
	plan, _ := planFor(t, 2, 3, contextSummaryRows)

	seen := 0
	for gIdx, g := range plan {
		idx := navPickerIndex(plan, gIdx)
		if !g.sel {
			if idx != -1 {
				t.Errorf("group %d (kind %d) is not selectable but maps to picker index %d", gIdx, g.kind, idx)
			}
			continue
		}
		if idx != seen {
			t.Errorf("group %d maps to picker index %d, want %d", gIdx, idx, seen)
		}
		// The inverse must return the same group, or a click and a
		// cursor move disagree about where a row is.
		if back := navSelGroup(plan, idx); back != gIdx {
			t.Errorf("picker index %d maps back to group %d, want %d", idx, back, gIdx)
		}
		seen++
	}
	// context header, model, files header, 2 files, subagents header, 3 agents
	if want := 1 + 1 + 1 + 2 + 1 + 3; seen != want {
		t.Errorf("plan has %d selectable rows, want %d", seen, want)
	}
}

// TestNavPlanBoundsReturnNoGroup: a negative picker index (no selection)
// and one past the last row map to no group, and a group index off either
// end of the plan maps to no picker row.
func TestNavPlanBoundsReturnNoGroup(t *testing.T) {
	plan, p := planFor(t, 2, 3, contextSummaryRows)
	last := len(p.navSelectable())
	for _, idx := range []int{-1, last, last + 5} {
		if got := navSelGroup(plan, idx); got != -1 {
			t.Errorf("navSelGroup(%d) = %d, want -1", idx, got)
		}
	}
	for _, gIdx := range []int{-1, len(plan), len(plan) + 10} {
		if got := navPickerIndex(plan, gIdx); got != -1 {
			t.Errorf("navPickerIndex(%d) = %d, want -1", gIdx, got)
		}
	}
}

// TestAnEmptySectionHeaderIsNotSelectable: a fold over nothing does
// nothing when taken and costs a stop on the way past, so an empty
// section's header stays a plain caption.
func TestAnEmptySectionHeaderIsNotSelectable(t *testing.T) {
	plan, _ := planFor(t, 0, 0, contextSummaryRows)
	for gIdx, g := range plan {
		if g.kind.header() && g.kind != navContextHeader && g.sel {
			t.Errorf("group %d: an empty section's header is selectable", gIdx)
		}
	}
	// The context header always folds: its section is never empty.
	if !plan[0].sel || !plan[0].collapsible() {
		t.Error("the context header must always be selectable and foldable")
	}
}

// TestTheSelectableListIsTheSameAtEveryContextHeight is the invariant
// that keeps a terminal resize from moving a picker index out from under
// a selection: the context section grows by adding non-selectable rows,
// so nothing below it shifts in the picker's index space.
func TestTheSelectableListIsTheSameAtEveryContextHeight(t *testing.T) {
	_, p := planFor(t, 2, 3, contextSummaryRows)
	want := p.navSelectable()
	for _, rows := range []int{1, contextSummaryRows, contextDetailRows, contextDetailRows + 1} {
		plan := p.navGroups(rows)
		got := make([]navGroup, 0, len(plan))
		for _, g := range plan {
			if g.sel {
				got = append(got, g)
			}
		}
		if len(got) != len(want) {
			t.Fatalf("contextRows=%d: %d selectable rows, want %d", rows, len(got), len(want))
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("contextRows=%d: selectable row %d is %+v, want %+v", rows, i, got[i], want[i])
			}
		}
	}
}

// TestFoldingASectionRemovesItsRowsFromTheList: a folded section's
// members must leave the picker's index space too, or the cursor can sit
// on a row that is not drawn.
func TestFoldingASectionRemovesItsRowsFromTheList(t *testing.T) {
	_, p := planFor(t, 2, 3, contextSummaryRows)
	full := len(p.navSelectable())

	p.filesCollapsed = true
	if got := len(p.navSelectable()); got != full-2 {
		t.Errorf("folding files left %d selectable rows, want %d", got, full-2)
	}
	p.agentsCollapsed = true
	if got := len(p.navSelectable()); got != full-5 {
		t.Errorf("folding both left %d selectable rows, want %d", got, full-5)
	}
	// The headers themselves stay: they are how the sections come back.
	for _, g := range p.navSelectable() {
		if g.kind == navFile || g.kind == navAgent {
			t.Errorf("a folded section's member row %+v is still selectable", g)
		}
	}
}
