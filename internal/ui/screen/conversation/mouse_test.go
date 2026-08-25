package conversation

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/composer"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/picker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// toolScreen returns a screen with one collapsed tool-output block, the
// click target, measured at 80x24.
func toolScreen(t *testing.T) Screen {
	t.Helper()
	s := sized(t, 0)
	next, _ := s.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{
			ToolCallID: "call-1",
			Chunk: "line one\nline two\nline three\nline four\nline five\n" +
				"line six\nline seven\nline eight\nline nine\nline ten\n" +
				"line eleven\nline twelve\nline thirteen\nline fourteen",
		},
	}})
	s = next.(Screen)
	next, _ = s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return next.(Screen)
}

func leftClick(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}

// TestClickCollapsedBlockExpandsIt pins the primary mouse action: a
// click on a collapsed block's header row expands that block.
func TestClickCollapsedBlockExpandsIt(t *testing.T) {
	s := toolScreen(t)
	blocks := s.transcript.Blocks()
	if len(blocks) != 1 || !blocks[0].Collapsed {
		t.Fatalf("precondition: one collapsed tool block, got %+v", blocks)
	}

	// The conversation is shorter than the viewport, so the block's
	// header sits at transcript row 0 - screen row topRow, below top gutter,
	// top bar and its margin row.
	topRow := 1 + s.topbar.Height() + 1
	next, _ := s.Update(leftClick(3, topRow))
	s = next.(Screen)
	if got := s.transcript.Blocks()[0].Collapsed; got {
		t.Error("a click on the collapsed header must expand the block")
	}
}

// TestClickExpandedBodyDoesNothing: only a collapsed header is a target.
func TestClickExpandedBodyDoesNothing(t *testing.T) {
	s := toolScreen(t)
	topRow := 1 + s.topbar.Height() + 1
	next, _ := s.Update(leftClick(3, topRow))
	s = next.(Screen) // now expanded

	next, _ = s.Update(leftClick(6, topRow+2)) // a body row
	s = next.(Screen)
	if !s.transcript.Following() {
		t.Error("a body click must not change scroll state")
	}
}

// TestClickComposerPositionsCursor: a click in the input row places the
// cursor at the clicked column, past the two-column prompt.
func TestClickComposerPositionsCursor(t *testing.T) {
	s := sized(t, 0)
	next, _ := s.Update(keyMsg("hello"))
	s = next.(Screen)
	next, _ = s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = next.(Screen)

	// The input sits framed above the status row and bottom gutter:
	// inputRow is 24 - 1(bottom gutter) - 1(status) - 2(composer bottom border + input row) = 20.
	inputRow := 24 - 1 - 1 - 2
	next, _ = s.Update(leftClick(1+2+2+3, inputRow)) // column 8 on screen == column 5 in input == after "hel"
	s = next.(Screen)
	if got := s.composer.Value(); got != "hello" {
		t.Fatalf("value changed to %q; a click must not edit", got)
	}
	// Type one character: it must land after "hel", proving the cursor
	// moved, without poking textinput internals.
	next, _ = s.Update(keyMsg("X"))
	s = next.(Screen)
	if got := s.composer.Value(); got != "helXlo" {
		t.Errorf("after clicking column 5 and typing, value is %q, want helXlo", got)
	}
}

// TestClickCompletionRowAcceptsIt: a click on a menu row accepts that
// row, not only the highlighted one.
func TestClickCompletionRowAcceptsIt(t *testing.T) {
	s := sized(t, 0)
	s.SetCommands([]composer.Command{
		{Name: "agent", Desc: "pick the agent"},
		{Name: "agents", Desc: "list agents"},
	})
	next, _ := s.Update(keyMsg("/a"))
	s = next.(Screen)
	next, _ = s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = next.(Screen)
	if !s.composer.MenuActive() {
		t.Fatal("precondition: the menu is open")
	}

	// Layout at height 24: bottom gutter 23, status row 22, composer bottom border 21,
	// composer input line 20, composer top border 19,
	// menu rows 17-18 (2 rows directly above composer frame).
	next, _ = s.Update(leftClick(4, 18)) // second menu row: "agents"
	s = next.(Screen)
	if s.composer.MenuActive() {
		t.Error("clicking a row must accept it and close the menu")
	}
	if got := s.composer.Value(); got != "/agents" {
		t.Errorf("accepted %q, want /agents", got)
	}
}

