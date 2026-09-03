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
	// Row 0 is top padding, 1-2 the context section, 3 the model header,
	// 4 the model row, 5 the files header, 6 is file 0.
	next, _ := s.handleNavClick(6)
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
	// clickRow 0 is top padding (handleNavClick does clickRow--); then
	// context header, context bar, model header, model row, files header,
	// f0.go, f1.go, subagents header, and two lines per agent from row 9.

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
		{"header: model", 3, false, "", "", 0},
		{"model row (single click)", 4, false, "", "", 0},
		{"header: files changed", 5, false, "", "", 0},
		{"header: subagents", 8, false, "", "", 0},
		{"out-of-range below", 18, false, "", "", 0},
		{"file 0", 6, true, "", "f0.go", 1},
		{"file 1", 7, true, "", "f1.go", 2},
		{"agent-0 name line", 9, true, "agent-0", "", 3},
		{"agent-0 metrics line", 10, true, "agent-0", "", 3},
		{"agent-1 name line", 11, true, "agent-1", "", 4},
		{"agent-1 metrics line", 12, true, "agent-1", "", 4},
		{"agent-2 name line", 13, true, "agent-2", "", 5},
		{"agent-2 metrics line", 14, true, "agent-2", "", 5},
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

func TestNavClick_ScrolledWindow(t *testing.T) {
	// 10 files and 5 agents in a 24-row terminal (content height 22, innerNavH = 20)
	// Groups: context header (1), context bar (1), model header (1), model row (1), files header (1), 10 files (10),
	// subagents header (1), 5 agents (10). Total = 26 lines > 20 maxRows.
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
		// Move cursor to bottom (agent-4, picker cursor index 1 + 10 + 4 = 15)
		s.panel.list.MoveTo(15)
		return s
	}

	// In this scrolled state:
	// Total groups: group 0..20
	// selGroup = 20 (agent-4). selRow = offsets[20] = 24.
	// maxRows = 20. limit = 20. start = selRow - limit + 1 = 5.
	// startGroup = 5 (first 5 groups dropped: context header, context bar, model header, model row, files header).
	// Groups visible in window: group 3 (f00.go) to group 18 (agent-4).
	// Line 0 of window (clickRow 1): f00.go
	// Line 1 of window (clickRow 2): f01.go
	// ...
	// Line 9 of window (clickRow 10): f09.go
	// Line 10 of window (clickRow 11): subagents (5) header
	// Line 11 of window (clickRow 12): agent-0 name
	// Line 12 of window (clickRow 13): agent-0 metrics
	// ...
	// Line 19 of window (clickRow 20): agent-4 metrics

	// Click rendered row for agent-0 name (clickRow 12)
	s1 := newScrolledScreen()
	next, _ := s1.handleNavClick(12)
	res := next.(Screen)
	if !res.panel.dialog || res.panel.dialogAgent != "agent-0" {
		t.Errorf("clickRow 12: dialog = %v, agent = %q; want agent-0", res.panel.dialog, res.panel.dialogAgent)
	}
	if cur := res.panel.list.CursorRow(); cur != 11 { // model row + 10 files + agent 0 = cursor index 11
		t.Errorf("clickRow 12: cursor = %d, want 11", cur)
	}

	// Click rendered row for f09.go (clickRow 10) on the scrolled screen
	s2 := newScrolledScreen()
	next, _ = s2.handleNavClick(10)
	res = next.(Screen)
	if !res.panel.dialog {
		t.Errorf("clickRow 10: expected dialog open")
	}
	entry, ok := res.panel.selected()
	if !ok || entry.Path != "f09.go" {
		t.Errorf("clickRow 10: selected file = %+v, want f09.go", entry)
	}
	if cur := res.panel.list.CursorRow(); cur != 10 {
		t.Errorf("clickRow 10: cursor = %d, want 10", cur)
	}

	// Click rendered row for subagents header (clickRow 11) on the scrolled screen -> should not open
	s3 := newScrolledScreen()
	next, _ = s3.handleNavClick(11)
	res = next.(Screen)
	if res.panel.dialog {
		t.Errorf("clickRow 11 (header) should not open dialog")
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
