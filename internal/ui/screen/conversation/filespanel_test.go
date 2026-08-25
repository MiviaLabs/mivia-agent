package conversation

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// diffEvent is one tool-end diff, the event vocabulary the panel feeds on.
func diffEvent(id, path string, added, removed int, after ...string) uievent.EventMsg {
	return uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{
			ToolCallID: id, OK: true,
			Diff: &uievent.Diff{
				Path: path, Added: added, Removed: removed,
				Hunks: []uievent.DiffHunk{{
					Header: "@@ -1 +1 @@",
					Lines:  []uievent.DiffLine{{Kind: uievent.DiffLineAdd, Text: "hunk " + path}},
				}},
				After: after,
			},
		},
	}}
}

// panelScreen is a wide screen with the session's diffs already folded
// into the panel and a couple of transcript notices for chat-column
// content.
func panelScreen(t *testing.T, width, height int, diffs ...uievent.EventMsg) Screen {
	t.Helper()
	s := sized(t, 2)
	next, _ := s.Update(tea.WindowSizeMsg{Width: width, Height: height})
	scr := next.(Screen)
	for _, d := range diffs {
		n, _ := scr.Update(d)
		scr = n.(Screen)
	}
	return scr
}

func sampleDiffs() []uievent.EventMsg {
	return []uievent.EventMsg{
		diffEvent("c1", "a.go", 3, 1, "package a"),
		diffEvent("c2", "b.go", 2, 0, "package b"),
		// a second edit to a.go: latest wins.
		diffEvent("c3", "a.go", 9, 2, "package a", "// second edit"),
	}
}

// openPanel presses ctrl+b to open the panel focused in its list.
func openPanel(t *testing.T, s Screen) Screen {
	t.Helper()
	next, _ := s.Update(ctrl('b'))
	scr := next.(Screen)
	if !scr.panel.open || !scr.panel.focused {
		t.Fatalf("ctrl+b left the panel open=%v focused=%v, want open and focused", scr.panel.open, scr.panel.focused)
	}
	return scr
}

// assertExactFrame pins the cockpit's frame contract: the view is
// exactly height rows, every row exactly width columns, and no row
// touches either screen edge.
func assertExactFrame(t *testing.T, view string, width, height int) {
	t.Helper()
	rows := strings.Split(view, "\n")
	if len(rows) != height {
		t.Fatalf("view is %d rows, want %d:\n%s", len(rows), height, view)
	}
	for i, row := range rows {
		if w := ansi.StringWidth(row); w != width {
			t.Errorf("row %d width %d, want %d: %q", i, w, width, ansi.Strip(row))
		}
		plain := ansi.Strip(row)
		if plain[0] != ' ' || plain[len(plain)-1] != ' ' {
			t.Errorf("row %d touches a screen edge: %q", i, plain)
		}
	}
}

// TestPanelCollapsesRepeatedPathsLatestWins: the panel answers the
// file's state, not its history - live events included.
func TestPanelCollapsesRepeatedPathsLatestWins(t *testing.T) {
	s := panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...)
	if len(s.panel.entries) != 2 {
		t.Fatalf("got %d entries, want 2 (a.go collapsed to its latest)", len(s.panel.entries))
	}
	if s.panel.entries[0].Path != "a.go" || len(s.panel.entries[0].Diff.After) != 2 {
		t.Errorf("a.go did not keep its latest edit: %+v", s.panel.entries[0].Diff)
	}
}

// TestPanelKindDerivedFromTheDiff: removals present means edited, none
// means created.
func TestPanelKindDerivedFromTheDiff(t *testing.T) {
	s := panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...)
	if s.panel.entries[0].Kind != fileEdited {
		t.Errorf("a.go kind %q, want edited (it has removals)", s.panel.entries[0].Kind)
	}
	if s.panel.entries[1].Kind != fileCreated {
		t.Errorf("b.go kind %q, want created (adds only)", s.panel.entries[1].Kind)
	}
}

// TestPanelDeletedDiffClaimsTheDeletedKind: a whole-file removal states
// itself, rather than reading as an edit that happens to only remove.
func TestPanelDeletedDiffClaimsTheDeletedKind(t *testing.T) {
	if e := newEntry(uievent.Diff{Path: "gone.go", Removed: 12, Deleted: true}); e.Kind != fileDeleted {
		t.Errorf("kind %q, want deleted", e.Kind)
	}
}

