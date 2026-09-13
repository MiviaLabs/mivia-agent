package transcript

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// focused builds a measured model holding n one-row notice blocks.
func focused(t *testing.T, n int) Model {
	t.Helper()
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
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

// TestFocusedBlock_ComposerFocusReturnsFalse pins the miss case: at rest
// (composer focus, m.focus == -1) there is no block to return.
func TestFocusedBlock_ComposerFocusReturnsFalse(t *testing.T) {
	m := focused(t, 3)
	if _, ok := m.FocusedBlock(); ok {
		t.Fatal("FocusedBlock reported a block while the composer holds focus")
	}
}

// TestFocusedBlock_ReturnsTheFocusedBlock proves FocusedBlock returns the
// SAME block FocusIndex names, by identity (CallID) - the shape a caller
// like the cancel-tool-call keybinding needs to check "is this the running
// call I mean to act on" without re-deriving the index into m.blocks itself.
func TestFocusedBlock_ReturnsTheFocusedBlock(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "call-1", Name: "run_command"},
	})

	m = m.FocusPrev() // composer -> the one live block
	block, ok := m.FocusedBlock()
	if !ok {
		t.Fatal("FocusedBlock reported no block while one is focused")
	}
	if block.CallID != "call-1" {
		t.Fatalf("got CallID %q, want %q", block.CallID, "call-1")
	}
	if block.Kind != uievent.KindToolStart {
		t.Fatalf("got Kind %v, want KindToolStart", block.Kind)
	}
	if !block.Focused {
		t.Fatal("the block FocusedBlock returned does not itself report Focused")
	}
}

// TestOnlyOneBlockIsEverFocused pins the derived flag. Two blocks both
// claiming the focus would draw two focus rings.
func TestOnlyOneBlockIsEverFocused(t *testing.T) {
	m := focused(t, 4).FocusPrev().FocusPrev()
	count := 0
	for i, b := range m.Blocks() {
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
	if m.ClearFocus().Blocks()[m.FocusIndex()].Focused {
		t.Error("ClearFocus left a block flagged")
	}
}

func TestToggleFocusedFlipsTheBlock(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c1", Name: "edit", OK: true, Result: "a\nb"},
	})
	m = m.FocusPrev()

	before := m.Blocks()[0].Collapsed
	next, ok := m.ToggleFocused()
	if !ok {
		t.Fatal("expected the toggle to apply to a collapsible block")
	}
	if next.Blocks()[0].Collapsed == before {
		t.Error("the block did not change state")
	}
}

func TestToggleFocusedRefusesWhenNothingApplies(t *testing.T) {
	m := focused(t, 2)
	if _, ok := m.ToggleFocused(); ok {
		t.Error("expected a refusal with the composer focused")
	}

	// Prose has no header to collapse into.
	p := New(loadTheme(t), theme.TierASCII)
	p.SetSize(80, 40)
	p, _ = p.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "hello"}})
	p = p.FocusPrev()
	if _, ok := p.ToggleFocused(); ok {
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
	//
	// Each body is longer than CollapseThresholdLines: a collapsed tool
	// card windows to that many lines plus a hint row (C4), so a body AT
	// or under the window would render identically collapsed or
	// expanded and prove nothing about growth.
	long := make([]string, uikitconfig.CollapseThresholdLines+5)
	for i := range long {
		long[i] = fmt.Sprintf("line %d", i)
	}
	chunk := strings.Join(long, "\n")
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	for i := 0; i < 3; i++ {
		id := string(rune('a' + i))
		m, _ = m.HandleEvent(uievent.Event{
			Kind: uievent.KindToolStart,
			Body: uievent.ToolStartBody{ToolCallID: id, Name: "run_command"},
		})
		m, _ = m.HandleEvent(uievent.Event{
			Kind: uievent.KindToolOutput,
			Body: uievent.ToolOutputBody{ToolCallID: id, Chunk: chunk},
		})
	}
	m = m.SetAllCollapsed(true)
	m.SetSize(80, 10) // budget 6; three collapsed headers fit
	if got := len(m.Blocks()); got != 3 {
		t.Fatalf("got %d live blocks collapsed, want all 3 to fit the budget", got)
	}

	next := m.SetAllCollapsed(false)

	// Nothing is lost when everything expands: the viewport scrolls
	// instead of dropping content.
	if got, want := len(next.Blocks()), len(m.Blocks()); got != want {
		t.Errorf("got %d blocks after expand-all, want %d: expanding must not drop content", got, want)
	}
	if next.TotalRows() <= m.TotalRows() {
		t.Errorf("expand-all did not grow the conversation: %d rows before, %d after",
			m.TotalRows(), next.TotalRows())
	}
}

