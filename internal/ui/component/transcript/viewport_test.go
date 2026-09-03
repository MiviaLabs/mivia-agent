package transcript

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestViewIsAlwaysExactlyTheViewportHeight is the cockpit's layout
// contract. The transcript owns a fixed number of rows, so it must fill
// them whether the conversation is empty, short, or long. A view that
// shrank would let the composer and status row move as output arrives.
func TestViewIsAlwaysExactlyTheViewportHeight(t *testing.T) {
	for _, n := range []int{0, 1, 5, 40} {
		m := sizedModel(t, 80, 10, n)
		if got := len(m.Rows()); got != 10 {
			t.Errorf("with %d blocks the viewport drew %d rows, want exactly 10", n, got)
		}
	}
}

// TestSeparatorsFollowSections pins transcript-polish.md R1: blank rows
// separate SECTIONS of the transcript - between prose and the activity
// group that follows it - while consecutive activity blocks inside one
// group read as one dense run with no blank rows between them. Spacing
// follows turns, not events.
func TestSeparatorsFollowSections(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 30)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "fix the bug"}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "On it."}})
	m, _ = m.HandleEvent(noticeEvent(blockName(0)))
	m, _ = m.HandleEvent(noticeEvent(blockName(1)))
	rows := strings.Split(ansi.Strip(m.View()), "\n")
	rowOf := func(needle string) (int, bool) {
		for i, r := range rows {
			if strings.Contains(r, needle) {
				return i, true
			}
		}
		return 0, false
	}
	prose, ok := rowOf("On it.")
	if !ok {
		t.Fatalf("assistant prose is missing from the view:\n%s", strings.Join(rows, "\n"))
	}
	if rows[prose-1] != "" {
		t.Errorf("got %q above the prose, want a blank section separator", rows[prose-1])
	}
	first, ok := rowOf(strings.TrimSpace(blockName(0)))
	if !ok || !strings.Contains(rows[first], blockName(0)) {
		t.Fatalf("first notice is missing from the view:\n%s", strings.Join(rows, "\n"))
	}
	if rows[first-1] != "" {
		t.Errorf("got %q above the activity group, want a blank section separator", rows[first-1])
	}
	second, ok := rowOf(strings.TrimSpace(blockName(1)))
	if !ok {
		t.Fatalf("second notice is missing from the view:\n%s", strings.Join(rows, "\n"))
	}
	if second != first+1 {
		t.Errorf("second notice at row %d, first at %d: activity rows must be adjacent inside a group", second, first)
	}
}

func TestUnmeasuredViewportDrawsNothing(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	if got := m.Rows(); got != nil {
		t.Errorf("got %q, want nothing before the first size", got)
	}
}

// TestNothingIsLostWhenItScrollsOff is the property the cockpit replaces
// eviction with. The inline renderer handed blocks to the terminal; the
// cockpit keeps them, so every block must still be reachable.
func TestNothingIsLostWhenItScrollsOff(t *testing.T) {
	const n = 40
	m := sizedModel(t, 80, 10, n)

	if got := len(m.Blocks()); got != n {
		t.Fatalf("got %d blocks, want all %d kept", got, n)
	}
	dump := ansi.Strip(m.Dump())
	for i := 0; i < n; i++ {
		if c := strings.Count(dump, blockName(i)); c != 1 {
			t.Errorf("block %q appears %d times in the dump, want exactly 1", blockName(i), c)
		}
	}
	// The viewport itself shows only the tail.
	view := ansi.Strip(m.View())
	if strings.Contains(view, blockName(0)) {
		t.Error("the oldest block is on screen; the viewport is not following the tail")
	}
	if !strings.Contains(view, blockName(n-1)) {
		t.Error("the newest block is not on screen while following")
	}
}