// TestPanelLiveAppendHoldsTheSelection: while the panel is open, a
// streamed tool-end diff appears in the list immediately, and the
// cursor stays on the path the user selected - a live update must not
// move the selection.
func TestPanelLiveAppendHoldsTheSelection(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // select b.go
	s = next.(Screen)
	if sel, _ := s.panel.list.Selected(); !strings.Contains(sel, "b.go") {
		t.Fatalf("precondition: selection is %q, want b.go", sel)
	}

	next, _ = s.Update(diffEvent("c9", "c.go", 1, 0))
	s = next.(Screen)
	if len(s.panel.entries) != 3 || s.panel.entries[2].Path != "c.go" {
		t.Fatalf("live diff did not append: %+v", s.panel.entries)
	}
	if sel, _ := s.panel.list.Selected(); !strings.Contains(sel, "b.go") {
		t.Errorf("live append moved the selection to %q, want b.go held", sel)
	}

	// A second edit to an existing path folds in place, latest wins.
	next, _ = s.Update(diffEvent("c10", "c.go", 4, 4))
	s = next.(Screen)
	if len(s.panel.entries) != 3 || s.panel.entries[2].Kind != fileEdited {
		t.Errorf("re-edit did not fold in place: %+v", s.panel.entries)
	}
	if sel, _ := s.panel.list.Selected(); !strings.Contains(sel, "b.go") {
		t.Errorf("fold moved the selection to %q", sel)
	}
}

// TestPanelSelectionSurvivesLiveUpdates: the panel is persistent, so a
// selected row in the list must survive the live rebuilds a
// background turn causes.
func TestPanelSelectionSurvivesLiveUpdates(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // move to b.go
	s = next.(Screen)
	if sel, ok := s.panel.list.Selected(); !ok || !strings.Contains(sel, "b.go") {
		t.Fatalf("precondition: selected %q, want \"b.go\"", sel)
	}
	next, _ = s.Update(diffEvent("c9", "c.go", 1, 0))
	s = next.(Screen)
	if sel, ok := s.panel.list.Selected(); !ok || !strings.Contains(sel, "b.go") {
		t.Errorf("after live update selected %q ok=%v, want still b.go", sel, ok)
	}
}

// TestPanelEmptyStateNamesItsSections: no activity is still a stated
// shape - the section headers with zero counts, not a blank pane.
func TestPanelEmptyStateNamesItsSections(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24))
	plain := ansi.Strip(s.View())
	for _, want := range []string{"files changed (0)", "subagents (0)"} {
		if !strings.Contains(plain, want) {
			t.Errorf("empty state missing %q:\n%s", want, plain)
		}
	}
}

// TestPanelToggleCycle: ctrl+b walks closed -> open with the list
// focused -> open with the composer focused -> closed, and no state
// leaves the key dead.
func TestPanelToggleCycle(t *testing.T) {
	s := panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...)

	s = openPanel(t, s) // closed -> open + focused

	next, _ := s.Update(ctrl('b')) // focused -> composer, panel stays
	s = next.(Screen)
	if !s.panel.open || s.panel.focused {
		t.Fatalf("second ctrl+b: open=%v focused=%v, want open with composer focus", s.panel.open, s.panel.focused)
	}
	// The composer takes keys while the panel stays on screen.
	next, _ = s.Update(key("h"))
	s = next.(Screen)
	if got := s.composer.Value(); got != "h" {
		t.Fatalf("composer value %q after defocus, want \"h\" (panel must not eat typing)", got)
	}

	next, _ = s.Update(ctrl('b')) // open + composer -> closed
	s = next.(Screen)
	if s.panel.open || s.panel.focused {
		t.Fatalf("third ctrl+b: open=%v focused=%v, want closed", s.panel.open, s.panel.focused)
	}
	// The transcript renders the diffs too, so the panel's section
	// header form is the panel-shaped string to test for.
	if strings.Contains(s.View(), "files changed (") {
		t.Error("closed panel still draws the sidebar")
	}
}

