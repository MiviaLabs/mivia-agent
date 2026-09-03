package conversation

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

// foldScreen is an open sidebar with files and subagents in it.
func foldScreen(t *testing.T, agents int) Screen {
	t.Helper()
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 40, sampleDiffs()...))
	for i := 0; i < agents; i++ {
		s.panel.observeAgentStart(fmt.Sprintf("task-%d", i), fmt.Sprintf("agent-%d", i))
	}
	s.panel.rebindIfOpen()
	return s
}

func sidebar(t *testing.T, s Screen) string {
	t.Helper()
	paneH := max(1, s.contentHeight())
	rows := s.panelRows(s.panelInnerWidth(), max(1, paneH-2))
	for i := range rows {
		rows[i] = ansi.Strip(rows[i])
	}
	return strings.Join(rows, "\n")
}

// TestLeftFoldsTheSelectedSectionAndRightUnfoldsIt is the headline of the
// feature: during a long run the files and subagents lists grow without
// bound and push each other off the pane, and folding one is the only way
// to keep the other in view.
func TestLeftFoldsTheSelectedSectionAndRightUnfoldsIt(t *testing.T) {
	s := foldScreen(t, 3)
	s.panel.selectNavKind(navAgentsHeader, 0)

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	s = next.(Screen)
	view := sidebar(t, s)
	if strings.Contains(view, "agent-0") {
		t.Errorf("left did not fold the subagents section:\n%s", view)
	}
	if !strings.Contains(view, "subagents (3)") {
		t.Errorf("the folded section lost its header, so it cannot be brought back:\n%s", view)
	}
	// The count survives the fold: a folded section still has to say how
	// much is inside, or folding hides the very thing being watched.
	if !strings.Contains(view, "a.go") {
		t.Errorf("folding the subagents took the files with it:\n%s", view)
	}

	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	s = next.(Screen)
	if view := sidebar(t, s); !strings.Contains(view, "agent-0") {
		t.Errorf("right did not unfold the section:\n%s", view)
	}
}

// TestFoldingLeavesTheCursorOnTheHeaderItActedOn: the cursor must stay
// where the user put it. Folding removes rows from the picker's index
// space, so without a rebind that holds the selection by identity the
// cursor would jump to whatever slid into its old index.
func TestFoldingLeavesTheCursorOnTheHeaderItActedOn(t *testing.T) {
	s := foldScreen(t, 3)
	s.panel.selectNavKind(navFilesHeader, 0)

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	s = next.(Screen)
	g, ok := s.panel.navCursor()
	if !ok || g.kind != navFilesHeader {
		t.Fatalf("after folding, the cursor is on %+v, want the files header", g)
	}
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	s = next.(Screen)
	if g, ok := s.panel.navCursor(); !ok || g.kind != navFilesHeader {
		t.Errorf("after unfolding, the cursor is on %+v, want the files header", g)
	}
}

// TestTheContextHeaderFoldsItsBreakdown: the same capability on the
// section that owns the most rows. Folded it keeps the share, because a
// gauge that hides how full the window is has nothing left to say.
func TestTheContextHeaderFoldsItsBreakdown(t *testing.T) {
	s := foldScreen(t, 1)
	s.panel.selectNavKind(navContextHeader, 0)
	if v := sidebar(t, s); !strings.Contains(v, "system") {
		t.Fatalf("precondition: the breakdown is open:\n%s", v)
	}

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	s = next.(Screen)
	view := sidebar(t, s)
	for _, gone := range []string{"system", "messages", "thinking", "this turn"} {
		if strings.Contains(view, gone) {
			t.Errorf("folding context left the %q row behind:\n%s", gone, view)
		}
	}
	if !strings.Contains(view, "context") {
		t.Errorf("the context header itself disappeared:\n%s", view)
	}
	if s.contextSectionRows(40) != 1 {
		t.Errorf("a folded context section claims %d rows, want 1", s.contextSectionRows(40))
	}
}