func TestCollapseAllLeavesProseAlone(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "a\nb"}})
	next := m.SetAllCollapsed(true)
	if next.Blocks()[0].Collapsed {
		t.Error("prose has no header to collapse into and must be left alone")
	}
}

func TestFocusedText(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
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

// TestFocusedTextCopiesTheReasoningDurationNotTheWordCount pins C1's
// clipboard parity: a reasoning block's copied text must state the same
// duration the screen shows ("Thought for Xs"), not the pre-C1
// "reasoning  N words  hidden" header headerPlain still builds from
// Header.Meta/State - and it must carry the block's full body regardless
// of whether the live view happens to be windowed (state 2) or collapsed
// (state 1) at the moment of the copy.
func TestFocusedTextCopiesTheReasoningDurationNotTheWordCount(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m.blocks = []Block{{
		Kind: uievent.KindReasoning, Collapsible: true, Collapsed: true,
		ElapsedMS: 4100,
		Header:    Header{Label: "reasoning", Meta: "9 words", State: "hidden"},
		Body:      []string{"step 1: analyze", "step 2: plan"},
	}}
	m = m.FocusPrev()

	got, ok := m.FocusedText()
	if !ok {
		t.Fatal("expected the focused block's text")
	}
	if !strings.Contains(got, "Thought for 4.1s") {
		t.Errorf("copied text = %q, want it to state the duration \"Thought for 4.1s\"", got)
	}
	if strings.Contains(got, "words") || strings.Contains(got, "hidden") {
		t.Errorf("copied text = %q, still carries the pre-C1 word-count header", got)
	}
	for _, want := range []string{"step 1: analyze", "step 2: plan"} {
		if !strings.Contains(got, want) {
			t.Errorf("copied text is missing %q:\n%s", want, got)
		}
	}
}

// TestFocusedTextIgnoresCollapseState: the user asked for the block's
// content, and collapse is a view state, not part of what they meant.
func TestFocusedTextIgnoresCollapseState(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c1", Name: "edit", OK: true, Result: "body-line"},
	})
	m = m.FocusPrev()
	open, _ := m.FocusedText()
	closed, _ := m.ToggleFocused()
	shut, _ := closed.FocusedText()
	if open != shut {
		t.Errorf("copied text changed with the collapse state:\nopen:   %q\nclosed: %q", open, shut)
	}
}

// TestFocusedTextIgnoresCollapseStateForNonToolBlock covers the OTHER
// side of the C3 change TestFocusedTextIgnoresCollapseState above pins
// for a tool block: a NON-tool collapsible kind (a hook here) still
// carries the plain v/>/blank collapse marker in column 1, and that
// marker IS view state, so the copy must not flip between "v " and "> "
// depending on whether the block happened to be open when copied.
func TestFocusedTextIgnoresCollapseStateForNonToolBlock(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindHook,
		Body: uievent.HookBody{Event: "PreToolUse", Program: "p", Tool: "run_command", Input: "line-1"},
	})
	m = m.FocusPrev()
	open, _ := m.FocusedText()
	closed, _ := m.ToggleFocused()
	shut, _ := closed.FocusedText()
	if open != shut {
		t.Errorf("copied text changed with the collapse state:\nopen:   %q\nclosed: %q", open, shut)
	}
	if strings.HasPrefix(open, "v ") || strings.HasPrefix(open, "> ") {
		t.Errorf("copied text leaked the collapse marker: %q", open)
	}
}

func TestToggleReasoning(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	// Stream reasoning delta with text
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindReasoning,
		Body: uievent.ReasoningDeltaBody{Text: "step 1: analyze\nstep 2: plan"},
	})
	// Finalize reasoning delta with word count
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindReasoning,
		Body: uievent.ReasoningDeltaBody{WordCount: 12},
	})
	m, _ = m.HandleEvent(noticeEvent("keep me open"))

	if m.ReasoningHidden() {
		t.Fatal("reasoning starts shown")
	}

	// Toggle reasoning hides reasoning
	m = m.ToggleReasoning()
	if !m.ReasoningHidden() {
		t.Fatal("the toggle did not record the hidden state")
	}
	for _, b := range m.Blocks() {
		if b.Kind == uievent.KindReasoning && !b.Collapsed {
			t.Error("a reasoning block stayed open")
		}
		if b.Kind == uievent.KindNotice && b.Collapsed {
			t.Error("the toggle collapsed a block that is not reasoning")
		}
	}

	// Second toggle shows reasoning again
	m = m.ToggleReasoning()
	if m.ReasoningHidden() {
		t.Error("the second press did not show reasoning again")
	}
}

