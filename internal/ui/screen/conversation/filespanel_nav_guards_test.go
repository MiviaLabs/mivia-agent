package conversation

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// The nav model's guard arms. Each is the branch that decides a key does
// NOTHING, which is exactly the kind of arm that rots unnoticed: a guard
// that stops guarding does not fail, it just starts acting on rows it
// should have left alone.

// TestNavAtRefusesAnOutOfRangeIndex: the picker's cursor can outrun the
// list between a mutation and its rebind, and every caller of navCursor
// treats "no group" as "do nothing".
func TestNavAtRefusesAnOutOfRangeIndex(t *testing.T) {
	s := foldScreen(t, 2)
	n := len(s.panel.navSelectable())
	for _, idx := range []int{-1, n, n + 7} {
		if g, ok := s.panel.navAt(idx); ok {
			t.Errorf("navAt(%d) returned %+v, want no group", idx, g)
		}
	}
}

// TestFoldKeysDoNothingOffAHeader: left and right are consumed by the
// panel, so if they stopped being guarded they would silently fold
// whatever section the cursor happened to sit inside.
func TestFoldKeysDoNothingOffAHeader(t *testing.T) {
	for _, kind := range []navKind{navModel, navFile, navAgent} {
		s := foldScreen(t, 2)
		if !s.panel.selectNavKind(kind, 0) {
			t.Fatalf("kind %d has no row to select", kind)
		}
		before := [3]bool{s.panel.contextCollapsed, s.panel.filesCollapsed, s.panel.agentsCollapsed}

		if s.panel.setSectionCollapsed(true) {
			t.Errorf("kind %d: left reported a fold on a row that is not a header", kind)
		}
		if s.panel.toggleSection() {
			t.Errorf("kind %d: enter reported a toggle on a row that is not a header", kind)
		}
		after := [3]bool{s.panel.contextCollapsed, s.panel.filesCollapsed, s.panel.agentsCollapsed}
		if before != after {
			t.Errorf("kind %d: a non-header row folded a section anyway: %v -> %v", kind, before, after)
		}
	}
}

// TestFoldingAnAlreadyFoldedSectionIsANoOp: right on an open section and
// left on a closed one report false, so the key is not swallowed for a
// state change that did not happen.
func TestFoldingAnAlreadyFoldedSectionIsANoOp(t *testing.T) {
	s := foldScreen(t, 2)
	s.panel.selectNavKind(navAgentsHeader, 0)

	if s.panel.setSectionCollapsed(false) {
		t.Error("unfolding an already-open section reported a change")
	}
	if !s.panel.setSectionCollapsed(true) {
		t.Fatal("folding an open section reported no change")
	}
	if s.panel.setSectionCollapsed(true) {
		t.Error("folding an already-folded section reported a change")
	}
}

// TestNavKeyOfRefusesAnOutOfRangeEntry: g.at indexes the filtered lists,
// and a group built against a longer list than the caller passes would
// otherwise index out of bounds. The key is empty instead, which
// rebindIfOpen reads as "this row is gone".
func TestNavKeyOfRefusesAnOutOfRangeEntry(t *testing.T) {
	for _, kind := range []navKind{navFile, navAgent} {
		g := navGroup{kind: kind, lines: 1, at: 9, sel: true}
		if got := navKeyOf(g, nil, nil); got != "" {
			t.Errorf("kind %d with at=9 over empty lists returned %q, want \"\"", kind, got)
		}
		if got := navKeyOf(navGroup{kind: kind, at: -1, sel: true}, nil, nil); got != "" {
			t.Errorf("kind %d with at=-1 returned %q, want \"\"", kind, got)
		}
	}
}

// TestASelectionWhoseRowDisappearsFallsBackToTheClamp: a held file can be
// dropped from the panel entirely (a session reset). The rebind cannot
// find it, so whatever the picker's clamp left under the cursor becomes
// the new selection - and must be recorded as such, or the NEXT rebind
// would still be hunting for the row that went away.
func TestASelectionWhoseRowDisappearsFallsBackToTheClamp(t *testing.T) {
	s := foldScreen(t, 2)
	s.panel.selectNavKind(navFile, 1)
	gone := s.panel.selectionKey()
	if gone == "" {
		t.Fatal("precondition: a file row is selected")
	}

	s.panel.entries = nil // the files are gone
	s.panel.rebindIfOpen()

	if s.panel.selKey == gone {
		t.Errorf("the rebind still holds %q, a row that no longer exists", gone)
	}
	if s.panel.selKey != s.panel.selectionKey() {
		t.Errorf("the recorded key %q does not match what the cursor is on (%q)",
			s.panel.selKey, s.panel.selectionKey())
	}
}

