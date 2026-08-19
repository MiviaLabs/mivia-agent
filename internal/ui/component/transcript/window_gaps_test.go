package transcript

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestConcurrentToolCallsStayDistinct pins that a tool block is keyed by
// ToolCallID. Resolving "the newest live block" instead passes every
// single-call test, and merges two concurrent calls into one block: B's
// output lands in A's body and B's state overwrites A's.
func TestConcurrentToolCallsStayDistinct(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40, 4)

	start := func(id, name string) uievent.Event {
		return uievent.Event{Kind: uievent.KindToolStart, Body: uievent.ToolStartBody{ToolCallID: id, Name: name}}
	}
	m, _ = m.HandleEvent(start("A", "read_file"))
	m, _ = m.HandleEvent(start("B", "run_command"))
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "A", Chunk: "output-for-A"},
	})
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "A", Name: "read_file", OK: true, Result: "48 lines"},
	})

	live := m.Live()
	if len(live) != 2 {
		t.Fatalf("got %d live blocks, want one per tool call", len(live))
	}
	a, b := live[0], live[1]
	if a.CallID != "A" || b.CallID != "B" {
		t.Fatalf("got call IDs %q and %q, want A then B", a.CallID, b.CallID)
	}
	if a.Header.State != "ok" {
		t.Errorf("call A state is %q, want ok", a.Header.State)
	}
	if b.Header.State == "ok" {
		t.Error("call B was marked ok by call A's tool.end")
	}
	if len(b.Body) != 0 {
		t.Errorf("call B body is %q, want empty: A's output leaked into it", b.Body)
	}
	if !strings.Contains(strings.Join(a.Body, "\n"), "output-for-A") {
		t.Errorf("call A body is %q, want A's own output", a.Body)
	}
}

// TestGrowthTriggeredEvictionReachesScrollback pins that when a live
// block grows and pushes older blocks out, those blocks are COMMITTED,
// not merely dropped. Discarding them silently loses transcript.
func TestGrowthTriggeredEvictionReachesScrollback(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 6, 4) // budget 2

	m, _ = m.HandleEvent(noticeEvent("old-notice"))
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "A", Name: "run_command"},
	})

	// One output line takes the block to two rows, which no longer fits
	// beside the notice.
	m, cmd := m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "A", Chunk: "l1"},
	})
	if cmd == nil {
		t.Fatal("growth past the budget evicted nothing and committed nothing")
	}
	if got := ansi.Strip(commitText(t, cmd)); !strings.Contains(got, "old-notice") {
		t.Errorf("got %q, want the evicted notice in the commit", got)
	}
}

// TestGrowingBlockDoesNotEvictItself pins the rule that keeps a tool call
// to one block. A block that grew past the budget used to commit ITSELF
// mid-flight: scrollback then held a header reading "running" forever,
// and tool.end found no live block and pushed a second header for the
// same call.
func TestGrowingBlockDoesNotEvictItself(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 6, 4) // budget 2

	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "A", Name: "run_command"},
	})
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "A", Chunk: "l1\nl2\nl3\nl4\nl5\nl6"},
	})

	live := m.Live()
	if len(live) != 1 {
		t.Fatalf("got %d live blocks, want the running call still live", len(live))
	}
	if !live[0].Collapsed {
		t.Error("a block that cannot fit the budget must collapse, not evict itself")
	}

	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "A", Name: "run_command", OK: true, Result: "done"},
	})
	live = m.Live()
	if len(live) != 1 {
		t.Fatalf("got %d live blocks after tool.end, want one block per call", len(live))
	}
	if live[0].Header.State != "ok" {
		t.Errorf("got state %q, want the same block advanced to ok", live[0].Header.State)
	}
}

// TestEvictionEvictsTheMinimum pins that eviction takes exactly what it
// must. Evicting one block more than necessary freezes content into
// scrollback while it is still on screen, which defeats the point of
// committing on eviction rather than on finalization.
func TestEvictionEvictsTheMinimum(t *testing.T) {
	const budget = 4
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, budget+4, 4)

	for i := 0; i < budget; i++ {
		m, _ = m.HandleEvent(noticeEvent("n" + strconv.Itoa(i)))
	}
	if got := len(m.Live()); got != budget {
		t.Fatalf("got %d live blocks, want the budget %d filled exactly", got, budget)
	}

	m, cmd := m.HandleEvent(noticeEvent("one-more"))
	if cmd == nil {
		t.Fatal("expected exactly one block evicted")
	}
	committed := ansi.Strip(commitText(t, cmd))
	if n := len(strings.Split(strings.TrimRight(committed, "\n"), "\n")); n != 1 {
		t.Errorf("evicted %d rows, want exactly 1:\n%q", n, committed)
	}
	if got := len(m.Live()); got != budget {
		t.Errorf("got %d live blocks, want the budget %d still filled", got, budget)
	}
}