// TestSetAllCollapsedNormalizesReasoningToAConsistentState pins that the
// global expand-all/collapse-all keys (IDExpandAll/IDCollapseAll) leave
// every reasoning block in the SAME state, not a mix that depends on
// which blocks a reader had individually cycled to the third (full-text)
// state (C1) before pressing the global key: a blanket "expand all" must
// not silently reveal one block's full text while every sibling opens to
// the windowed state, and a blanket "collapse all" must not leave a
// stale Expanded=true sitting behind a Collapsed block for a later
// expand-all to surface.
func TestSetAllCollapsedNormalizesReasoningToAConsistentState(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m.blocks = []Block{
		{Kind: uievent.KindReasoning, Collapsible: true, Collapsed: false, Expanded: true, Body: []string{"a"}},
		{Kind: uievent.KindReasoning, Collapsible: true, Collapsed: true, Expanded: false, Body: []string{"b"}},
	}

	expanded := m.SetAllCollapsed(false)
	for i, b := range expanded.Blocks() {
		if b.Collapsed {
			t.Errorf("block %d: want Collapsed=false after expand-all", i)
		}
		if b.Expanded {
			t.Errorf("block %d: want Expanded=false after expand-all (windowed, not a leaked full-text state)", i)
		}
	}

	collapsed := expanded.SetAllCollapsed(true)
	for i, b := range collapsed.Blocks() {
		if !b.Collapsed {
			t.Errorf("block %d: want Collapsed=true after collapse-all", i)
		}
		if b.Expanded {
			t.Errorf("block %d: want Expanded=false after collapse-all, not left stale for the next expand-all", i)
		}
	}
}

// TestReasoningToggleFocusedCyclesThreeStates pins C1's three-state
// toggle path (space/enter on the focused block, ToggleFocused):
// collapsed (only the "Thought for Xs" summary) -> windowed (the last
// CollapseThresholdLines lines) -> full text -> back to collapsed.
func TestReasoningToggleFocusedCyclesThreeStates(t *testing.T) {
	body := make([]string, uikitconfig.CollapseThresholdLines+5)
	for i := range body {
		body[i] = fmt.Sprintf("line %d", i+1)
	}
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m.blocks = []Block{{Kind: uievent.KindReasoning, Collapsible: true, Collapsed: true, Body: body}}
	m = m.FocusPrev()

	if !m.blocks[0].Collapsed || m.blocks[0].Expanded {
		t.Fatalf("reasoning must start collapsed with Expanded=false, got Collapsed=%v Expanded=%v",
			m.blocks[0].Collapsed, m.blocks[0].Expanded)
	}

	m, ok := m.ToggleFocused()
	if !ok || m.blocks[0].Collapsed || m.blocks[0].Expanded {
		t.Fatalf("first toggle: want the windowed state (Collapsed=false, Expanded=false), got ok=%v Collapsed=%v Expanded=%v",
			ok, m.blocks[0].Collapsed, m.blocks[0].Expanded)
	}

	m, ok = m.ToggleFocused()
	if !ok || m.blocks[0].Collapsed || !m.blocks[0].Expanded {
		t.Fatalf("second toggle: want the full-text state (Collapsed=false, Expanded=true), got ok=%v Collapsed=%v Expanded=%v",
			ok, m.blocks[0].Collapsed, m.blocks[0].Expanded)
	}

	m, ok = m.ToggleFocused()
	if !ok || !m.blocks[0].Collapsed || m.blocks[0].Expanded {
		t.Fatalf("third toggle: want back to collapsed (Collapsed=true, Expanded=false), got ok=%v Collapsed=%v Expanded=%v",
			ok, m.blocks[0].Collapsed, m.blocks[0].Expanded)
	}
}

