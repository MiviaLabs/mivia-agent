package conversation

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func TestNavClickSelectsRow(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	// Row 0 is top padding, 1-3 the context section, 4 the model header,
	// 5 the model row, 6 the files header, 7 is file 0.
	next, _ := s.handleNavClick(7)
	s = next.(Screen)
	if !s.panel.dialog {
		t.Error("clicking file row 0 should open content dialog")
	}
}

func TestNavClick_TwoLineAgentRowsUnwindowed(t *testing.T) {
	diffs := []uievent.EventMsg{
		{Event: uievent.Event{Kind: uievent.KindToolEnd, Body: uievent.ToolEndBody{
			Diff: &uievent.Diff{Path: "f0.go", Added: 1},
		}}},
		{Event: uievent.Event{Kind: uievent.KindToolEnd, Body: uievent.ToolEndBody{
			Diff: &uievent.Diff{Path: "f1.go", Added: 1},
		}}},
	}
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, diffs...))
	s.panel.observeAgentStart("agent-0", "agent-0")
	s.panel.observeAgentStart("agent-1", "agent-1")
	s.panel.observeAgentStart("agent-2", "agent-2")

	// Rendered structure (unwindowed, innerNavH = 20):
	// clickRow 0 is top padding (handleNavClick does clickRow--); then the
	// three context rows (header, bar, totals), model header, model row,
	// files header, f0.go, f1.go, subagents header, and two lines per agent
	// from clickRow 10.

	tests := []struct {
		name       string
		clickRow   int
		wantOpen   bool
		wantAgent  string
		wantFile   string
		wantCursor int
	}{
		{"header: context", 1, false, "", "", 0},
		{"context bar", 2, false, "", "", 0},
		{"context totals", 3, false, "", "", 0},
		{"header: model", 4, false, "", "", 0},
		{"model row (single click)", 5, false, "", "", 0},
		{"header: files changed", 6, false, "", "", 0},
		{"header: subagents", 9, false, "", "", 0},
		{"out-of-range below", 19, false, "", "", 0},
		{"file 0", 7, true, "", "f0.go", 1},
		{"file 1", 8, true, "", "f1.go", 2},
		{"agent-0 name line", 10, true, "agent-0", "", 3},
		{"agent-0 metrics line", 11, true, "agent-0", "", 3},
		{"agent-1 name line", 12, true, "agent-1", "", 4},
		{"agent-1 metrics line", 13, true, "agent-1", "", 4},
		{"agent-2 name line", 14, true, "agent-2", "", 5},
		{"agent-2 metrics line", 15, true, "agent-2", "", 5},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scr := s
			next, _ := scr.handleNavClick(tc.clickRow)
			res := next.(Screen)
			if res.panel.dialog != tc.wantOpen {
				t.Fatalf("dialog = %v, want %v", res.panel.dialog, tc.wantOpen)
			}
			if tc.wantOpen {
				if tc.wantAgent != "" && res.panel.dialogAgent != tc.wantAgent {
					t.Errorf("dialogAgent = %q, want %q", res.panel.dialogAgent, tc.wantAgent)
				}
				if tc.wantFile != "" {
					entry, ok := res.panel.selected()
					if !ok || entry.Path != tc.wantFile {
						t.Errorf("selected file = %+v, want %q", entry, tc.wantFile)
					}
				}
				if cur := res.panel.list.CursorRow(); cur != tc.wantCursor {
					t.Errorf("cursor row = %d, want %d", cur, tc.wantCursor)
				}
			}
		})
	}
}

// sidebarRowOf returns the clickRow that handleNavClick must be given to
// hit the rendered sidebar line containing needle.
//
// The row is READ BACK OUT of the rendered sidebar rather than computed
// from the window arithmetic. An earlier version of this test recomputed
// that arithmetic in a comment and asserted the numbers it derived, so it
// agreed with the code by construction and could not see the window
// overrunning its own line limit - which is exactly the defect it was
// meant to cover. handleNavClick consumes one row of top padding before
// indexing the sidebar, hence the +1.
func sidebarRowOf(t *testing.T, s Screen, needle string) int {
	t.Helper()
	paneH := max(1, s.contentHeight())
	inner := max(1, paneH-2)
	for i, row := range s.panelRows(s.panelInnerWidth(), inner) {
		if strings.Contains(ansi.Strip(row), needle) {
			return i + 1
		}
	}
	t.Fatalf("%q draws on no sidebar row", needle)
	return -1
}