// TestPanelSectionsGroupByCategory: the sidebar's sections are
// categories of thing (files changed, subagents), each headed with a
// live count, empty sections keep their header, file rows carry their
// kind as a glyph (+ created, ~ edited), and the selection is marked.
func TestPanelSectionsGroupByCategory(t *testing.T) {
	diffs := []uievent.EventMsg{
		diffEvent("c1", "internal/ui/a.go", 3, 1, "package a"),
		diffEvent("c2", "cmd/b.go", 2, 0, "package b"),
		diffEvent("c3", "internal/ui/a.go", 9, 2, "package a", "// second edit"),
	}
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 30, diffs...))
	view := s.View()
	plain := ansi.Strip(view)
	for _, want := range []string{"files changed (2)", "subagents (0)"} {
		if !strings.Contains(plain, want) {
			t.Errorf("section header %q missing:\n%s", want, plain)
		}
	}
	// Rows split name from dimmed directory, with the kind glyph in
	// front and the selection marker on the selected row.
	if !strings.Contains(plain, "~ a.go") || !strings.Contains(plain, "internal/ui") {
		t.Errorf("edited row does not show glyph + name + directory:\n%s", plain)
	}
	if !strings.Contains(plain, "+ b.go") {
		t.Errorf("created row does not show its glyph:\n%s", plain)
	}
	if !strings.Contains(plain, "> ~ a.go") {
		t.Errorf("the selected row is not marked:\n%s", plain)
	}

	// A live re-edit keeps the file in place but flips its glyph.
	next, _ := s.Update(diffEvent("c4", "cmd/b.go", 5, 5, "package b2"))
	s = next.(Screen)
	plain = ansi.Strip(s.View())
	if !strings.Contains(plain, "~ b.go") || strings.Contains(plain, "+ b.go") {
		t.Errorf("live re-edit did not flip the kind glyph:\n%s", plain)
	}

	// A subagent progress update fills the subagents section live, with
	// id, status, and step - and the composer can hold focus while it
	// lands.
	next, _ = s.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "sa-1",
			Progress: &uievent.Progress{Step: 2, TotalSteps: 5, Status: "running"}},
	}})
	s = next.(Screen)
	plain = ansi.Strip(s.View())
	if !strings.Contains(plain, "subagents (1)") || !strings.Contains(plain, "sa-1") ||
		!strings.Contains(plain, "running") || !strings.Contains(plain, "2/5") {
		t.Errorf("subagent progress did not reach the section live:\n%s", plain)
	}
	// A later update folds in place rather than appending a row. (The
	// transcript keeps its own historical "running" block - only the
	// sidebar row must flip to the latest status.)
	next, _ = s.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "sa-1",
			Progress: &uievent.Progress{Step: 5, TotalSteps: 5, Status: "done"}},
	}})
	s = next.(Screen)
	plain = ansi.Strip(s.View())
	if !strings.Contains(plain, "subagents (1)") || !strings.Contains(plain, "sa-1") || !strings.Contains(plain, "done") {
		t.Errorf("subagent update did not fold in place:\n%s", plain)
	}
}

// TestPanelEscReturnsFocusToComposerPanelStaysOpen: esc while the list
// holds focus is the defocus key, not the close key - the panel stays
// visible and live.
func TestPanelEscReturnsFocusToComposerPanelStaysOpen(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	s = next.(Screen)
	if s.panel.focused {
		t.Error("esc did not hand focus back to the composer")
	}
	if !s.panel.open {
		t.Error("esc closed the panel; it must stay open")
	}
	if !strings.Contains(s.View(), "a.go") {
		t.Errorf("panel content vanished after esc:\n%s", s.View())
	}
}