// TestRowsDoesNotRenderBlocksAboveTheViewport pins the performance
// contract Rows' own doc comment states: "a block above or below it
// costs one Height call and nothing else." Following the tail of a long
// conversation must not re-render the whole scrollback on every frame.
func TestRowsDoesNotRenderBlocksAboveTheViewport(t *testing.T) {
	const n = 500
	m := sizedModel(t, 80, 10, n)

	renderCalls = 0
	_ = m.Rows()
	// 10 visible rows plus generous slack for the streaming tail and any
	// boundary block - nowhere near the 500 blocks held in history.
	if renderCalls > 30 {
		t.Errorf("Rows() triggered %d Block.Render calls for a 10-row viewport over %d blocks; want it bounded by the viewport, not the history", renderCalls, n)
	}
}

func TestScrollToTopAndBottom(t *testing.T) {
	const n = 40
	m := sizedModel(t, 80, 10, n)

	m = m.ScrollToTop()
	if m.Following() {
		t.Error("jumping to the top must pause auto-follow")
	}
	if !strings.Contains(ansi.Strip(m.View()), blockName(0)) {
		t.Error("the oldest block is not on screen at the top")
	}

	m = m.ScrollToBottom()
	if !m.Following() {
		t.Error("jumping to the bottom must resume auto-follow")
	}
	if !strings.Contains(ansi.Strip(m.View()), blockName(n-1)) {
		t.Error("the newest block is not on screen at the bottom")
	}
}

// TestScrollingUpPausesFollow pins rule 6.7: streaming output must not
// drag the reader away from what they stopped to read.
func TestScrollingUpPausesFollow(t *testing.T) {
	m := sizedModel(t, 80, 10, 40)
	if !m.Following() {
		t.Fatal("a fresh transcript follows the tail")
	}

	m = m.ScrollBy(-3)
	if m.Following() {
		t.Fatal("scrolling up did not pause auto-follow")
	}
	at := m.Offset()

	// New output arrives; the view must stay put.
	m, _ = m.HandleEvent(noticeEvent("arrived-while-reading"))
	if m.Offset() != at {
		t.Errorf("the view moved from row %d to %d while the reader was scrolled up", at, m.Offset())
	}
	if strings.Contains(ansi.Strip(m.View()), "arrived-while-reading") {
		t.Error("new output pulled itself onto a screen the reader had scrolled away from")
	}
}

// TestScrollingBackToTheBottomResumesFollow is the other half: the
// reader must be able to get back without a separate key.
func TestScrollingBackToTheBottomResumesFollow(t *testing.T) {
	m := sizedModel(t, 80, 10, 40).ScrollBy(-3)
	m = m.ScrollBy(3)
	if !m.Following() {
		t.Error("returning to the bottom did not resume auto-follow")
	}
}

func TestScrollClampsAtBothEnds(t *testing.T) {
	m := sizedModel(t, 80, 10, 40)
	m = m.ScrollBy(-1000)
	if m.Offset() != 0 {
		t.Errorf("got offset %d, want a clamp to the first row", m.Offset())
	}
	m = m.ScrollBy(1000)
	if got, want := m.Offset(), m.TotalRows()-10; got != want {
		t.Errorf("got offset %d, want a clamp to %d", got, want)
	}
}

func TestScrollByZeroChangesNothing(t *testing.T) {
	m := sizedModel(t, 80, 10, 40).ScrollBy(-3)
	before := m.Offset()
	if got := m.ScrollBy(0); got.Offset() != before || got.Following() {
		t.Error("a zero scroll changed the viewport state")
	}
}

// TestShortConversationNeverScrolls: with less content than the screen
// there is nowhere to go, and the offset must stay at zero.
func TestShortConversationNeverScrolls(t *testing.T) {
	m := sizedModel(t, 80, 20, 3)
	for _, next := range []Model{m.ScrollBy(-5), m.ScrollBy(5), m.ScrollToTop(), m.ScrollToBottom()} {
		if next.Offset() != 0 {
			t.Errorf("got offset %d, want 0 when the conversation fits the screen", next.Offset())
		}
	}
}