// TestAFoldedSectionGivesItsRowsToTheOther is why the feature exists at
// all: the rows a fold releases must actually go to the sections still
// open, or folding is decoration.
func TestAFoldedSectionGivesItsRowsToTheOther(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 22, sampleDiffs()...))
	for i := 0; i < 12; i++ {
		s.panel.observeAgentStart(fmt.Sprintf("task-%d", i), fmt.Sprintf("agent-%d", i))
	}
	s.panel.rebindIfOpen()
	// The cursor sits on the subagents header, so the window is anchored
	// there and the rows above it are what a fold can give back.
	s.panel.selectNavKind(navAgentsHeader, 0)
	before := strings.Count(sidebar(t, s), "agent-")

	s.panel.contextCollapsed = true
	s.panel.filesCollapsed = true
	s.panel.rebindIfOpen()
	after := strings.Count(sidebar(t, s), "agent-")

	if after <= before {
		t.Errorf("folding context and files freed no rows for the subagents: %d visible before, %d after", before, after)
	}
}

// TestAnEmptySectionHeaderDoesNotFold: with nothing inside, the header is
// a caption. Left and right on it would be controls that do nothing.
func TestAnEmptySectionHeaderDoesNotFold(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 40, sampleDiffs()...))
	// No subagents have run, so the subagents header is not selectable.
	if s.panel.selectNavKind(navAgentsHeader, 0) {
		t.Error("an empty section's header took the cursor")
	}
	if !strings.Contains(sidebar(t, s), "subagents (0)") {
		t.Error("the empty section lost its caption")
	}
}

// TestClickingASectionHeaderFoldsIt: the header draws the marker, so it
// is the affordance. A marker the mouse cannot work is a control that
// lies about being one - the same rule the transcript's headers follow.
func TestClickingASectionHeaderFoldsIt(t *testing.T) {
	s := foldScreen(t, 3)
	paneH := max(1, s.contentHeight())
	inner := max(1, paneH-2)

	clickRow := -1
	for i, row := range s.panelRows(s.panelInnerWidth(), inner) {
		if strings.Contains(ansi.Strip(row), "subagents (3)") {
			clickRow = i + 1 // handleNavClick consumes one padding row
		}
	}
	if clickRow < 0 {
		t.Fatal("the subagents header draws on no row")
	}
	next, _ := s.handleNavClick(clickRow)
	s = next.(Screen)
	if !s.panel.agentsCollapsed {
		t.Error("clicking the subagents header did not fold it")
	}
	if s.panel.dialog {
		t.Error("clicking a header opened a dialog")
	}
}

// TestEnterOnAHeaderTogglesIt: Enter does the row's own action, whatever
// the row is - open the picker on the model row, fold on a header.
func TestEnterOnAHeaderTogglesIt(t *testing.T) {
	s := foldScreen(t, 2)
	s.panel.selectNavKind(navAgentsHeader, 0)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if !s.panel.agentsCollapsed {
		t.Error("enter on the subagents header did not fold it")
	}
	if s.panel.dialog {
		t.Error("enter on a header opened a dialog")
	}
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.panel.agentsCollapsed {
		t.Error("a second enter did not unfold the section")
	}
}

// TestFoldStateSurvivesLiveUpdates: subagent rows tick constantly during
// a run, and every tick rebinds the list. A fold that a live update
// undoes is a fold the user has to keep re-applying.
func TestFoldStateSurvivesLiveUpdates(t *testing.T) {
	s := foldScreen(t, 2)
	s.panel.selectNavKind(navAgentsHeader, 0)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	s = next.(Screen)

	next, _ = s.Update(diffEvent("c9", "c.go", 1, 0))
	s = next.(Screen)
	if !s.panel.agentsCollapsed {
		t.Error("a live diff unfolded the subagents section")
	}
	if g, ok := s.panel.navCursor(); !ok || g.kind != navAgentsHeader {
		t.Errorf("a live diff moved the cursor off the folded header, onto %+v", g)
	}
}