// TestClickOutsideLeftButtonIsIgnored: right and middle clicks carry no
// action.
func TestClickOutsideLeftButtonIsIgnored(t *testing.T) {
	s := toolScreen(t)
	before := s.transcript.Blocks()
	next, _ := s.Update(tea.MouseClickMsg{X: 3, Y: 0, Button: tea.MouseRight})
	s = next.(Screen)
	after := s.transcript.Blocks()
	if after[0].Collapsed != before[0].Collapsed {
		t.Error("a right click must not expand a block")
	}
}

// TestClickReleaseDoesNothing: the release half of a click carries no
// action (there is no drag selection to complete).
func TestClickReleaseDoesNothing(t *testing.T) {
	s := toolScreen(t)
	next, _ := s.Update(tea.MouseReleaseMsg{X: 3, Y: 0, Button: tea.MouseLeft})
	s = next.(Screen)
	if !s.transcript.Blocks()[0].Collapsed {
		t.Error("a release must not expand a block")
	}
}

// TestStatusRowStatesNewBlockCount pins rule 6.7: the jump-to-bottom
// affordance states how many blocks arrived while the reader was
// paused, and does not show the count while following.
func TestStatusRowStatesNewBlockCount(t *testing.T) {
	s := sized(t, 0)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = next.(Screen)

	// Pause, then let blocks arrive.
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyHome, Mod: tea.ModCtrl})
	s = next.(Screen)
	if s.transcript.Following() {
		t.Fatal("precondition: ctrl+home pauses follow")
	}
	for i := 0; i < 3; i++ {
		n, _ := s.Update(uievent.EventMsg{Event: uievent.Event{
			Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: "streamed"},
		}})
		s = n.(Screen)
	}

	row := ansi.Strip(s.statusRow())
	if !strings.Contains(row, "3 new blocks") {
		t.Errorf("status row %q must state the count of what arrived while paused", row)
	}
	if !strings.Contains(row, "ctrl+end") {
		t.Errorf("status row %q must keep the way back to following", row)
	}

	// Following again: no count.
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl})
	s = next.(Screen)
	if row := ansi.Strip(s.statusRow()); strings.Contains(row, "new blocks") {
		t.Errorf("status row %q must not show a count while following", row)
	}
}

// TestClickClearsTheOverlay: a click is not a key, but it must still
// dismiss the help overlay - acting through an overlay acts on
// something the user cannot see.
func TestClickClearsTheOverlay(t *testing.T) {
	s := sized(t, 0)
	next, _ := s.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	s = next.(Screen)
	if s.overlay == "" {
		t.Fatal("precondition: the help overlay is open")
	}
	next, cmd := s.Update(leftClick(3, 3))
	s = next.(Screen)
	if s.overlay != "" {
		t.Error("a click must clear the overlay")
	}
	// The dismissed overlay drew over content the transcript/composer
	// underneath never redrew; without a terminal clear that content
	// can bleed through (see hasClearScreen's doc comment).
	if !hasClearScreen(cmd) {
		t.Error("expected dismissing the overlay by click to clear the screen")
	}
}

// TestConversationViewFlagsHoldAltScreen pins the base screen's surface
// contract with the router.
func TestConversationViewFlagsHoldAltScreen(t *testing.T) {
	s := sized(t, 0)
	if !s.ViewFlags().AltScreen {
		t.Error("the conversation screen must hold the alternate screen")
	}
}

func TestClickOnSplitPanelRightNavIgnored(t *testing.T) {
	s := toolScreen(t)
	s.width = uikitconfig.BreakpointWide + 20
	s.height = 30
	s.panel.open = true
	next, _ := s.Update(leftClick(s.width-5, 10))
	if next == nil {
		t.Fatal("expected non-nil screen")
	}
}