func TestPageBy(t *testing.T) {
	m := sizedModel(t, 80, 10, 60)
	bottom := m.Offset()

	half := m.PageBy(-1, 2)
	if got, want := half.Offset(), bottom-5; got != want {
		t.Errorf("half page up: got offset %d, want %d", got, want)
	}
	full := m.PageBy(-1, 1)
	if got, want := full.Offset(), bottom-10; got != want {
		t.Errorf("full page up: got offset %d, want %d", got, want)
	}
	// A viewport too short for a fraction still moves by one row.
	tiny := sizedModel(t, 80, 1, 60)
	if got := tiny.Offset() - tiny.PageBy(-1, 2).Offset(); got != 1 {
		t.Errorf("got a %d-row step in a 1-row viewport, want 1", got)
	}
}

// TestResizeKeepsTheReaderInPlace: a resize must not silently scroll.
func TestResizeReclampsWithoutLosingContent(t *testing.T) {
	m := sizedModel(t, 80, 10, 40).ScrollToTop()
	m.SetSize(80, 20)
	if m.Offset() != 0 {
		t.Errorf("got offset %d after a grow, want the reader left where they were", m.Offset())
	}
	if got := len(m.Rows()); got != 20 {
		t.Errorf("got %d rows after the resize, want 20", got)
	}
	if got := len(m.Blocks()); got != 40 {
		t.Errorf("got %d blocks after a resize, want all 40", got)
	}
}

// TestFollowingSurvivesAResize: a following viewport must still be at
// the bottom after the terminal changes size.
func TestFollowingSurvivesAResize(t *testing.T) {
	m := sizedModel(t, 80, 10, 40)
	m.SetSize(80, 6)
	if !m.Following() {
		t.Error("the viewport stopped following across a resize")
	}
	if !strings.Contains(ansi.Strip(m.View()), blockName(39)) {
		t.Error("the newest block is not on screen after a resize while following")
	}
}

func TestSetSizeToTheSameSizeIsANoOp(t *testing.T) {
	m := sizedModel(t, 80, 10, 40).ScrollToTop()
	m.SetSize(80, 10)
	if m.Offset() != 0 || m.Following() {
		t.Error("re-setting the same size disturbed the viewport")
	}
}

// TestTrimCountsWhatItDropped pins the honesty rule. A transcript that
// silently forgets its own start gives the user no way to tell a short
// session from a truncated one.
func TestTrimCountsWhatItDropped(t *testing.T) {
	over := 5
	m := sizedModel(t, 80, 10, uikitconfig.MaxTranscriptLines+over)

	if got := len(m.Blocks()); got != uikitconfig.MaxTranscriptLines {
		t.Errorf("got %d blocks, want the bound %d", got, uikitconfig.MaxTranscriptLines)
	}
	if got := m.Dropped(); got != over {
		t.Errorf("got %d dropped, want %d", got, over)
	}
	if !strings.Contains(ansi.Strip(m.Dump()), "earlier blocks dropped") {
		t.Error("the dump does not state that the transcript was truncated")
	}
	// The oldest surviving block is the one after the dropped run.
	if got := ansi.Strip(m.Dump()); !strings.Contains(got, blockName(over)) {
		t.Errorf("the oldest surviving block is missing from the dump")
	}
}

// TestDumpExpandsCollapsedBlocks: a collapse is a view state, and a dump
// the user asked for must not hide what they cannot see.
func TestDumpExpandsCollapsedBlocks(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 20)
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "a", Name: "run_command"},
	})
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "a", Chunk: "hidden-line"},
	})
	m = m.SetAllCollapsed(true)

	if strings.Contains(ansi.Strip(m.View()), "hidden-line") {
		t.Fatal("the collapsed block still shows its body on screen")
	}
	if !strings.Contains(ansi.Strip(m.Dump()), "hidden-line") {
		t.Error("the dump hides a collapsed body; it must expand everything")
	}
}

