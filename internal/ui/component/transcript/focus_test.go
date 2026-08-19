package transcript

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// focused builds a measured model holding n one-row notice blocks.
func focused(t *testing.T, n int) Model {
	t.Helper()
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40, 4)
	for i := 0; i < n; i++ {
		m, _ = m.HandleEvent(noticeEvent("n" + string(rune('a'+i))))
	}
	return m
}

// TestFocusDirectionIsVertical pins the model the keys read as: the
// composer is the bottom row, Shift-Tab goes up into the transcript and
// Tab comes back down out of it.
func TestFocusDirectionIsVertical(t *testing.T) {
	m := focused(t, 3)
	if m.Focused() || m.FocusIndex() != -1 {
		t.Fatalf("got focus %d, want the composer at rest", m.FocusIndex())
	}

	m = m.FocusPrev() // up, into the newest block
	if got := m.FocusIndex(); got != 2 {
		t.Fatalf("got focus %d, want the newest block", got)
	}
	m = m.FocusPrev()
	if got := m.FocusIndex(); got != 1 {
		t.Errorf("got focus %d, want one further up", got)
	}
	m = m.FocusNext()
	m = m.FocusNext()
	if got := m.FocusIndex(); got != -1 {
		t.Errorf("got focus %d, want Tab past the newest to reach the composer", got)
	}
}

func TestFocusWallsDoNotWrap(t *testing.T) {
	m := focused(t, 3)
	for i := 0; i < 10; i++ {
		m = m.FocusPrev()
	}
	if got := m.FocusIndex(); got != 0 {
		t.Errorf("got focus %d, want it held at the oldest block", got)
	}
	// From the composer, Tab has nothing below it to reach.
	m = m.ClearFocus()
	if got := m.FocusNext().FocusIndex(); got != -1 {
		t.Errorf("got focus %d, want the composer kept", got)
	}
}

func TestFocusOnAnEmptyWindowIsANoOp(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	if got := m.FocusNext().FocusIndex(); got != -1 {
		t.Errorf("got %d, want no focus with nothing live", got)
	}
	if got := m.FocusPrev().FocusIndex(); got != -1 {
		t.Errorf("got %d, want no focus with nothing live", got)
	}
}

// TestOnlyOneBlockIsEverFocused pins the derived flag. Two blocks both
// claiming the focus would draw two focus rings.
func TestOnlyOneBlockIsEverFocused(t *testing.T) {
	m := focused(t, 4).FocusPrev().FocusPrev()
	count := 0
	for i, b := range m.Live() {
		if b.Focused {
			count++
			if i != m.FocusIndex() {
				t.Errorf("block %d is flagged focused but the index is %d", i, m.FocusIndex())
			}
		}
	}
	if count != 1 {
		t.Errorf("got %d focused blocks, want exactly 1", count)
	}
	if m.ClearFocus().Live()[m.FocusIndex()].Focused {
		t.Error("ClearFocus left a block flagged")
	}
}

func TestToggleFocusedFlipsTheBlock(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40, 4)
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c1", Name: "edit", OK: true, Result: "a\nb"},
	})
	m = m.FocusPrev()

	before := m.Live()[0].Collapsed
	next, _, ok := m.ToggleFocused()
	if !ok {
		t.Fatal("expected the toggle to apply to a collapsible block")
	}
	if next.Live()[0].Collapsed == before {
		t.Error("the block did not change state")
	}
}

func TestToggleFocusedRefusesWhenNothingApplies(t *testing.T) {
	m := focused(t, 2)
	if _, _, ok := m.ToggleFocused(); ok {
		t.Error("expected a refusal with the composer focused")
	}

	// Prose has no header to collapse into.
	p := New(loadTheme(t), theme.TierASCII)
	p.SetSize(80, 40, 4)
	p, _ = p.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "hello"}})
	p = p.FocusPrev()
	if _, _, ok := p.ToggleFocused(); ok {
		t.Error("expected a refusal on a prose block")
	}
}

