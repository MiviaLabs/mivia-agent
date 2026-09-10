package transcript

import (
	"slices"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// This file is the mouse click-to-toggle router, split out of
// viewport.go: ToggleBlockAtScreenRow and its collapse-marker hit test.
// C4 gave it a second hit target (a tool card's hint row), which is
// enough new logic to earn its own file beside the scroll/layout code
// viewport.go still owns.

// ToggleBlockAtScreenRow opens or closes the block whose HEADER (or, for
// a windowed tool card, whose HINT row) draws on the given viewport row.
// x and y are relative to the transcript's own top-left, the way a mouse
// event reports them. It reports false when the row holds no collapsible
// header or hint, so a click can fall through.
//
// A tool block's hit target is its hint row, not its header: C3 puts the
// call's outcome in column 1 instead of a collapse marker, so C4 moves
// the click target to the card's own "… N more lines" hint row, which is
// itself the only mouse affordance - re-collapsing needs Space/Enter on
// the focused block (focus.go, ToggleFocused). Every other collapsible
// kind keeps the ORIGINAL header-row contract this function has carried
// since before C3/C4: only the header row acts, a body row falls
// through, and closing (not opening) requires the click to land on the
// collapse MARKER - full rationale (the drag-select hazard a plain
// header press would create) in docs/design/wireframes-panes.md section
// 2 (C3/C4 add the tool-block exception above).
//
// Either direction cancels a live selection (selection.go), the same
// rule push/ScrollBy/SetSize already follow. Clicking a coalesced leader
// row (R2) opens the whole run; closing is per-member.
func (m Model) ToggleBlockAtScreenRow(x, y int) (Model, bool) {
	if y < 0 || !m.FocusedRowVisible(y) {
		return m, false
	}
	row := m.offset + y
	spans := m.layout()
	for i := range m.blocks {
		s := spans[i]
		if s.height == 0 || row < s.top || row >= s.top+s.height {
			continue
		}
		if s.runSize > 0 {
			if row != s.top {
				continue
			}
			m.invalidateSelection()
			m.expandRun(i)
			return m, true
		}
		blk := m.blocks[i]
		if !blk.Collapsible {
			return m, false
		}
		if blk.isToolBlock() {
			offset, ok := blk.card(m.width - s.indent).hintOffset()
			if !ok || row != s.top+offset {
				return m, false
			}
			m.invalidateSelection()
			m.blocks = slices.Clone(m.blocks)
			m.blocks[i].Collapsed = false
			m.clampOffset()
			return m, true
		}
		if row != s.top {
			return m, false
		}
		if !blk.Collapsed && !hitsCollapseMarker(x, s.indent) {
			return m, false
		}
		m.invalidateSelection()
		m.blocks = slices.Clone(m.blocks)
		m.blocks[i].Collapsed = !m.blocks[i].Collapsed
		// A reasoning block's third state (Expanded, C1) is a keyboard-only
		// affordance - the mouse toggle only ever flips Collapsed, so it
		// must not leave a stale Expanded=true behind for the next open to
		// pick up. Without this, closing a fully-expanded reasoning block
		// and reopening it with the mouse skips the windowed second state
		// entirely, dumping the full text back with no click having asked
		// for that much.
		if blk.Kind == uievent.KindReasoning {
			m.blocks[i].Expanded = false
		}
		m.clampOffset()
		return m, true
	}
	return m, false
}

// hitsCollapseMarker reports whether column x lands on a header's
// collapse glyph. The marker occupies one column at the block's indent,
// and the space after it is included so a one-column target does not
// have to be hit exactly.
//
// Only the non-tool-block path in ToggleBlockAtScreenRow calls this: a
// tool block's column 1 is its outcome glyph, not a collapse control
// (C3), so its hit target is the card's hint row instead (C4).
func hitsCollapseMarker(x, indent int) bool {
	return x >= indent && x <= indent+1
}