func TestDumpIncludesTheStreamingTail(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 20)
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindTextDelta,
		Body: uievent.TextDeltaBody{Text: "still-streaming"},
	})
	if !strings.Contains(ansi.Strip(m.Dump()), "still-streaming") {
		t.Error("the dump drops the in-flight span")
	}
}

// TestFocusScrollsTheBlockIntoView: moving focus to a block above the
// fold must bring it on screen, or the focus ring is invisible.
func TestFocusScrollsTheBlockIntoView(t *testing.T) {
	m := sizedModel(t, 80, 10, 40)
	for i := 0; i < 15; i++ {
		m = m.FocusPrev()
	}
	if !m.Focused() {
		t.Fatal("expected a focused block")
	}
	want := ansi.Strip(m.Blocks()[m.FocusIndex()].Render(m.Theme, m.Tier, 80))
	if !strings.Contains(ansi.Strip(m.View()), strings.TrimSpace(want)) {
		t.Errorf("the focused block is off screen:\n%s", ansi.Strip(m.View()))
	}
}

// TestTallBlockAlignsToItsHead: a block taller than the viewport shows
// its header, which is what identifies it.
func TestTallBlockAlignsToItsHead(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 5)
	m, _ = m.HandleEvent(noticeEvent("first"))
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "a", Name: "run_command"},
	})
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "a", Chunk: strings.Repeat("line\n", 20)},
	})
	m = m.SetAllCollapsed(false).FocusPrev()

	if !strings.Contains(ansi.Strip(m.View()), "run_command") {
		t.Errorf("a block taller than the viewport did not show its header:\n%s", ansi.Strip(m.View()))
	}
}

// TestUpdateLiveGrowsWithoutLosingAnything: a tool block may now grow to
// any size, because the viewport scrolls instead of evicting.
func TestUpdateLiveGrowsWithoutLosingAnything(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 5)
	m, _ = m.HandleEvent(noticeEvent("older"))
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "a", Name: "run_command"},
	})
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "a", Chunk: strings.Repeat("out\n", 30)},
	})
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "a", Name: "run_command", OK: true, Result: "done"},
	})

	if got := len(m.Blocks()); got != 2 {
		t.Fatalf("got %d blocks, want the notice and ONE block for the whole call", got)
	}
	if got := m.Blocks()[1].Header.State; got != "ok" {
		t.Errorf("got state %q, want the same block advanced to ok", got)
	}
	if !strings.Contains(ansi.Strip(m.Dump()), "older") {
		t.Error("the older block was lost when the tool block grew")
	}
}

func TestFindLiveMatchesNewestActiveBlock(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 20)
	// Turn 1 tool call "call-1" starts and ends
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "call-1", Name: "memory_search"},
	})
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "call-1", Name: "memory_search", OK: true, Result: "t1"},
	})
	// Turn 2 reuses the same tool call ID "call-1"
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "call-1", Name: "memory_search"},
	})
	// In flight: block 1 is ok, block 2 is running
	blocks := m.Blocks()
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	if blocks[0].Header.State != "ok" {
		t.Errorf("block 0 state = %q, want ok", blocks[0].Header.State)
	}
	if blocks[1].Header.State != "running" {
		t.Errorf("block 1 state = %q, want running", blocks[1].Header.State)
	}
	// Turn 2 tool call "call-1" ends
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "call-1", Name: "memory_search", OK: true, Result: "t2"},
	})
	blocks = m.Blocks()
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	if blocks[0].Header.State != "ok" {
		t.Errorf("block 0 state = %q, want ok", blocks[0].Header.State)
	}
	if blocks[1].Header.State != "ok" {
		t.Errorf("block 1 state = %q, want ok", blocks[1].Header.State)
	}
}

func TestWidthAndHeightReportTheViewport(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(120, 30)
	if got, want := m.Width(), 120; got != want {
		t.Errorf("got width %d, want %d", got, want)
	}
	if got, want := m.Height(), 30; got != want {
		t.Errorf("got height %d, want %d", got, want)
	}
}