// TestExpandingCanEvict pins the dangerous direction of expand-all: the
// blocks grow at once, so the oldest are pushed to scrollback rather
// than dropped.
func TestExpandingCanEvict(t *testing.T) {
	// Built at a roomy size so every block keeps its body, then shrunk
	// while collapsed so the three header rows still fit. Expanding is
	// then the only thing that can overflow the budget.
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40, 4)
	for i := 0; i < 3; i++ {
		id := string(rune('a' + i))
		m, _ = m.HandleEvent(uievent.Event{
			Kind: uievent.KindToolStart,
			Body: uievent.ToolStartBody{ToolCallID: id, Name: "run_command"},
		})
		m, _ = m.HandleEvent(uievent.Event{
			Kind: uievent.KindToolOutput,
			Body: uievent.ToolOutputBody{ToolCallID: id, Chunk: "one\ntwo\nthree"},
		})
	}
	m, _ = m.SetAllCollapsed(true)
	m.SetSize(80, 10, 4) // budget 6; three collapsed headers fit
	if got := len(m.Live()); got != 3 {
		t.Fatalf("got %d live blocks collapsed, want all 3 to fit the budget", got)
	}

	next, committed := m.SetAllCollapsed(false)
	if committed == "" {
		t.Fatal("expanding past the budget evicted nothing; the oldest must reach scrollback")
	}
	if got := len(next.Live()); got >= 3 {
		t.Errorf("got %d live blocks after expanding, want the oldest evicted", got)
	}
	// Whatever survives must still fit.
	rows := 0
	for _, b := range next.Live() {
		rows += b.Height(80)
	}
	if rows > next.budget() {
		t.Errorf("live window is %d rows after expand-all, budget is %d", rows, next.budget())
	}
}

func TestCollapseAllLeavesProseAlone(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40, 4)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "a\nb"}})
	next, _ := m.SetAllCollapsed(true)
	if next.Live()[0].Collapsed {
		t.Error("prose has no header to collapse into and must be left alone")
	}
}

func TestFocusedText(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40, 4)
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c1", Name: "run_command", OK: true, Result: "line-1\nline-2"},
	})
	if _, ok := m.FocusedText(); ok {
		t.Error("expected no text with the composer focused")
	}

	m = m.FocusPrev()
	got, ok := m.FocusedText()
	if !ok {
		t.Fatal("expected the focused block's text")
	}
	for _, want := range []string{"run_command", "line-1", "line-2"} {
		if !strings.Contains(got, want) {
			t.Errorf("copied text is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Error("copied text carries styling; the clipboard must hold plain text")
	}
}

// TestFocusedTextIgnoresCollapseState: the user asked for the block's
// content, and collapse is a view state, not part of what they meant.
func TestFocusedTextIgnoresCollapseState(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40, 4)
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c1", Name: "edit", OK: true, Result: "body-line"},
	})
	m = m.FocusPrev()
	open, _ := m.FocusedText()
	closed, _, _ := m.ToggleFocused()
	shut, _ := closed.FocusedText()
	if open != shut {
		t.Errorf("copied text changed with the collapse state:\nopen:   %q\nclosed: %q", open, shut)
	}
}

func TestToggleReasoning(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40, 4)
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindReasoning,
		Body: uievent.ReasoningDeltaBody{WordCount: 12},
	})
	m, _ = m.HandleEvent(noticeEvent("keep me open"))

	if m.ReasoningHidden() {
		t.Fatal("reasoning starts shown")
	}
	m = m.ToggleReasoning()
	if !m.ReasoningHidden() {
		t.Fatal("the toggle did not record the hidden state")
	}
	for _, b := range m.Live() {
		if b.Kind == uievent.KindReasoning && !b.Collapsed {
			t.Error("a reasoning block stayed open")
		}
		if b.Kind == uievent.KindNotice && b.Collapsed {
			t.Error("the toggle collapsed a block that is not reasoning")
		}
	}
	if m.ToggleReasoning().ReasoningHidden() {
		t.Error("the second press did not show reasoning again")
	}
}
