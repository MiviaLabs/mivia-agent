package transcript

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestCancelledTurnKeepsPartialTextAndSaysWhy pins section 13: a
// transcript that drops the partial text and gives no reason is lying
// about why it stopped.
func TestCancelledTurnKeepsPartialTextAndSaysWhy(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 24)
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindTextDelta,
		Body: uievent.TextDeltaBody{Text: "half a thought"},
	})
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindTurnEnd,
		Body: uievent.TurnEndBody{Reason: "cancelled"},
	})

	got := ansi.Strip(m.Dump())
	if !strings.Contains(got, "half a thought") {
		t.Errorf("the partial text was dropped:\n%s", got)
	}
	if !strings.Contains(got, "cancelled") {
		t.Errorf("the transcript does not say why the turn stopped:\n%s", got)
	}
	// Order matters: the text came first, then the reason.
	if strings.Index(got, "half a thought") > strings.Index(got, "cancelled") {
		t.Error("the reason is above the partial text it explains")
	}
}

func TestCancelledTurnWithNoPartialTextStillSaysWhy(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 24)
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindTurnEnd,
		Body: uievent.TurnEndBody{Reason: "cancelled"},
	})
	if got := len(m.Blocks()); got != 1 {
		t.Fatalf("got %d blocks, want just the reason", got)
	}
	if !strings.Contains(ansi.Strip(m.Dump()), "cancelled") {
		t.Error("the reason block is missing")
	}
}

func TestCompletedTurnAddsNoBlock(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 24)
	for _, reason := range []string{"", turnReasonCompleted} {
		next, _ := m.HandleEvent(uievent.Event{
			Kind: uievent.KindTurnEnd,
			Body: uievent.TurnEndBody{Reason: reason},
		})
		if got := len(next.Blocks()); got != 0 {
			t.Errorf("reason %q added %d blocks, want none: turn state belongs to the status row", reason, got)
		}
	}
}

// TestOneToolCallIsOneBlock pins the whole point of keying by CallID:
// pending, running and the result advance one header in place instead of
// stacking three blocks for one call.
func TestOneToolCallIsOneBlock(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 24)

	steps := []uievent.Event{
		{Kind: uievent.KindToolPending, Body: uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"}},
		{Kind: uievent.KindToolStart, Body: uievent.ToolStartBody{ToolCallID: "c1", Name: "run_command", Args: map[string]any{"command": "go test"}}},
		{Kind: uievent.KindToolOutput, Body: uievent.ToolOutputBody{ToolCallID: "c1", Chunk: "PASS"}},
		{Kind: uievent.KindToolEnd, Body: uievent.ToolEndBody{ToolCallID: "c1", Name: "run_command", OK: true, Result: "ok"}},
	}
	wantState := []string{"pending", "running", "running", "ok"}
	for i, ev := range steps {
		m, _ = m.HandleEvent(ev)
		if got := len(m.Blocks()); got != 1 {
			t.Fatalf("after step %d there are %d blocks, want exactly 1", i, got)
		}
		if got := m.Blocks()[0].Header.State; got != wantState[i] {
			t.Errorf("after step %d state is %q, want %q", i, got, wantState[i])
		}
	}
	if got := ansi.Strip(m.Dump()); !strings.Contains(got, "PASS") {
		t.Errorf("the tool output was lost as the block advanced:\n%s", got)
	}
}

// TestPendingTextFlushesBeforeAToolBlockStarts pins the sibling case to
// TestCancelledTurnKeepsPartialTextAndSaysWhy: a tool call that starts
// before the model's running text.delta reaches its text.end must not
// discard that partial prose. Losing it is what corrupted the row when a
// tool block's own first output line drew into the same slot.
func TestPendingTextFlushesBeforeAToolBlockStarts(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 24)
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindTextDelta,
		Body: uievent.TextDeltaBody{Text: "Checking the directory now."},
	})
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "c1", Name: "list_dir"},
	})

	got := ansi.Strip(m.Dump())
	if !strings.Contains(got, "Checking the directory now.") {
		t.Errorf("the pending text was dropped when the tool block started:\n%s", got)
	}
	if len(m.Blocks()) != 2 {
		t.Fatalf("got %d blocks, want 2: the flushed prose, then the tool block", len(m.Blocks()))
	}
	if m.Blocks()[0].Header.Label == "list_dir" {
		t.Error("the prose block must come before the tool block, not after")
	}
}