// TestCollapseAllWithNoFocusKeepsTheOffsetValid covers the branch where
// there is nothing to re-anchor on: the offset must still be clamped
// against a conversation that just became much shorter.
func TestCollapseAllWithNoFocusKeepsTheOffsetValid(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 10)
	for i := 0; i < 6; i++ {
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
	m = m.ScrollToTop().ScrollBy(10)
	if m.Focused() {
		t.Fatal("this case is about having NO focused block")
	}

	m = m.SetAllCollapsed(true)
	if m.Offset() > m.TotalRows() {
		t.Errorf("offset %d is past the end of a %d-row conversation", m.Offset(), m.TotalRows())
	}
	if got := len(m.Rows()); got != 10 {
		t.Errorf("got %d rows after collapse-all, want the full viewport", got)
	}
}

// TestScrollToFocusIsANoOpWithoutAFocusedBlock covers the guard.
func TestScrollToFocusIsANoOpWithoutAFocusedBlock(t *testing.T) {
	m := sizedModel(t, 80, 10, 40).ScrollToTop()
	if got := m.ScrollToFocus().Offset(); got != 0 {
		t.Errorf("got offset %d, want the view left alone with nothing focused", got)
	}

	// An unmeasured viewport has no window to scroll within.
	u := New(loadTheme(t), theme.TierASCII)
	u, _ = u.HandleEvent(noticeEvent("x"))
	u = u.FocusPrev()
	if got := u.ScrollToFocus().Offset(); got != 0 {
		t.Errorf("got offset %d, want 0 with no measured viewport", got)
	}
}

// TestFocusBelowTheFoldScrollsDown covers the other arm of ScrollToFocus:
// a block past the bottom edge must come up into view.
func TestFocusBelowTheFoldScrollsDown(t *testing.T) {
	m := sizedModel(t, 80, 10, 40).ScrollToTop()
	m.focus = len(m.Blocks()) - 1
	m = m.syncFocus().ScrollToFocus()
	if !strings.Contains(ansi.Strip(m.View()), blockName(39)) {
		t.Errorf("the focused block below the fold did not scroll into view:\n%s", ansi.Strip(m.View()))
	}
}

// TestUnmeasuredModelStillAcceptsEvents covers the width-0 body path and
// the unmeasured clamp. The first events can arrive before Bubble Tea
// delivers a WindowSizeMsg, so nothing here may assume a size.
func TestUnmeasuredModelStillAcceptsEvents(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII) // no SetSize
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "a", Name: "run_command"},
	})
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "a", Chunk: "one\ntwo"},
	})
	if got := len(m.Blocks()); got != 1 {
		t.Fatalf("got %d blocks, want the events kept before the first size", got)
	}
	if m.Offset() != 0 {
		t.Errorf("got offset %d, want 0 on an unmeasured viewport", m.Offset())
	}
	// Once measured, everything is there.
	m.SetSize(80, 20)
	if !strings.Contains(ansi.Strip(m.Dump()), "two") {
		t.Error("content buffered before the first size was lost")
	}
}

// TestScrolledReaderIsClampedWhenTheConversationShrinks covers the
// clamp's upper arm: collapsing content out from under a scrolled reader
// must not leave them past the end.
func TestScrolledReaderIsClampedWhenTheConversationShrinks(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 5)
	for i := 0; i < 6; i++ {
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
	m = m.ScrollToTop().ScrollBy(12)
	m = m.SetAllCollapsed(true)
	if got, want := m.Offset(), m.TotalRows()-m.Height(); got > want && want >= 0 {
		t.Errorf("got offset %d, want at most %d after the conversation shrank", got, want)
	}
}

