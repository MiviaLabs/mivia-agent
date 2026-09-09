package conversation

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
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

// TestTheWindowFitsFromEveryCursorPosition sweeps the anchor, which the
// named regression test does not: it only ever selects the LAST row, so
// the window's downward-growth guard is never exercised and the upward
// one only at even limits. Removing either guard passed the whole suite.
func TestTheWindowFitsFromEveryCursorPosition(t *testing.T) {
	s := foldScreen(t, 8)
	rows := len(s.panel.navSelectable())
	for cur := 0; cur < rows; cur++ {
		s.panel.list.MoveTo(cur)
		for maxRows := 2; maxRows <= 40; maxRows++ {
			if n := len(s.panelRows(60, maxRows)); n > maxRows {
				t.Fatalf("cursor %d, maxRows %d: sidebar drew %d rows", cur, maxRows, n)
			}
		}
	}
}

// TestFoldingRebindsThePickerSoEveryCursorRowSelects: folding removes
// rows from the picker's list, and picker.Update clamps Down against its
// own item count. Without the rebind inside setSectionCollapsed the list
// keeps the pre-fold count, so arrowing down walks the cursor onto rows
// that select nothing and where Enter does nothing.
func TestFoldingRebindsThePickerSoEveryCursorRowSelects(t *testing.T) {
	s := foldScreen(t, 2)
	s.panel.selectNavKind(navFilesHeader, 0)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	s = next.(Screen)

	for i := 0; i < 12; i++ {
		next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
		if _, ok := s.panel.navCursor(); !ok {
			t.Fatalf("after %d downs the cursor (row %d) selects nothing; the picker was not rebound",
				i+1, s.panel.list.CursorRow())
		}
	}
}

// TestTheContextSectionDrawsExactlyTheRowsItClaims. panelRows consumes
// the context block through a closure, so a mismatch between the claimed
// height and the rendered rows does not shift the click map - it
// silently DROPS the last row of the breakdown instead. Deleting the
// capped-budget row bump passed the entire suite while losing "this
// turn" from every capped session.
func TestTheContextSectionDrawsExactlyTheRowsItClaims(t *testing.T) {
	for _, capped := range []bool{false, true} {
		for _, collapsed := range []bool{false, true} {
			s := foldScreen(t, 1)
			s.panel.contextCollapsed = collapsed
			window := int64(400_000)
			declared := window
			if capped {
				declared = 1_000_000
			}
			s.topbar.SetSession(
				ports.ModelInfo{Name: "m", ContextWindow: window, DeclaredWindow: declared},
				ports.Usage{InputTokens: 10_000})
			for _, maxRows := range []int{0, 1, 4, 10, 23, 24, 40, 120} {
				claimed := s.contextSectionRows(maxRows)
				drawn := len(s.panelContextRows(60, maxRows))
				if claimed != drawn {
					t.Errorf("capped=%v collapsed=%v maxRows=%d: claims %d rows, draws %d",
						capped, collapsed, maxRows, claimed, drawn)
				}
			}
		}
	}
}

// TestAHeldAgentRowSurvivesAFileArrivingAboveIt is the symmetric twin of
// the bug selKey exists for, and the case the fold tests missed: the
// cursor on a subagent row while a new FILE arrives above the whole
// subagents section.
func TestAHeldAgentRowSurvivesAFileArrivingAboveIt(t *testing.T) {
	s := foldScreen(t, 3)
	s.panel.selectNavKind(navAgent, 1)
	want := s.panel.selectionKey()
	if want == "" {
		t.Fatal("precondition: an agent row is selected")
	}

	next, _ := s.Update(diffEvent("c9", "zz-new.go", 1, 0))
	s = next.(Screen)
	if got := s.panel.selectionKey(); got != want {
		t.Errorf("a file arriving above moved the selection from %q to %q", want, got)
	}
}

// TestAHeldFileRowSurvivesAFileArrivingAboveIt: the same for a file row,
// the other half of the key map that nothing pinned.
func TestAHeldFileRowSurvivesAFileArrivingAboveIt(t *testing.T) {
	s := foldScreen(t, 1)
	s.panel.selectNavKind(navFile, 1)
	want := s.panel.selectionKey()

	next, _ := s.Update(diffEvent("c9", "aaa-first.go", 1, 0))
	s = next.(Screen)
	if got := s.panel.selectionKey(); got != want {
		t.Errorf("a file arriving above moved the selection from %q to %q", want, got)
	}
}

// TestTheHintStatesWhatTheSelectedRowActuallyDoes: "enter:view" is false
// on a section header, where Enter folds. ux-rules 1.4 requires a hint to
// state the complete truth, and the fold keys otherwise had no
// advertisement at all beyond the marker glyph.
func TestTheHintStatesWhatTheSelectedRowActuallyDoes(t *testing.T) {
	s := foldScreen(t, 2)

	s.panel.selectNavKind(navFile, 0)
	if got := ansi.Strip(s.panelFocusedHints(80)); !strings.Contains(got, "enter:view") {
		t.Errorf("on a file row the hint is %q, want it to name enter:view", got)
	}

	s.panel.selectNavKind(navAgentsHeader, 0)
	got := ansi.Strip(s.panelFocusedHints(80))
	if strings.Contains(got, "enter:view") {
		t.Errorf("on a header the hint still claims enter:view: %q", got)
	}
	for _, want := range []string{"←/→:fold", "enter:toggle"} {
		if !strings.Contains(got, want) {
			t.Errorf("header hint %q does not name %q", got, want)
		}
	}
}