func TestNavClick_ScrolledWindow(t *testing.T) {
	// 10 files and 5 agents in a 24-row terminal (content height 22,
	// innerNavH = 20). The list is taller than the pane, so the window
	// scrolls and the click map must follow it.
	var diffs []uievent.EventMsg
	for i := 0; i < 10; i++ {
		diffs = append(diffs, uievent.EventMsg{Event: uievent.Event{Kind: uievent.KindToolEnd, Body: uievent.ToolEndBody{
			Diff: &uievent.Diff{Path: fmt.Sprintf("f%02d.go", i), Added: 1},
		}}})
	}
	newScrolledScreen := func() Screen {
		s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, diffs...))
		for i := 0; i < 5; i++ {
			s.panel.observeAgentStart(fmt.Sprintf("agent-%d", i), fmt.Sprintf("agent-%d", i))
		}
		// The cursor sits on the last agent: model row + 10 files + 4.
		s.panel.list.MoveTo(15)
		return s
	}

	// A subagent row in the scrolled window opens its thread.
	s1 := newScrolledScreen()
	next, _ := s1.handleNavClick(sidebarRowOf(t, s1, "agent-0"))
	res := next.(Screen)
	if !res.panel.dialog || res.panel.dialogAgent != "agent-0" {
		t.Errorf("dialog = %v, agent = %q; want agent-0", res.panel.dialog, res.panel.dialogAgent)
	}
	if cur := res.panel.list.CursorRow(); cur != 11 { // model row + 10 files + agent 0
		t.Errorf("cursor = %d, want 11", cur)
	}

	// A file row in the same window opens its diff.
	s2 := newScrolledScreen()
	next, _ = s2.handleNavClick(sidebarRowOf(t, s2, "f09.go"))
	res = next.(Screen)
	if !res.panel.dialog {
		t.Error("clicking a file row did not open its dialog")
	}
	entry, ok := res.panel.selected()
	if !ok || entry.Path != "f09.go" {
		t.Errorf("selected file = %+v, want f09.go", entry)
	}
	if cur := res.panel.list.CursorRow(); cur != 10 {
		t.Errorf("cursor = %d, want 10", cur)
	}

	// A section header selects nothing and opens nothing.
	s3 := newScrolledScreen()
	next, _ = s3.handleNavClick(sidebarRowOf(t, s3, "subagents ("))
	if next.(Screen).panel.dialog {
		t.Error("clicking a section header opened a dialog")
	}
}

// TestTheSidebarWindowNeverOutgrowsItsPane is the discriminator for the
// window's line budget. It used to pick a line range and then widen it
// outwards to whole groups at both ends, so a two-line agent group
// straddling a boundary pushed the window past the pane. The caller clips
// the overflow off the BOTTOM, so what was lost was the selected agent's
// own rows - and with twenty agents the selection left the pane entirely.
func TestTheSidebarWindowNeverOutgrowsItsPane(t *testing.T) {
	for _, agents := range []int{1, 2, 5, 8, 20} {
		for _, height := range []int{12, 16, 20, 24, 30, 40} {
			s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, height, sampleDiffs()...))
			for i := 0; i < agents; i++ {
				s.panel.observeAgentStart(fmt.Sprintf("task-%02d", i), fmt.Sprintf("agent-%02d", i))
				s.panel.agents[i].ToolCalls = 7 + i
			}
			s.panel.rebindIfOpen()
			s.panel.list.MoveTo(len(s.panel.rowLabels()) - 1)

			paneH := max(1, s.contentHeight())
			inner := max(1, paneH-2)
			rows := s.panelRows(s.panelInnerWidth(), inner)
			if len(rows) > inner {
				t.Errorf("agents=%d height=%d: sidebar drew %d rows into a %d-row pane",
					agents, height, len(rows), inner)
			}

			// The selected agent's name AND its metrics line must both
			// survive the frame's clip: an elapsed time nobody can see is
			// the same as no elapsed time.
			frame := ansi.Strip(strings.Join(s.panelFrameRows(), "\n"))
			name := fmt.Sprintf("agent-%02d", agents-1)
			metrics := fmt.Sprintf("%d tools", 7+agents-1)
			if !strings.Contains(frame, name) {
				t.Errorf("agents=%d height=%d: the selected agent %q is off the pane", agents, height, name)
			}
			if !strings.Contains(frame, metrics) {
				t.Errorf("agents=%d height=%d: the selected agent's metrics line (%q) was cut", agents, height, metrics)
			}
		}
	}
}

// TestNavClickThroughHandleClickSelectsTheClickedRow pins the caller-side
// translation end to end: a left click at real screen coordinates on the
// row that renders b.go must land the picker cursor on b.go and open its
// dialog. handleClick is the only screen-y -> clickRow translation layer
// the direct-handleNavClick tests above never exercise, so an off-by-one
// at a mouse.go call site is invisible to them.
func TestNavClickThroughHandleClickSelectsTheClickedRow(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	reading, _ := render.SplitWidths(contentWidth(uikitconfig.BreakpointWide))
	x := reading + 2 // a column inside the nav pane, past the divider

	// Anchor on rendered text, not arithmetic: the files header row, then
	// file 0 (a.go) one row below it, file 1 (b.go) two rows below it.
	rows := strings.Split(ansi.Strip(s.View()), "\n")
	headerRow := -1
	for i, row := range rows {
		if strings.Contains(row, "files changed (") {
			headerRow = i
			break
		}
	}
	if headerRow < 0 {
		t.Fatalf("files header never rendered; view rows: %d", len(rows))
	}
	bRow := headerRow + 2

	next, _ := s.Update(leftClick(x, bRow))
	s = next.(Screen)
	if !s.panel.dialog {
		t.Fatalf("click at screen row %d opened no dialog", bRow)
	}
	if got := s.panel.list.CursorRow(); got != 2 {
		t.Errorf("click on the rendered b.go row left the cursor at %d, want 2 (b.go, after the model row and a.go)", got)
	}
}