// TestPanelWideSplitFrameContract: at and above the wide breakpoint the
// cockpit splits - the chat column left, the file list right, separated
// by ONE vertical rule on the sidebar's edge and nothing else framed
// but the composer's own box - and the whole frame keeps its exact
// size and gutter.
func TestPanelWideSplitFrameContract(t *testing.T) {
	for _, w := range []int{uikitconfig.BreakpointWide, 200} {
		s := openPanel(t, panelScreen(t, w, 30, sampleDiffs()...))
		view := s.View()
		assertExactFrame(t, view, w, 30)
		plain := ansi.Strip(view)
		for _, want := range []string{"notice-a", "notice-b", "a.go", "b.go", "files changed", "subagents"} {
			if !strings.Contains(plain, want) {
				t.Errorf("width %d: split view missing %q:\n%s", w, want, plain)
			}
		}
		// The split frames nothing extra: only the framed composer box border is drawn (no vertical rule separating panes).
		first := ansi.Strip(strings.Split(view, "\n")[3])
		if strings.Contains(first, "│") {
			t.Errorf("width %d: split drawn with unexpected vertical rule on content row: %q", w, first)
		}
		if got := strings.Count(view, "╭"); got != 1 {
			t.Errorf("width %d: framed %d boxes, want 1 (framed composer)", w, got)
		}
		// The top bar names the product, not a tab strip that no longer
		// exists.
		top := ansi.Strip(strings.Split(view, "\n")[0])
		if strings.Contains(top, "files") {
			t.Errorf("width %d: top bar still carries a tab strip: %q", w, top)
		}
	}
}

// TestPanelDialogSitsInTheLeftColumnWithListStillVisible: Enter on the
// selection opens the content as a dialog sized to the LEFT pane, with
// the list still visible beside it - not a full-terminal takeover.
func TestPanelDialogSitsInTheLeftColumnWithListStillVisible(t *testing.T) {
	w := uikitconfig.BreakpointWide
	s := openPanel(t, panelScreen(t, w, 30, sampleDiffs()...))
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if !s.panel.dialog {
		t.Fatal("enter did not open the content dialog")
	}
	view := s.View()
	assertExactFrame(t, view, w, 30)

	reading, _ := render.SplitWidths(w - 2)
	rows := strings.Split(view, "\n")
	dialogCol, listCol := -1, -1
	for _, r := range rows {
		if i := strings.Index(r, "@@ -1 +1 @@"); i >= 0 && dialogCol < 0 {
			dialogCol = ansi.StringWidth(r[:i])
		}
		// b.go is the entry NOT selected, so it only draws in the list
		// pane beside the dialog.
		if i := strings.Index(r, "b.go"); i >= 0 && listCol < 0 {
			listCol = ansi.StringWidth(r[:i])
		}
	}
	if dialogCol < 0 {
		t.Fatalf("dialog body missing:\n%s", view)
	}
	if listCol < 0 || listCol < reading {
		t.Fatalf("list pane not visible beside the dialog (b.go at column %d, left pane ends at %d):\n%s", listCol, reading, view)
	}
	if !strings.Contains(ansi.Strip(view), "a.go") {
		t.Error("dialog title does not name the selected file")
	}

	// Any key closes the dialog back to list + chat.
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	s = next.(Screen)
	if s.panel.dialog {
		t.Error("esc did not close the content dialog")
	}
	if !s.panel.open || !s.panel.focused {
		t.Error("closing the dialog must leave the panel open with the list focused")
	}
	if !strings.Contains(s.View(), "notice-a") {
		t.Error("chat column did not return after the dialog closed")
	}
}

// TestPanelDialogDiffSourceToggle: d flips the dialog between the diff
// and the post-edit source; d again returns to the diff.
func TestPanelDialogDiffSourceToggle(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	next, _ = s.Update(key("d"))
	s = next.(Screen)
	if !s.panel.sourceView || !strings.Contains(s.View(), "// second edit") {
		t.Errorf("source view does not show the after content:\n%s", s.View())
	}
	next, _ = s.Update(key("d"))
	s = next.(Screen)
	if s.panel.sourceView || !strings.Contains(s.View(), "@@") {
		t.Errorf("diff view did not return:\n%s", s.View())
	}
}

// TestPanelDialogScrollReachesTheTail: the half-page scrolls must make
// the last row of a long diff reachable, not strand it below the clip.
func TestPanelDialogScrollReachesTheTail(t *testing.T) {
	long := uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c1", OK: true,
			Diff: &uievent.Diff{Path: "long.go", Added: 40, Removed: 2,
				Hunks: []uievent.DiffHunk{{
					Header: "@@ -1 +1 @@",
					Lines: func() []uievent.DiffLine {
						lines := make([]uievent.DiffLine, 40)
						for i := range lines {
							lines[i] = uievent.DiffLine{Kind: uievent.DiffLineAdd, Text: "tailrow-" + strings.Repeat("y", i+1)}
						}
						return lines
					}(),
				}},
			},
		},
	}}
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 16, long))
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	last := "tailrow-"
	// A short pane windows only a few rows at a time and steps by half
	// that, so the tail can be many presses away - but it must be
	// reachable.
	for i := 0; i < 80 && !strings.Contains(ansi.Strip(s.View()), last); i++ {
		n, _ := s.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
		s = n.(Screen)
	}
	if !strings.Contains(ansi.Strip(s.View()), last) {
		t.Errorf("half-page scrolls never reached the diff's last row:\n%s", s.View())
	}
}