// TestFocusOnTheOldestBlockSurvivesTrim covers reindexFocus clamping to
// the front when the focused block is itself dropped.
func TestFocusOnTheOldestBlockSurvivesTrim(t *testing.T) {
	m := sizedModel(t, 80, 10, uikitconfig.MaxTranscriptLines)
	m = m.ScrollToTop()
	m.focus = 0
	m = m.syncFocus()

	m, _ = m.HandleEvent(noticeEvent("pushes-one-out"))
	if !m.Focused() {
		t.Fatal("focus was dropped when the focused block itself was trimmed")
	}
	if got := m.FocusIndex(); got != 0 {
		t.Errorf("got focus %d, want the oldest survivor", got)
	}
}

// TestFocusIsClearedWhenEveryBlockIsTrimmed covers the upper clamp in
// reindexFocus: with nothing left there is no index to hold.
func TestFocusIsClearedWhenEveryBlockIsTrimmed(t *testing.T) {
	m := sizedModel(t, 80, 10, 3)
	m = m.FocusPrev()
	if !m.Focused() {
		t.Fatal("expected a focused block")
	}
	// reindexFocus runs AFTER the blocks are removed, so the removal is
	// modelled here the same way trim does it.
	n := len(m.blocks)
	m.blocks = nil
	m.reindexFocus(n)
	if m.focus != -1 {
		t.Errorf("got focus %d, want -1 once nothing is left", m.focus)
	}
}

// TestBodyRowsAtUnknownWidthPassesThrough covers the width-0 arm: with
// no measured terminal there is no measure to wrap to, so the logical
// lines are returned unchanged rather than wrapped to a guess.
func TestBodyRowsAtUnknownWidthPassesThrough(t *testing.T) {
	b := Block{Header: Header{Label: "x"}, Body: []string{strings.Repeat("word ", 40)}}
	got := b.bodyRows(0)
	if len(got) != 1 || got[0] != b.Body[0] {
		t.Errorf("got %q, want the body unchanged at an unknown width", got)
	}
}

// TestCollapseAllRefocusesOnTheFocusedBlock covers the re-anchor arm:
// with a block focused, collapsing everything must keep that block on
// screen rather than leaving the reader at a stale row number.
func TestCollapseAllRefocusesOnTheFocusedBlock(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 6)
	for i := 0; i < 8; i++ {
		id := string(rune('a' + i))
		m, _ = m.HandleEvent(uievent.Event{
			Kind: uievent.KindToolStart,
			Body: uievent.ToolStartBody{ToolCallID: id, Name: "call-" + id},
		})
		m, _ = m.HandleEvent(uievent.Event{
			Kind: uievent.KindToolOutput,
			Body: uievent.ToolOutputBody{ToolCallID: id, Chunk: "one\ntwo\nthree"},
		})
	}
	// Focus a block near the start, then collapse everything.
	for i := 0; i < 6; i++ {
		m = m.FocusPrev()
	}
	name := m.Blocks()[m.FocusIndex()].Header.Label

	m = m.SetAllCollapsed(true)
	if !strings.Contains(ansi.Strip(m.View()), name) {
		t.Errorf("the focused block %q left the screen when everything collapsed:\n%s",
			name, ansi.Strip(m.View()))
	}
}

// TestTallFocusedBlockShowsItsHeadNotItsTail covers the inner clamp in
// ScrollToFocus. Aligning a block taller than the viewport to its BOTTOM
// would hide the header, which is the only thing that identifies it.
func TestTallFocusedBlockShowsItsHeadNotItsTail(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 5)
	m, _ = m.HandleEvent(noticeEvent("above"))
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "a", Name: "tall_call"},
	})
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "a", Chunk: strings.Repeat("row\n", 30)},
	})
	m = m.SetAllCollapsed(false)

	// Scroll away, then focus the tall block from above it.
	m = m.ScrollToTop()
	m.focus = 1
	m = m.syncFocus().ScrollToFocus()

	view := ansi.Strip(m.View())
	if !strings.Contains(view, "tall_call") {
		t.Errorf("the tall block's header is off screen:\n%s", view)
	}
	// Block 0 ("above") is a 1-row notice and block 1 is also activity,
	// so the group rule (R1) puts no blank row between them: block 1's
	// own top row starts at offset 1.
	if got, want := m.Offset(), 1; got != want {
		t.Errorf("got offset %d, want %d: the block's own top row", got, want)
	}
}