func TestClickOnNarrowPanelIgnored(t *testing.T) {
	s := toolScreen(t)
	s.width = 60
	s.height = 30
	s.panel.open = true
	next, _ := s.Update(leftClick(10, 5))
	if next == nil {
		t.Fatal("expected non-nil screen")
	}
}

func TestDoubleClickOnTopBarModelOpensModelPicker(t *testing.T) {
	s := sized(t, 0)
	s.SetCommandRunner(&fakeRunner{outcome: ports.CommandOutcome{ModelChoices: []string{"model-1", "model-2"}}})
	s.topbar.SetSession(ports.ModelInfo{Name: "claude-3-7-sonnet", Provider: "anthropic"}, ports.Usage{})
	s.width = 100
	s.height = 24

	start, end, ok := s.topbar.ModelBounds()
	if !ok {
		t.Fatal("expected ModelBounds ok")
	}
	modelCol := 1 + (start+end)/2 // 1 for gutter

	// First click: does not open
	next, _ := s.Update(leftClick(modelCol, 1)) // topGutter is 1
	s = next.(Screen)
	if s.modelPicker != nil {
		t.Error("single click should not open model picker")
	}

	// Second click within 500ms at same spot: opens model picker
	next, _ = s.Update(leftClick(modelCol, 1))
	s = next.(Screen)
	if s.modelPicker == nil {
		t.Error("double click on top bar model must open model picker")
	}
}

func TestDoubleClickOnTopBarActivityOpensSidebar(t *testing.T) {
	s := sized(t, 0)
	s.width = 100
	s.height = 24
	s.topbar.SetWidth(s.chatWidth())
	s.topbar.SetSession(ports.ModelInfo{Name: "claude-3-7-sonnet", Provider: "anthropic"}, ports.Usage{})
	s.topbar.SetActivity(1, 1)
	s.panel.open = false

	start, end, ok := s.topbar.ActivityBounds()
	if !ok {
		t.Fatal("expected ActivityBounds ok")
	}
	activityCol := 1 + (start+end)/2 // 1 for gutter

	// First click: does not open sidebar
	next, _ := s.Update(leftClick(activityCol, 1))
	s = next.(Screen)
	if s.panel.open {
		t.Error("single click should not open sidebar")
	}

	// Second click within 500ms at same spot: opens sidebar
	next, _ = s.Update(leftClick(activityCol, 1))
	s = next.(Screen)
	if !s.panel.open {
		t.Error("double click on top bar activity badge must open sidebar")
	}
}

func TestClickOnDialogCloseButtonOrBackdropDismissesPicker(t *testing.T) {
	s := sized(t, 0)
	s.width = 80
	s.height = 24
	pm := picker.New(s.Theme, s.Tier, []string{"model-1", "model-2"})
	s.modelPicker = &pm

	if s.modelPicker == nil {
		t.Fatal("precondition: model picker open")
	}

	// Click outside / backdrop dismisses dialog
	next, _ := s.Update(leftClick(1, 1))
	s = next.(Screen)
	if s.modelPicker != nil {
		t.Error("click on dialog backdrop must dismiss model picker")
	}
}

func TestClickOnPanelDialogCloseButtonDismissesSubagent(t *testing.T) {
	thread := &scriptedThread{
		events:  make(chan uievent.Event, 4),
		history: []ports.Message{{Role: "assistant", Text: "ready"}},
	}
	s := threadScreen(t, stubThreads{"sa-1": thread}, false)

	// Enter opens subagent thread
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if !s.panel.dialog {
		t.Fatal("precondition: subagent dialog open")
	}

	// Click on [x] close button in top-right of left pane
	readingW, _ := render.SplitWidths(contentWidth(s.width))
	closeX := readingW - 2
	next, _ = s.Update(leftClick(closeX, 2))
	s = next.(Screen)
	if s.panel.dialog {
		t.Error("click on close button must dismiss subagent dialog")
	}

	// Reopen subagent dialog
	s.panel.dialog = true
	s.panel.dialogAgent = "sa-1"

	// Click on nav pane in right pane (x >= readingW)
	next, _ = s.Update(leftClick(readingW+5, 3))
	s = next.(Screen)
	if !s.panel.focused {
		t.Error("click on nav pane must focus panel")
	}
}