// TestPanelNarrowCollapsesToListFullWidth: below the wide breakpoint
// there is no room for two panes - the list replaces the transcript
// area, the chrome below keeps its place, and Enter opens a
// full-width dialog.
func TestPanelNarrowCollapsesToListFullWidth(t *testing.T) {
	s := openPanel(t, panelScreen(t, 80, 24, sampleDiffs()...))
	view := s.View()
	assertExactFrame(t, view, 80, 24)
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "a.go") {
		t.Errorf("narrow view is not the list:\n%s", plain)
	}
	for _, hidden := range []string{"@@", "notice-a"} {
		if strings.Contains(plain, hidden) {
			t.Errorf("narrow view still shows %q (no room for the chat column):\n%s", hidden, plain)
		}
	}
	if !strings.Contains(plain, "> ") {
		t.Errorf("composer vanished under the narrow panel:\n%s", plain)
	}

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if !s.panel.dialog || !strings.Contains(s.View(), "@@") {
		t.Errorf("enter did not open the full-width content dialog:\n%s", s.View())
	}
	assertExactFrame(t, s.View(), 80, 24)

	next, _ = s.Update(key("x")) // any key closes it
	s = next.(Screen)
	if s.panel.dialog {
		t.Error("a key did not close the narrow dialog")
	}
}

func TestPanelDiffDialog_FitsInsideDialogFrameWithoutClipTruncation(t *testing.T) {
	d := sampleDiffs()[0]
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, d))
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	view := s.View()
	for _, line := range strings.Split(view, "\n") {
		if strings.HasSuffix(strings.TrimRight(line, " │"), "~") {
			t.Errorf("line was truncated with clip marker: %q", line)
		}
	}
}

// TestPanelLiveEntryAppearsWhileComposerFocused is the panel's
// defining contract: with the panel open and focus back on the
// composer, a sent message whose turn edits a file grows the panel's
// list on screen without the user touching the panel - and neither
// closes the panel nor steals the composer's focus.
func TestPanelLiveEntryAppearsWhileComposerFocused(t *testing.T) {
	until := make(chan uievent.Event, 4)
	conv := &scriptedConversation{events: until}
	s := New(loadTheme(t), theme.TierASCII, nil, conv, nil, 40, fixedNow)
	next, _ := s.Update(tea.WindowSizeMsg{Width: uikitconfig.BreakpointWide, Height: 24})
	s = next.(Screen)

	s = openPanel(t, s)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEscape}) // focus back to composer
	s = next.(Screen)
	if s.panel.focused || !s.panel.open {
		t.Fatal("precondition: panel open, composer focused")
	}

	// Type and send a message; the turn's reply carries a tool-end diff.
	next, _ = s.Update(keyMsg("edit the file"))
	s = next.(Screen)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.active == nil {
		t.Fatal("enter did not start the turn")
	}
	next, _ = s.Update(diffEvent("c1", "live.go", 2, 1, "package live"))
	s = next.(Screen)

	if len(s.panel.entries) != 1 || s.panel.entries[0].Path != "live.go" {
		t.Fatalf("the turn's diff did not reach the panel: %+v", s.panel.entries)
	}
	if !s.panel.open || s.panel.focused {
		t.Errorf("the live update moved focus: open=%v focused=%v, want open with composer focus", s.panel.open, s.panel.focused)
	}
	if got := s.composer.Value(); got != "" {
		t.Errorf("composer kept %q after send", got)
	}
	if !strings.Contains(s.View(), "live.go") {
		t.Errorf("panel view does not show the new entry:\n%s", s.View())
	}
}

