package conversation

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/composer"
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
	// header sits at transcript row 0.
	next, _ := s.Update(leftClick(3, 0))
	s = next.(Screen)
	if got := s.transcript.Blocks()[0].Collapsed; got {
		t.Error("a click on the collapsed header must expand the block")
	}
}

// TestClickExpandedBodyDoesNothing: only a collapsed header is a target.
func TestClickExpandedBodyDoesNothing(t *testing.T) {
	s := toolScreen(t)
	next, _ := s.Update(leftClick(3, 0))
	s = next.(Screen) // now expanded

	next, _ = s.Update(leftClick(6, 2)) // a body row
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

	// The input sits inside the composer's frame: one row above the
	// bottom border, one column in for the gutter and one for the
	// border itself.
	inputRow := 24 - 2
	next, _ = s.Update(leftClick(1+1+2+3, inputRow)) // column 5 of the input == after "hel"
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

	// Layout at height 24: bottom border 23, input 22, top border 21,
	// menu rows 19-20 (2 rows above the frame).
	next, _ = s.Update(leftClick(4, 20)) // second menu row: "agents"
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