// TestNewWhilePausedCountsAndResets pins rule 6.7's count: blocks that
// arrive while the reader paused auto-follow are counted, the count is
// zero while following, and returning to the bottom clears it.
func TestNewWhilePausedCountsAndResets(t *testing.T) {
	// Enough blocks to overflow the viewport, or scrolling up cannot
	// pause follow at all (a short conversation has nothing to scroll).
	m := sizedModel(t, 80, 4, 20)
	if got := m.NewWhilePaused(); got != 0 {
		t.Fatalf("a following transcript counts %d, want 0", got)
	}

	m = m.ScrollBy(-1) // pause by scrolling up
	if m.Following() {
		t.Fatal("scrolling up must pause auto-follow")
	}
	for i := 0; i < 3; i++ {
		m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: "late"}})
	}
	if got := m.NewWhilePaused(); got != 3 {
		t.Errorf("counted %d new blocks while paused, want 3", got)
	}

	m = m.ScrollToBottom()
	if got := m.NewWhilePaused(); got != 0 {
		t.Errorf("after jumping to the bottom the count is %d, want 0", got)
	}
}

// TestScrollByToTheBottomResetsCount: reaching the bottom by scrolling
// (not the jump key) also clears the count.
func TestScrollByToTheBottomResetsCount(t *testing.T) {
	m := sizedModel(t, 80, 4, 20).ScrollBy(-1)
	for i := 0; i < 2; i++ {
		m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: "late"}})
	}
	if m.NewWhilePaused() != 2 {
		t.Fatalf("count = %d, want 2", m.NewWhilePaused())
	}
	m = m.ScrollBy(500) // past the bottom
	if !m.Following() || m.NewWhilePaused() != 0 {
		t.Errorf("scrolling to the bottom: following=%v count=%d, want true/0", m.Following(), m.NewWhilePaused())
	}
}

// TestMarkerOnlyWhereABodyExists pins transcript-polish.md R3: a marker
// may only be painted where there is a body to open. push() used to force
// Collapsible on every non-prose block, so a one-line notice or a
// one-line error carried a "v" with nothing under it. A body at or above
// the default threshold still starts collapsed.
func TestMarkerOnlyWhereABodyExists(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierASCII)
	m.SetSize(80, 40)
	m, _ = m.HandleEvent(noticeEvent("one-line notice"))
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindError, Body: uievent.ErrorBody{Text: "one-line error"}})
	long := "lifecycle hooks (20)"
	for i := 0; i < 20; i++ {
		long += fmt.Sprintf("\n  [%d] active", i)
	}
	m, _ = m.HandleEvent(noticeEvent(long))

	blocks := m.Blocks()
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3", len(blocks))
	}
	if blocks[0].Collapsible || len(blocks[0].Body) != 0 {
		t.Errorf("a one-line notice must be header-only and not collapsible: %+v", blocks[0])
	}
	if blocks[1].Collapsible || len(blocks[1].Body) != 0 {
		t.Errorf("a one-line error must be header-only and not collapsible: %+v", blocks[1])
	}
	for i, b := range []Block{blocks[0], blocks[1]} {
		row := ansi.Strip(strings.SplitN(b.Render(th, theme.TierASCII, 80), "\n", 2)[0])
		if row != "  "+b.Header.Label+" "+b.Header.Detail {
			t.Errorf("block %d header = %q, want a blank marker column", i, row)
		}
	}
	if !blocks[2].Collapsible {
		t.Error("a 20-line body must be collapsible")
	}
	if !blocks[2].Collapsed {
		t.Error("a 20-line body must start collapsed per defaultCollapsed")
	}
}