// TestEnterOnAModelRowStillOpensThePicker: the fold interception runs
// before the picker sees Enter, so the row that is NOT a header must
// still get its own action.
func TestEnterOnAModelRowStillOpensThePicker(t *testing.T) {
	s := foldScreen(t, 1)
	s.panel.selectNavKind(navModel, 0)
	next, _, handled := s.handlePanelListKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !handled {
		t.Fatal("the panel did not handle Enter")
	}
	if scr, ok := next.(Screen); ok && scr.panel.contextCollapsed {
		t.Error("Enter on the model row folded the context section")
	}
}

// TestFoldKeysDoNothingOnAStaleCursor: between a mutation and its rebind
// the picker's cursor can point past the end of the nav plan - rows were
// removed and the list has not caught up. The fold keys are consumed by
// the panel, so an unguarded lookup would act on a group that no longer
// exists.
func TestFoldKeysDoNothingOnAStaleCursor(t *testing.T) {
	s := foldScreen(t, 3)
	s.panel.selectNavKind(navAgent, 2) // the last row

	// The agents go away without a rebind: the cursor now indexes past
	// the plan.
	s.panel.agents = nil
	if _, ok := s.panel.navCursor(); ok {
		t.Fatal("precondition: the cursor should now point past the plan")
	}
	before := [3]bool{s.panel.contextCollapsed, s.panel.filesCollapsed, s.panel.agentsCollapsed}

	if s.panel.setSectionCollapsed(true) {
		t.Error("setSectionCollapsed acted on a cursor past the end of the plan")
	}
	if s.panel.toggleSection() {
		t.Error("toggleSection acted on a cursor past the end of the plan")
	}
	if after := [3]bool{s.panel.contextCollapsed, s.panel.filesCollapsed, s.panel.agentsCollapsed}; before != after {
		t.Errorf("a stale cursor folded a section: %v -> %v", before, after)
	}
}

// TestOpeningAPanelWithNoHeldRowRecordsWhateverItLandsOn: the very first
// rebind has no key to hold, so it must record what the cursor ended up
// on. Leaving selKey empty would send the NEXT rebind hunting for a row
// that was never named.
func TestOpeningAPanelWithNoHeldRowRecordsWhateverItLandsOn(t *testing.T) {
	p := newPanel(theme.Theme{Name: "test"}, theme.TierASCII)
	p.appendLive(uievent.Diff{Path: "a.go", Added: 1})
	if p.selKey != "" {
		t.Fatal("precondition: a fresh panel holds no row")
	}

	p.openPanel()
	if p.selKey == "" {
		t.Error("opening the panel recorded no selection; the next rebind has nothing to hold")
	}
	if p.selKey != p.selectionKey() {
		t.Errorf("recorded %q but the cursor is on %q", p.selKey, p.selectionKey())
	}
}

// TestARebindWithNothingToHoldStillRecordsWhereItLanded: selKey is empty
// on a panel that has never recorded a move, and selectionKey answers
// nothing when the cursor is stale. With no key to hunt for, the rebind
// still has to name what the clamp left under the cursor - otherwise the
// NEXT rebind is also holding nothing, and the selection is free to drift
// on every live update from then on.
func TestARebindWithNothingToHoldStillRecordsWhereItLanded(t *testing.T) {
	s := foldScreen(t, 3)
	s.panel.selectNavKind(navAgent, 2)
	s.panel.selKey = ""  // never recorded
	s.panel.agents = nil // and the cursor now points past the plan

	if s.panel.selectionKey() != "" {
		t.Fatal("precondition: there is nothing for the rebind to hold")
	}
	s.panel.rebindIfOpen()

	if s.panel.selKey == "" {
		t.Error("the rebind recorded nothing; the next one will have nothing to hold either")
	}
	if s.panel.selKey != s.panel.selectionKey() {
		t.Errorf("recorded %q but the cursor is on %q", s.panel.selKey, s.panel.selectionKey())
	}
}