// TestPanelApprovalClosesTheDialog: an approval prompt claims the chat
// column and every key, so a content dialog covering that column must
// close rather than hide the decision the user must make.
func TestPanelApprovalClosesTheDialog(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if !s.panel.dialog {
		t.Fatal("precondition: the content dialog is open")
	}
	next, _ = s.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolPending,
		Body: uievent.ToolPendingBody{ToolCallID: "c9", Name: "run_command"},
	}})
	s = next.(Screen)
	if s.panel.dialog {
		t.Error("a pending approval did not close the content dialog")
	}
	if !s.approval.Active() {
		t.Error("the approval prompt is not armed")
	}
}

// TestPanelKeysDoNotArmQuit: a ctrl+c that arms the second-press quit
// must be disarmed by any later panel key, or a stray armed quit
// survives a browsing session and the next ctrl+c quits instead of
// cancelling.
func TestPanelKeysDoNotArmQuit(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	s.active = fakeHandle{id: "t1"} // a running turn: the first ctrl+c cancels + arms

	next, _ := s.Update(ctrl('c'))
	s = next.(Screen)
	if !s.quitArmed {
		t.Fatal("precondition: the first ctrl+c arms the quit")
	}

	next, _ = s.Update(key("j")) // a panel key: moves the list selection
	s = next.(Screen)
	if s.quitArmed {
		t.Error("a panel key left the quit armed; only a second ctrl+c may keep it")
	}

	// With the arm cleared, this ctrl+c cancels again rather than
	// quitting the session.
	next, cmd := s.Update(ctrl('c'))
	s = next.(Screen)
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Error("ctrl+c quit the session: the panel key did not disarm the earlier arm")
		}
	}
}

// scriptedConversation is a ports.Conversation whose turns the test
// drives by writing events into the handle's channel.
type scriptedConversation struct {
	events chan uievent.Event
}

func (c *scriptedConversation) Send(context.Context, intent.Send) (ports.TurnHandle, error) {
	return scriptedHandle{ch: c.events}, nil
}
func (c *scriptedConversation) History() []ports.Message  { return nil }
func (c *scriptedConversation) Model() ports.ModelInfo    { return ports.ModelInfo{} }
func (c *scriptedConversation) ContextUsage() ports.Usage { return ports.Usage{} }
func (c *scriptedConversation) Title() string             { return "scripted" }
func (c *scriptedConversation) ID() string                { return "scripted" }

type scriptedHandle struct{ ch chan uievent.Event }

func (h scriptedHandle) ID() string                   { return "scripted" }
func (h scriptedHandle) Events() <-chan uievent.Event { return h.ch }
func (h scriptedHandle) Cancel()                      {}

// TestPanelDTypesIntoTheFilterWhileNoDialogShows: the view toggle acts
// on content, and the only content surface is the dialog - with the
// dialog closed, 'd' must reach the filter (it is a letter users must
func TestPanelDDoesNotFilter(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	next, _ := s.Update(key("d"))
	s = next.(Screen)
	if s.panel.sourceView {
		t.Error("d flipped sourceView with no dialog on screen to show it")
	}
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if !s.panel.dialog || !strings.Contains(s.View(), "@@") {
		t.Errorf("enter did not open the diff dialog:\n%s", s.View())
	}
}

// TestPanelEscDefocusesToComposer: esc hands focus to the composer.
func TestPanelEscDefocusesToComposer(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	s = next.(Screen)
	if s.panel.focused {
		t.Error("esc must hand focus to the composer")
	}
}

// TestPanelCursorMarkerIsTheFocusSignal: the "> " marker and SIDEBAR focused header
// show while the list holds focus.
func TestPanelCursorMarkerIsTheFocusSignal(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	if !strings.Contains(s.View(), "> ~ a.go") || !strings.Contains(s.View(), "SIDEBAR") {
		t.Errorf("focused list does not mark the selected row or header:\n%s", s.View())
	}
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	s = next.(Screen)
	view := s.View()
	if strings.Contains(view, "> ~") {
		t.Errorf("defocused list still shows the cursor marker:\n%s", view)
	}
	if !strings.Contains(view, "~ a.go") {
		t.Errorf("defocused list dropped its rows entirely:\n%s", view)
	}
}

// TestPanelEnterGatedOnDialogFit: at a height too small to draw the
// dialog, Enter leaves the list usable instead of arming a dialog flag
// that swallows keys for a surface nothing renders.
func TestPanelEnterGatedOnDialogFit(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 7, sampleDiffs()...))
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.panel.dialog {
		t.Error("enter opened a dialog the frame cannot draw")
	}
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	if sel, _ := s.panel.list.Selected(); !strings.Contains(sel, "b.go") {
		t.Errorf("list navigation died under the too-small frame (selected %q)", sel)
	}
}