// TestToggleBlockAtScreenRow pins the click contract: the header row of
// a collapsed block expands it; other rows and off-screen rows do not.
func TestToggleBlockAtScreenRow(t *testing.T) {
	// One header-only notice plus one tool call with a body. Under R3
	// only the block WITH a body is collapsible, so it is the click
	// target; the header-only block stays a fall-through.
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 10)
	m, _ = m.HandleEvent(noticeEvent("header-only"))
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "a", Name: "run_command"},
	})
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "a", Chunk: "hidden-line"},
	})
	m = m.SetAllCollapsed(true)
	m.SetSize(80, 10)
	if len(m.Blocks()) == 0 {
		t.Fatal("precondition: a block exists")
	}
	if m.Blocks()[0].Collapsible {
		t.Fatal("precondition: the header-only block must not be collapsible")
	}
	if !m.Blocks()[1].Collapsed {
		t.Fatal("precondition: the tool block must start collapsed")
	}

	// A header-only block carries no expansion, so the click falls
	// through to the caller.
	if _, ok := m.ToggleBlockAtScreenRow(groupIndent, 0); ok {
		t.Error("clicking a header-only block must report nothing to expand")
	}

	// The tool block is the last one; scroll so its header is the first
	// visible row. Its header is at maxOffset.
	m = m.ScrollToBottom()
	target := len(m.Blocks()) - 1
	// Walk heights to the tool block's first row, then click the row
	// relative to the viewport.
	first := 0
	for i := range m.Blocks()[:target] {
		first += m.Blocks()[i].Height(80)
	}
	for m.Offset() > first {
		m = m.ScrollBy(-1)
	}
	click := first - m.Offset()

	next, ok := m.ToggleBlockAtScreenRow(groupIndent, click)
	if !ok {
		t.Fatal("click on the tool block header must expand it")
	}
	if next.Blocks()[target].Collapsed {
		t.Error("the clicked block must be expanded")
	}

	// A body row of the now-expanded block must not report anything.
	if _, ok := next.ToggleBlockAtScreenRow(groupIndent, click + 1); ok {
		t.Error("a body row must not report an expansion")
	}

	// Off-screen rows are refused.
	if _, ok := next.ToggleBlockAtScreenRow(groupIndent, 99); ok {
		t.Error("a row outside the viewport must be refused")
	}
	if _, ok := next.ToggleBlockAtScreenRow(groupIndent, -1); ok {
		t.Error("a negative row must be refused")
	}
}

// TestSetSizeRebuildsUserBlocksAtTheNewWidth pins the sidebar/reflow
// contract: a user turn's rows are width-styled (marker, indent, and a
// selection background that must reach the edge), so shrinking the
// surface cannot leave rows built at the old width - they would
// hard-wrap into a broken fill - and widening must extend the fill
// again rather than stop short.
func TestSetSizeRebuildsUserBlocksAtTheNewWidth(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{
		Input: "shrink me please shrink me please shrink me",
	}})

	m.SetSize(118, 30)
	wide := m.Rows()
	for _, r := range wide {
		if w := ansi.StringWidth(r); w > 118 {
			t.Fatalf("row wider than the surface after sizing: %d %q", w, ansi.Strip(r))
		}
	}

	m.SetSize(60, 30)
	narrow := m.Rows()
	if len(narrow) <= len(wide)-2 {
		t.Fatalf("narrowing did not re-wrap the user block: %d rows vs %d", len(narrow), len(wide))
	}
	for _, r := range narrow {
		if w := ansi.StringWidth(r); w > 60 {
			t.Fatalf("a row built at the old width survived the shrink: %d %q", w, ansi.Strip(r))
		}
	}

	// Round-trip: widening rebuilds the fill to the full surface again.
	m.SetSize(118, 30)
	again := m.Rows()
	for _, r := range again {
		if w := ansi.StringWidth(r); w > 118 {
			t.Fatalf("row exceeds the surface after re-widening: %d", w)
		}
	}
	if len(again) != len(wide) {
		t.Errorf("round-trip changed the row count: %d vs %d", len(again), len(wide))
	}
}