// TestToggleReasoningHidesEveryReasoningBlockRegardlessOfExpanded pins
// that ctrl+r's global hide (ToggleReasoning) still collapses a
// reasoning block down to its one-line summary even when a reader had
// fully expanded it (C1's third state) - Collapsed always wins over
// Expanded when both are consulted at render time.
func TestToggleReasoningHidesEveryReasoningBlockRegardlessOfExpanded(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m.blocks = []Block{{
		Kind: uievent.KindReasoning, Collapsible: true,
		Collapsed: false, Expanded: true, Body: []string{"the full reasoning text"},
	}}

	m = m.ToggleReasoning()

	if !m.blocks[0].Collapsed {
		t.Error("ctrl+r must collapse a reasoning block even when it was fully expanded")
	}
	rendered := m.blocks[0].Render(loadTheme(t), theme.TierASCII, 80)
	if strings.Contains(rendered, "the full reasoning text") {
		t.Errorf("a collapsed reasoning block must show only its summary line, got:\n%s", rendered)
	}
}

// TestSyncFocusClampsAStrayIndex covers the guards. Focus is set from
// outside eviction in a later wave, so both ends are defended here
// rather than trusted.
func TestSyncFocusClampsAStrayIndex(t *testing.T) {
	m := focused(t, 2)

	m.focus = -9
	if got := m.syncFocus().FocusIndex(); got != -1 {
		t.Errorf("got %d, want any negative index folded to the composer", got)
	}

	m.focus = 99
	if got := m.syncFocus().FocusIndex(); got != len(m.Blocks())-1 {
		t.Errorf("got %d, want a clamp to the newest block", got)
	}
}

func TestFocusedText_DiffBlockHeaderPlain(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m, _ = m.pushBlock(Block{
		Kind: uievent.KindToolEnd,
		Header: Header{
			Label:   "search_replace",
			Detail:  "foo.go",
			DiffAdd: 5,
			DiffDel: 2,
			Meta:    "10ms",
			State:   "ok",
		},
		Body: []string{"line1", "line2"},
	})
	m = m.FocusPrev()
	text, ok := m.FocusedText()
	if !ok {
		t.Fatal("expected FocusedText ok=true")
	}
	if !strings.Contains(text, "+5 -2") {
		t.Errorf("expected '+5 -2' in FocusedText(), got %q", text)
	}
	if !strings.Contains(text, "search_replace") || !strings.Contains(text, "foo.go") {
		t.Errorf("expected tool label and detail in FocusedText(), got %q", text)
	}
}

// diffSample is a two-line hunk wide enough that unified and split
// renders visibly differ - used by every ToggleFocusedDiffSplit test.
func diffSample() *uievent.Diff {
	return &uievent.Diff{Path: "file.go", Hunks: []uievent.DiffHunk{{
		Header: "@@ -1,1 +1,1 @@",
		Lines: []uievent.DiffLine{
			{Kind: uievent.DiffLineDel, Text: "old line"},
			{Kind: uievent.DiffLineAdd, Text: "new line"},
		},
	}}}
}

// modelWithFocusedDiff builds a measured model holding one focused
// tool.end block that carries diffSample(), at the given width.
func modelWithFocusedDiff(t *testing.T, width int) Model {
	t.Helper()
	m := New(loadTheme(t), theme.TierTrueColor)
	m.SetSize(width, 40)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "c1", Name: "edit"}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c1", Name: "edit", OK: true, Diff: diffSample()}})
	m.blocks[0].Collapsed = false
	m.focus = 0
	return m
}

// TestToggleFocusedDiffSplitRoundTrip pins C8's toggle: pressing "s"
// twice on a wide-enough focused diff block returns to unified, and the
// FIRST press actually rendered split - not just flipped a flag no one
// reads.
func TestToggleFocusedDiffSplitRoundTrip(t *testing.T) {
	m := modelWithFocusedDiff(t, 160)
	unified := strings.Join(m.blocks[0].Body, "\n")
	if strings.Contains(unified, "│") {
		t.Fatalf("a freshly pushed diff block must start unified, got a column divider: %q", unified)
	}

	m, ok := m.ToggleFocusedDiffSplit()
	if !ok {
		t.Fatal("expected the toggle to succeed at width 160")
	}
	if !m.blocks[0].DiffSplit {
		t.Error("DiffSplit did not flip to true")
	}
	split := strings.Join(m.blocks[0].Body, "\n")
	if !strings.Contains(split, "│") {
		t.Errorf("split render carries no column divider: %q", split)
	}
	if split == unified {
		t.Error("toggling split did not change the rendered body at all")
	}

	m, ok = m.ToggleFocusedDiffSplit()
	if !ok {
		t.Fatal("expected the second toggle to succeed")
	}
	if m.blocks[0].DiffSplit {
		t.Error("DiffSplit did not flip back to false")
	}
	backToUnified := strings.Join(m.blocks[0].Body, "\n")
	if backToUnified != unified {
		t.Errorf("round trip did not restore the original unified render\n got  %q\n want %q", backToUnified, unified)
	}
}