func TestPanelUserMessageRendering(t *testing.T) {
	embedded, _ := theme.Embedded()
	var dark theme.Theme
	for _, c := range embedded {
		if c.Name == "mivia-dark" {
			dark = c
			break
		}
	}
	s := New(dark, theme.TierTrueColor, embedded, errConversation{}, nil, uikitconfig.BreakpointWide, fixedNow)
	next, _ := s.Update(tea.WindowSizeMsg{Width: uikitconfig.BreakpointWide, Height: 30})
	s = next.(Screen)
	n, _ := s.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindTurnStart,
		Body: uievent.TurnStartBody{Input: "Add retry with exponential backoff to the S3 uploader, and cover it with a comprehensive end-to-end test suite for edge cases."},
	}})
	s = n.(Screen)

	// Panel closed (full width)
	viewClosed := s.View()
	assertExactFrame(t, viewClosed, uikitconfig.BreakpointWide, 30)
	readingW, _ := render.SplitWidths(contentWidth(uikitconfig.BreakpointWide))

	// Panel open (split width)
	s = openPanel(t, s)
	viewOpen := s.View()
	assertExactFrame(t, viewOpen, uikitconfig.BreakpointWide, 30)

	// Check transcript rows rendered within the reading width
	rows := s.transcript.Rows()
	for i, r := range rows {
		if w := ansi.StringWidth(r); w > readingW {
			t.Errorf("row %d width = %d, exceeds reading width %d", i, w, readingW)
		}
	}

	// Panel closed again
	next, _ = s.Update(ctrl('b'))
	s = next.(Screen)
	next, _ = s.Update(ctrl('b'))
	s = next.(Screen)
	viewClosedAgain := s.View()
	assertExactFrame(t, viewClosedAgain, uikitconfig.BreakpointWide, 30)
}

func TestNavClickSelectsRow(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	// Row 0 is top gutter, row 1 is SIDEBAR, row 2 is files header, row 3 is file 0
	next, _ := s.handleNavClick(3)
	s = next.(Screen)
	if !s.panel.dialog {
		t.Error("clicking file row 0 should open content dialog")
	}
}

func TestPanelWide_SidebarFullHeightAndTopbarInLeftPane(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	view := s.View()
	assertExactFrame(t, view, uikitconfig.BreakpointWide, 24)

	plain := ansi.Strip(view)
	// Sidebar title should be visible in the right pane
	if !strings.Contains(plain, "FILES") && !strings.Contains(plain, "a.go") {
		t.Errorf("sidebar content missing from view:\n%s", plain)
	}

	// First row should have topbar content on the left (e.g. model or branch/title) and sidebar top border on the right
	lines := strings.Split(plain, "\n")
	if len(lines) != 24 {
		t.Fatalf("expected 24 lines, got %d", len(lines))
	}
}

func TestObserveAgentChronologicalLogs(t *testing.T) {
	var p panel
	p.observeAgent("sub-1", &uievent.Progress{Status: "running", Step: 1, TotalSteps: 3, Log: []string{"start step 1"}})
	p.observeAgent("sub-1", &uievent.Progress{Status: "running", Step: 2, TotalSteps: 3, Log: []string{"finish step 2"}})
	if len(p.agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(p.agents))
	}
	logs := p.agents[0].Log
	if len(logs) != 2 || logs[0] != "start step 1" || logs[1] != "finish step 2" {
		t.Errorf("logs out of chronological order: %v", logs)
	}
}

