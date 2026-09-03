package transcript

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// proseThenTool is the commonest shape on screen: assistant prose, then a
// tool call. The tool block starts a new section, so the layout puts a
// blank separator row above it - which is exactly the case where span.top
// used to point at the separator instead of the header.
func proseThenTool(t *testing.T) Model {
	t.Helper()
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 20)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "some prose"}})
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "a", Name: "run_command"},
	})
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "a", Chunk: "hidden-line"},
	})
	return m.SetAllCollapsed(true)
}

// drawnRowOf finds the viewport row a block's header actually draws on.
// Reading it back out of the rendered output is the point: every other
// assertion here derives its row from the layout, which is the thing
// under test, so only the screen can referee.
func drawnRowOf(t *testing.T, m Model, needle string) int {
	t.Helper()
	rows := strings.Split(ansi.Strip(m.View()), "\n")
	for i, r := range rows {
		if strings.Contains(r, needle) {
			return i
		}
	}
	t.Fatalf("%q draws on no visible row:\n%s", needle, strings.Join(rows, "\n"))
	return -1
}

// TestSpanTopIsTheHeaderRowNotTheSeparator is the discriminator for the
// whole span geometry. span.top used to be assigned BEFORE the separator
// row was counted, so for every block that starts a section - which is
// every tool call after prose - top named the blank row above the header.
//
// Nothing failed loudly, because the two callers that convert a row
// number back into a block (the click router) and forward into a scroll
// (ScrollToFocus) both consumed the same wrong number consistently. Only
// the rendered output disagrees, so this test compares against it.
func TestSpanTopIsTheHeaderRowNotTheSeparator(t *testing.T) {
	m := proseThenTool(t)
	drawn := drawnRowOf(t, m, "run_command")
	spans := m.layout()
	tool := len(m.blocks) - 1
	if !spans[tool].sepBefore {
		t.Fatal("precondition: the tool block must start a section, so a separator sits above it")
	}
	if got := spans[tool].top - m.Offset(); got != drawn {
		t.Errorf("span.top maps to viewport row %d, but the header draws on row %d", got, drawn)
	}
}

// TestClickLandsOnTheHeaderTheUserSees: the click contract stated against
// the rendered screen. The old geometry made this exactly backwards -
// clicking the header did nothing and clicking the blank row above it
// expanded the block - which is unreachable from any assertion that
// derives its click row from the layout.
func TestClickLandsOnTheHeaderTheUserSees(t *testing.T) {
	m := proseThenTool(t)
	header := drawnRowOf(t, m, "run_command")
	if header == 0 {
		t.Fatal("precondition: a separator row must sit above the header")
	}
	if _, ok := m.ExpandBlockAtScreenRow(header); !ok {
		t.Errorf("clicking the header row (%d) the user can see did not expand the block", header)
	}
	if _, ok := m.ExpandBlockAtScreenRow(header - 1); ok {
		t.Errorf("clicking the blank separator row (%d) expanded a block", header-1)
	}
}

// TestScrollToFocusShowsTheWholeFocusedBlock pins the second consumer of
// span.top against the corrected geometry: bottom is derived as
// top+height, so the two must be measured from the same row or the
// arithmetic reaches one row short. It is an invariant test, not a
// regression pin - the old geometry happened to pass this fixture,
// because it shifted top and bottom together.
func TestScrollToFocusShowsTheWholeFocusedBlock(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 6)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "some prose"}})
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "a", Name: "run_command"},
	})
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "a", Chunk: "one\ntwo\nthree"},
	})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "tail prose"}})

	tool := -1
	for i := range m.blocks {
		if m.blocks[i].Header.Label == "run_command" {
			tool = i
		}
	}
	if tool < 0 {
		t.Fatal("precondition: the tool block exists")
	}
	m.blocks[tool].Collapsed = false
	m.focus = tool
	m = m.syncFocus().ScrollToFocus()

	s := m.layout()[tool]
	if s.top < m.Offset() {
		t.Errorf("focused block starts at row %d, above the viewport top %d", s.top, m.Offset())
	}
	if s.top+s.height > m.Offset()+m.Height() {
		t.Errorf("focused block ends at row %d, below the viewport bottom %d",
			s.top+s.height, m.Offset()+m.Height())
	}
}