// TestMultipleBlocksEvictedByOnePushArriveInOneOrderedMessage pins the
// ordering hazard the design comment names: tea.Batch documents "no
// ordering guarantees", so several evicted blocks must arrive as ONE
// message, already in order.
func TestMultipleBlocksEvictedByOnePushArriveInOneOrderedMessage(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 8, 4) // budget 4

	for _, name := range []string{"first", "second", "third", "fourth"} {
		m, _ = m.HandleEvent(noticeEvent(name))
	}

	// One hard shrink evicts three blocks at once.
	got := ansi.Strip(m.SetSize(80, 5, 4)) // budget 1
	if got == "" {
		t.Fatal("the shrink evicted nothing")
	}
	iF, iS, iT := strings.Index(got, "first"), strings.Index(got, "second"), strings.Index(got, "third")
	if iF < 0 || iS < 0 || iT < 0 {
		t.Fatalf("expected all three evicted blocks in one message, got %q", got)
	}
	if !(iF < iS && iS < iT) {
		t.Errorf("evicted blocks are out of order in the single message: %q", got)
	}
	if got := len(m.Live()); got != 1 {
		t.Errorf("got %d live blocks after the shrink, want only the newest", got)
	}
}

// TestBodyLineWiderThanTerminalCountsEveryRow pins the height accounting
// against the real terminal. A tool line twice the terminal width draws
// two rows; counting logical lines made the budget nominal, and the view
// outgrew the terminal.
func TestBodyLineWiderThanTerminalCountsEveryRow(t *testing.T) {
	const width = 40
	b := Block{
		Header: Header{Label: "run_command"},
		Body:   []string{strings.Repeat("x", 100)},
	}
	// 100 columns of body at an indent of 4 needs 36-column rows: three
	// of them, plus the header.
	if got, want := b.Height(width), 1+3; got != want {
		t.Errorf("got height %d, want %d", got, want)
	}
	rows := strings.Split(b.Render(loadTheme(t), theme.TierASCII, width), "\n")
	if len(rows) != b.Height(width) {
		t.Errorf("Render drew %d rows but Height reported %d", len(rows), b.Height(width))
	}
	for i, row := range rows {
		if w := ansi.StringWidth(row); w > width {
			t.Errorf("row %d is %d columns, wider than the %d-column terminal", i, w, width)
		}
	}
}

// TestProseReflowsOnResize pins that prose is stored unwrapped. Wrapping
// at push time froze the measure taken then, so a later shrink left rows
// wider than the terminal that Height could not account for.
func TestProseReflowsOnResize(t *testing.T) {
	b := Block{Prose: true, Body: []string{strings.Repeat("word ", 30)}}

	wide := b.Height(120)
	narrow := b.Height(40)
	if narrow <= wide {
		t.Errorf("prose did not reflow: %d rows at 120 columns, %d at 40", wide, narrow)
	}
	for _, row := range b.bodyRows(40) {
		if w := ansi.StringWidth(row); w > 40 {
			t.Errorf("row is %d columns after a shrink to 40: %q", w, row)
		}
	}
}

// TestRetainedBodyIsNotAliased pins the deep copy. Block.Body is a
// slice, so copying the Block struct shares the backing array, and a
// caller mutating a row would write through into the retained ring.
func TestRetainedBodyIsNotAliased(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 5, 4) // budget 1

	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindError,
		Body: uievent.ErrorBody{Text: "line-one\nline-two", Fatal: false},
	})
	m, _ = m.HandleEvent(noticeEvent("push-it-out"))

	got := m.Retained()
	if len(got) == 0 || len(got[0].Body) == 0 {
		t.Fatal("expected a retained block with a body")
	}
	got[0].Body[0] = "mutated"
	if again := m.Retained(); again[0].Body[0] == "mutated" {
		t.Error("mutating a returned body wrote through into the retained ring")
	}
}

// TestFocusReturnsToTheComposerWhenEverythingEvicts covers the upper
// clamp in reindexFocus. When the live window empties there is no index
// to hold, so focus must return to the composer rather than point at a
// block that is no longer there.
func TestFocusReturnsToTheComposerWhenEverythingEvicts(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 6, 4) // budget 2
	m, _ = m.HandleEvent(noticeEvent("a"))
	m, _ = m.HandleEvent(noticeEvent("b"))
	m.focus = 1

	// A zero budget evicts every block, including the newest.
	m.SetSize(80, 4, 4)
	if len(m.Live()) != 0 {
		t.Fatalf("got %d live blocks, want an empty window", len(m.Live()))
	}
	if m.focus != -1 {
		t.Errorf("got focus %d, want -1 (the composer) with nothing live", m.focus)
	}
}

// TestSingleBlockTallerThanTheBudgetAtZeroBudget pins the unmeasured
// window. Nothing may be collapsed there: every block commits on push,
// and a collapsed commit would hide a body the user never gets to see.
func TestSingleBlockTallerThanTheBudgetAtZeroBudget(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII) // no SetSize: budget 0
	m, cmd := m.HandleEvent(uievent.Event{
		Kind: uievent.KindError,
		Body: uievent.ErrorBody{Text: "line-one\nline-two", Fatal: false},
	})
	got := ansi.Strip(commitText(t, cmd))
	for _, want := range []string{"line-one", "line-two"} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(got, want) {
				t.Errorf("committed block is missing %q, so the body was hidden:\n%s", want, got)
			}
		})
	}
	if len(m.Live()) != 0 {
		t.Errorf("got %d live blocks, want none at a zero budget", len(m.Live()))
	}
}