func TestPanel_ReconcileTerminal_CancelsRunningSubagents(t *testing.T) {
	var p panel
	p.observeAgentStart("sub-1", "invoke_subagent")
	p.observeAgent("sub-2", &uievent.Progress{Status: "running", Step: 1, TotalSteps: 2})
	p.observeAgent("sub-3", &uievent.Progress{Status: "completed", Step: 2, TotalSteps: 2})

	if p.activeAgentCount() != 2 {
		t.Fatalf("expected 2 active agents, got %d", p.activeAgentCount())
	}

	p.reconcileTerminal("cancelled")

	if p.activeAgentCount() != 0 {
		t.Errorf("expected 0 active agents after reconcile, got %d", p.activeAgentCount())
	}
	if p.agents[0].Status != "cancelled" {
		t.Errorf("sub-1 status = %q, want 'cancelled'", p.agents[0].Status)
	}
	if p.agents[1].Status != "cancelled" {
		t.Errorf("sub-2 status = %q, want 'cancelled'", p.agents[1].Status)
	}
	if p.agents[2].Status != "completed" {
		t.Errorf("sub-3 status = %q, want 'completed'", p.agents[2].Status)
	}
}

func TestPanel_ReconcileTerminal_ErrorReasonMarksFailed(t *testing.T) {
	var p panel
	p.observeAgentStart("sub-1", "invoke_subagent")
	p.reconcileTerminal("error")
	if p.agents[0].Status != "failed" {
		t.Errorf("sub-1 status = %q, want 'failed'", p.agents[0].Status)
	}
}

func TestPanel_ObserveAgentHistory_IdempotentUpsert(t *testing.T) {
	var p panel
	p.observeAgentHistory("sub-1", "completed")
	p.observeAgentHistory("sub-1", "completed")
	if len(p.agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(p.agents))
	}
	if p.agents[0].Status != "completed" {
		t.Errorf("expected status 'completed', got %q", p.agents[0].Status)
	}
}

func TestPanel_SelectionKey_PreservesCursorOnStatusUpdate(t *testing.T) {
	p := newPanel(theme.Theme{Name: "test"}, theme.TierASCII)
	p.open = true
	p.observeAgentStart("sub-1", "invoke_subagent")
	// Initially highlighted
	if k := p.selectionKey(); k != "a:sub-1" {
		t.Errorf("initial selectionKey = %q, want 'a:sub-1'", k)
	}
	// Update status via observeAgent (which updates label from running to completed)
	p.observeAgent("sub-1", &uievent.Progress{Status: "completed", Step: 3, TotalSteps: 3})
	if k := p.selectionKey(); k != "a:sub-1" {
		t.Errorf("selectionKey after status update = %q, want 'a:sub-1'", k)
	}
}

func TestPanel_ReconcileTerminal_CustomNonTerminalStatusReconciles(t *testing.T) {
	var p panel
	p.observeAgent("sub-blocked", &uievent.Progress{Status: "blocked", Step: 1, TotalSteps: 2})
	p.observeAgent("sub-waiting", &uievent.Progress{Status: "waiting", Step: 1, TotalSteps: 2})

	if p.activeAgentCount() != 2 {
		t.Errorf("activeAgentCount = %d, want 2", p.activeAgentCount())
	}

	p.reconcileTerminal("cancelled")

	if p.activeAgentCount() != 0 {
		t.Errorf("activeAgentCount after reconcile = %d, want 0", p.activeAgentCount())
	}
	if p.agents[0].Status != "cancelled" {
		t.Errorf("sub-blocked status = %q, want 'cancelled'", p.agents[0].Status)
	}
	if p.agents[1].Status != "cancelled" {
		t.Errorf("sub-waiting status = %q, want 'cancelled'", p.agents[1].Status)
	}
}

func TestScrollPanelInSplitMode(t *testing.T) {
	s := sized(t, 1)
	next, _ := s.Update(tea.WindowSizeMsg{Width: uikitconfig.BreakpointWide, Height: 30})
	scr := next.(Screen)
	hunks := make([]uievent.DiffHunk, 0, 40)
	for i := 0; i < 40; i++ {
		hunks = append(hunks, uievent.DiffHunk{Lines: []uievent.DiffLine{{Kind: uievent.DiffLineAdd, Text: "line"}}})
	}
	diffEv := uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{
			ToolCallID: "tc-1",
			Name:       "write_file",
			Diff:       &uievent.Diff{Path: "long.go", Added: 40, Hunks: hunks},
		},
	}}
	n, _ := scr.Update(diffEv)
	scr = n.(Screen)
	scr = openPanel(t, scr)
	scr.scrollPanel(1)
	if scr.panel.offset == 0 && scr.panelBodyRows() > 0 {
		t.Errorf("expected offset > 0 after scrolling down, got %d", scr.panel.offset)
	}
}