func TestToolEventsWithoutAStartPushAFreshBlock(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 24)
	for _, ev := range []uievent.Event{
		{Kind: uievent.KindToolOutput, Body: uievent.ToolOutputBody{ToolCallID: "orphan", Chunk: "x"}},
		{Kind: uievent.KindToolEnd, Body: uievent.ToolEndBody{ToolCallID: "other", Name: "edit", OK: true}},
	} {
		next, _ := m.HandleEvent(ev)
		if len(next.Blocks()) == 0 {
			t.Errorf("%v produced no block; an orphan event must still be shown", ev.Kind)
		}
	}
}

func TestUnknownBodyIsIgnored(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 24)
	next, cmd := m.HandleEvent(uievent.Event{Kind: "invented", Body: nil})
	if len(next.Blocks()) != 0 || cmd != nil {
		t.Error("an unknown body produced output")
	}
}

func TestHandleToolEventRejectsOtherBodies(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 24)
	next, cmd := m.handleToolEvent(uievent.NoticeBody{Text: "not a tool body"})
	if len(next.Blocks()) != 0 || cmd != nil {
		t.Error("handleToolEvent accepted a body that is not a tool body")
	}
}

// TestTallBlockCollapsesByDefault pins wireframes-panes.md section 5:
// open under the threshold, closed at or above it.
func TestTallBlockCollapsesByDefault(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 200)

	body := make([]uievent.PlanItem, uikitconfig.CollapseThresholdLines+1)
	for i := range body {
		body[i] = uievent.PlanItem{Text: "step"}
	}
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindPlan,
		Body: uievent.PlanBody{Items: body, Total: len(body)},
	})
	if !m.Blocks()[0].Collapsed {
		t.Error("a block at or above the collapse threshold must start collapsed")
	}
	// 1 header row, plus the trailing blank separator every block gets.
	if got := m.Blocks()[0].Height(80); got != 2 {
		t.Errorf("got height %d, want 2 for a collapsed block", got)
	}
}

func TestProseBlockIsNotCollapsible(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "a\nb\nc"}})
	if m.Blocks()[0].Collapsible {
		t.Error("prose has no header to collapse into")
	}
}

// TestTrimMovesFocusWithTheBlocks: when the bound drops blocks from the
// front, focus must follow a surviving block rather than silently point
// somewhere else.
func TestTrimMovesFocusWithTheBlocks(t *testing.T) {
	m := sizedModel(t, 80, 10, uikitconfig.MaxTranscriptLines)
	m = m.FocusPrev() // the newest block
	name := m.Blocks()[m.FocusIndex()].Header.Detail

	m, _ = m.HandleEvent(noticeEvent("pushes-one-out"))
	if !m.Focused() {
		t.Fatal("focus was dropped when the bound trimmed the front")
	}
	if got := m.Blocks()[m.FocusIndex()].Header.Detail; got != name {
		t.Errorf("focus moved from %q to %q; it must stay on the same block", name, got)
	}
}

// TestTrimKeepsTheReaderInPlace: dropping rows above the reader must
// shift the offset by the same amount, or the view jumps.
func TestTrimKeepsTheReaderInPlace(t *testing.T) {
	m := sizedModel(t, 80, 10, uikitconfig.MaxTranscriptLines)
	m = m.ScrollToTop().ScrollBy(5)
	before := ansi.Strip(m.View())

	m, _ = m.HandleEvent(noticeEvent("pushes-one-out"))
	if got := ansi.Strip(m.View()); got != before {
		t.Errorf("the view moved when the bound trimmed the front:\nbefore:\n%s\nafter:\n%s", before, got)
	}
}

// TestProgressWithoutABarStillShowsItsLog covers the degenerate progress
// event: no total means no honest bar, but the log still matters.
func TestProgressWithoutABarStillShowsItsLog(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 24)
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{
			ToolCallID: "a",
			Progress:   &uievent.Progress{Step: 1, TotalSteps: 0, Log: []string{"working"}},
		},
	})
	got := ansi.Strip(m.Dump())
	if !strings.Contains(got, "working") {
		t.Errorf("the progress log was dropped when there was no bar to draw:\n%s", got)
	}
	if strings.Contains(got, "%") {
		t.Errorf("a percentage was drawn with no total to compute it from:\n%s", got)
	}
}