// TestToggleFocusedDiffSplitRefusedBelowMinWidth pins the refusal: the
// toggle is a documented no-op - false, no state change - under
// render.MinSplitDiffWidth, not a silent split at an illegible width.
func TestToggleFocusedDiffSplitRefusedBelowMinWidth(t *testing.T) {
	m := modelWithFocusedDiff(t, render.MinSplitDiffWidth-1)
	before := m.blocks[0].DiffSplit
	beforeBody := strings.Join(m.blocks[0].Body, "\n")

	next, ok := m.ToggleFocusedDiffSplit()
	if ok {
		t.Fatal("expected the toggle to be refused below MinSplitDiffWidth")
	}
	if next.blocks[0].DiffSplit != before {
		t.Error("a refused toggle must not change DiffSplit")
	}
	if strings.Join(next.blocks[0].Body, "\n") != beforeBody {
		t.Error("a refused toggle must not change the rendered body")
	}
}

// TestToggleFocusedDiffSplitRefusedWithoutFocusOrDiff pins the other two
// refusal conditions: nothing focused, and a focused block with no diff.
func TestToggleFocusedDiffSplitRefusedWithoutFocusOrDiff(t *testing.T) {
	t.Run("nothing focused", func(t *testing.T) {
		m := focused(t, 2)
		m.SetSize(160, 40)
		if _, ok := m.ToggleFocusedDiffSplit(); ok {
			t.Error("expected refusal with no block focused")
		}
	})
	t.Run("focused block has no diff", func(t *testing.T) {
		m := focused(t, 2)
		m.SetSize(160, 40)
		m = m.FocusPrev()
		if _, ok := m.ToggleFocusedDiffSplit(); ok {
			t.Error("expected refusal on a focused block with no diff")
		}
	})
}

// TestToggleFocusedDiffSplitRefusedInTheContentWidthDeadZone pins a real
// bug found by bug-audit: ToggleFocusedDiffSplit checked the raw
// viewport width against render.MinSplitDiffWidth, but restyle (the
// path this toggle calls) renders the diff at
// m.width-groupIndent-uikitconfig.BodyIndent (6 columns narrower). A
// viewport width in [120,125] passed the raw check while the actual
// render width [114,119] was still below MinSplitDiffWidth -
// DiffSplit flipped true and the toggle reported success, but the
// rendered Body stayed unified: state and render disagreed, and a
// LATER unrelated resize could silently flip the render to split with
// no further key press.
func TestToggleFocusedDiffSplitRefusedInTheContentWidthDeadZone(t *testing.T) {
	for width := render.MinSplitDiffWidth; width < render.MinSplitDiffWidth+groupIndent+uikitconfig.BodyIndent; width++ {
		m := modelWithFocusedDiff(t, width)
		beforeBody := strings.Join(m.blocks[0].Body, "\n")

		next, ok := m.ToggleFocusedDiffSplit()
		if ok {
			t.Errorf("width %d: expected refusal (render width %d < MinSplitDiffWidth %d), but the toggle succeeded",
				width, width-groupIndent-uikitconfig.BodyIndent, render.MinSplitDiffWidth)
		}
		if next.blocks[0].DiffSplit {
			t.Errorf("width %d: DiffSplit flipped true even though the toggle was refused", width)
		}
		if strings.Join(next.blocks[0].Body, "\n") != beforeBody {
			t.Errorf("width %d: Body changed even though the toggle was refused", width)
		}
	}
}

// TestToggleFocusedDiffSplitReanchorsTheViewport pins a real bug found
// by bug-audit: every OTHER height-changing mutator in this file
// (ToggleFocused, toggleReasoningFocused, SetAllCollapsed) ends by
// calling ScrollToFocus/clampOffset, because changing a block's height
// shifts every row below it - without re-anchoring, a transcript
// following the tail stops following, or a fixed offset now points
// into the wrong content. ToggleFocusedDiffSplit changes a block's
// rendered row count (split and unified pack a hunk into a different
// number of lines) but returned with neither call.
func TestToggleFocusedDiffSplitReanchorsTheViewport(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor)
	// Small height and several prior blocks: the transcript is already
	// scrolled, following the tail, so a height change that is not
	// re-anchored is observable as "stopped following".
	m.SetSize(160, 6)
	for i := 0; i < 5; i++ {
		m, _ = m.HandleEvent(noticeEvent("n" + string(rune('a'+i))))
	}
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "c1", Name: "edit"}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c1", Name: "edit", OK: true, Diff: diffSample()}})
	m.blocks[len(m.blocks)-1].Collapsed = false

	if !m.Following() {
		t.Fatal("precondition: expected the transcript to be following the tail")
	}
	m.focus = len(m.blocks) - 1

	next, ok := m.ToggleFocusedDiffSplit()
	if !ok {
		t.Fatal("expected the toggle to succeed at width 160")
	}
	// Following() alone does not prove this: it is a FLAG that only
	// clampOffset/ScrollToFocus consult to decide whether to advance
	// m.offset, not something Rows() re-derives at render time. The
	// real invariant every sibling mutator keeps is offset==maxOffset
	// while following - checked directly, in-package, since neither is
	// exported.
	if !next.follow {
		t.Errorf("toggling the focused diff block's split view cleared the follow flag")
	}
	if next.offset != next.maxOffset() {
		t.Errorf("offset (%d) does not track the tail (maxOffset %d) after the toggle changed the block's height - the viewport was not re-anchored",
			next.offset, next.maxOffset())
	}
}

// TestToggleFocusedDiffSplitDoesNotCorruptTheBody pins a real bug found
// alongside the viewport re-anchor issue: restyle's diff branch used to
// infer where the diff's own rendered lines began in Body by comparing
// the OLD and NEW render lengths (replaceDiffTail). Unified and split
// do not render a balanced hunk to the same row count, so toggling
// DiffSplit exposed the inference as wrong - it kept a stale line of
// the OLD render, or duplicated the hunk header, depending on which
// direction the length changed. block.go's DiffBodyPrefixLen field
// fixes this by tracking the true split point instead of inferring it.
func TestToggleFocusedDiffSplitDoesNotCorruptTheBody(t *testing.T) {
	m := modelWithFocusedDiff(t, 160)
	next, ok := m.ToggleFocusedDiffSplit()
	if !ok {
		t.Fatal("expected the toggle to succeed at width 160")
	}
	body := next.blocks[0].Body
	headers := 0
	for _, line := range body {
		if strings.Contains(ansi.Strip(line), "@@") {
			headers++
		}
	}
	if headers != 1 {
		t.Errorf("got %d hunk headers after toggling to split, want exactly 1 - the render is corrupted: %q", headers, body)
	}
	// The one-shot render of the same diff, at the same split state, is
	// the ground truth for what a correct rebuild must equal.
	want := render.FormatDiffLines(next.Theme, next.Tier, next.diffContentWidth(), *diffSample(), true)
	if strings.Join(body, "\n") != strings.Join(want, "\n") {
		t.Errorf("toggled body does not match a one-shot split render\n got  %q\n want %q", body, want)
	}
}

// TestToggleFocusedDiffSplitPreservesOutputAboveTheDiff pins the other
// half of the DiffBodyPrefixLen contract: a tool call that printed
// output before its diff (handleToolEnd's live-merge path) must keep
// that output when the diff portion below it is rebuilt for a toggle,
// the same guarantee TestSetThemeKeepsToolOutputAboveTheDiff already
// pins for a theme change.
func TestToggleFocusedDiffSplitPreservesOutputAboveTheDiff(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor)
	m.SetSize(160, 40)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "c1", Name: "edit_file"}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "c1", Chunk: "scanning up.go\n"}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c1", Name: "edit_file", OK: true, Diff: diffSample()}})
	m.blocks[0].Collapsed = false
	m.focus = 0

	next, ok := m.ToggleFocusedDiffSplit()
	if !ok {
		t.Fatal("expected the toggle to succeed at width 160")
	}
	joined := strings.Join(next.blocks[0].Body, "\n")
	if !strings.Contains(joined, "scanning up.go") {
		t.Errorf("toggling split lost the tool output that preceded the diff: %q", joined)
	}
}
