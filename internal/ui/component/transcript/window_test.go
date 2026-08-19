package transcript

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// noticeEvent is a one-row block: header only, no body.
func noticeEvent(text string) uievent.Event {
	return uievent.Event{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: text}}
}

func drain(t *testing.T, m Model, evs []uievent.Event) (Model, []string) {
	t.Helper()
	var committed []string
	for _, ev := range evs {
		next, c := m.HandleEvent(ev)
		m = next
		if c == nil {
			continue
		}
		if msg, ok := c().(CommitMsg); ok {
			committed = append(committed, msg.Text)
		}
	}
	return m, committed
}

// TestLiveWindowNeverExceedsBudget is the core invariant: View() must
// stay within the eviction budget no matter how much is streamed. A
// View taller than the terminal is the bug the whole architecture
// exists to prevent.
func TestLiveWindowNeverExceedsBudget(t *testing.T) {
	const height, reserved = 24, 4
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, height, reserved)

	// Heights vary so the resize path is exercised too, not only the
	// push path. SetSize used to record the new size without evicting,
	// so the window stayed over budget until the next event arrived.
	heights := []int{24, 6, 40, 5, 12, 24}
	h := height
	for i := 0; i < 200; i++ {
		next, _ := m.HandleEvent(noticeEvent("n" + strconv.Itoa(i)))
		m = next
		assertWithinBudget(t, m, i, h, reserved)

		if i%7 == 0 {
			h = heights[(i/7)%len(heights)]
			m.SetSize(80, h, reserved)
			assertWithinBudget(t, m, i, h, reserved)
		}
	}
}

// assertWithinBudget measures TERMINAL rows, not logical lines. A body
// line wider than the terminal draws on two rows, and counting newlines
// cannot see it.
func assertWithinBudget(t *testing.T, m Model, step, height, reserved int) {
	t.Helper()
	view := m.View()
	if view == "" {
		return
	}
	rows := 0
	for _, line := range strings.Split(view, "\n") {
		w := ansi.StringWidth(line)
		rows += max(1, (w+m.width-1)/m.width)
	}
	if rows > height-reserved {
		t.Fatalf("at step %d the view is %d terminal rows, budget is %d:\n%q",
			step, rows, height-reserved, view)
	}
}

// TestNoBlockIsLostOrDuplicated pins the other half: the union of what
// went to scrollback and what is still live must contain every block
// exactly once, in order.
func TestNoBlockIsLostOrDuplicated(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 24, 4)

	// Tokens are zero-padded and delimited so that no token is a prefix
	// of another. Plain "n5" is a substring of "n50", which let a lost
	// block pass as present.
	const n = 200
	token := func(i int) string { return fmt.Sprintf("n%03d|", i) }
	evs := make([]uievent.Event, 0, n)
	for i := 0; i < n; i++ {
		evs = append(evs, noticeEvent(token(i)))
	}
	m, committed := drain(t, m, evs)

	seen := ansi.Strip(strings.Join(committed, "\n") + "\n" + m.View())
	for i := 0; i < n; i++ {
		// Exactly once: zero means the block was lost, two or more means
		// it was printed to scrollback twice.
		if got := strings.Count(seen, token(i)); got != 1 {
			t.Fatalf("block %q appears %d times, want exactly 1", token(i), got)
		}
	}
	// And in order.
	for i := 1; i < n; i++ {
		if strings.Index(seen, token(i-1)) > strings.Index(seen, token(i)) {
			t.Fatalf("block %q appears after %q", token(i-1), token(i))
		}
	}
}

func TestEvictionPreservesOrder(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 6, 4) // budget 2

	evs := []uievent.Event{noticeEvent("first"), noticeEvent("second"), noticeEvent("third"), noticeEvent("fourth")}
	_, committed := drain(t, m, evs)

	all := strings.Join(committed, "\n")
	iFirst, iSecond := strings.Index(all, "first"), strings.Index(all, "second")
	if iFirst < 0 || iSecond < 0 {
		t.Fatalf("expected the oldest blocks evicted, got %q", all)
	}
	if iFirst > iSecond {
		t.Error("evicted blocks reached scrollback out of order")
	}
}

// TestZeroBudgetEvictsImmediately covers the pre-WindowSizeMsg state:
// height is 0, so every finalized block commits at once. That is the
// previous commit-on-finalize behaviour, which is known-good.
func TestZeroBudgetEvictsImmediately(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII) // no SetSize
	next, cmd := m.HandleEvent(noticeEvent("hello"))
	if cmd == nil {
		t.Fatal("expected an immediate commit at zero budget")
	}
	if !strings.Contains(cmd().(CommitMsg).Text, "hello") {
		t.Error("expected the block text in the commit")
	}
	if next.View() != "" {
		t.Errorf("got %q, want an empty live window at zero budget", next.View())
	}
}

func TestShrinkEvictsAndGrowDoesNotUnevict(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 24, 4)
	for i := 0; i < 5; i++ {
		next, _ := m.HandleEvent(noticeEvent("n" + strconv.Itoa(i)))
		m = next
	}
	before := len(m.Live())
	if before == 0 {
		t.Fatal("expected a populated live window")
	}

	// The shrink ITSELF must evict and return what it evicted. An
	// earlier version only recorded the new size, so the window stayed
	// over budget - and drew more rows than the terminal had - until the
	// next event happened to arrive.
	committed := m.SetSize(80, 6, 4) // budget 2
	if got := len(m.Live()); got > 2 {
		t.Errorf("got %d live blocks after the shrink, want at most 2", got)
	}
	if committed == "" {
		t.Error("the shrink evicted blocks but returned no text to commit")
	}
	if !strings.Contains(ansi.Strip(committed), "n0") {
		t.Errorf("expected the oldest block in the shrink commit, got %q", committed)
	}
	next, _ := m.HandleEvent(noticeEvent("shrink"))
	m = next

	m.SetSize(80, 40, 4)
	shrunk := len(m.Live())
	next, _ = m.HandleEvent(noticeEvent("grow"))
	m = next
	if len(m.Live()) != shrunk+1 {
		t.Errorf("growing un-evicted committed content: %d -> %d", shrunk, len(m.Live()))
	}
}

func TestRetainedRingHoldsEvictedBlocks(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 6, 4)
	for i := 0; i < 6; i++ {
		next, _ := m.HandleEvent(noticeEvent("n" + strconv.Itoa(i)))
		m = next
	}
	if len(m.Retained()) == 0 {
		t.Error("expected evicted blocks retained for the pager")
	}
}

func TestRetainedReturnsACopy(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 6, 4)
	for i := 0; i < 4; i++ {
		next, _ := m.HandleEvent(noticeEvent("n" + strconv.Itoa(i)))
		m = next
	}
	got := m.Retained()
	if len(got) == 0 {
		t.Skip("nothing retained yet")
	}
	got[0].Header.Label = "mutated"
	if m.Retained()[0].Header.Label == "mutated" {
		t.Error("Retained must return a copy")
	}
}

// TestNegativeBudgetClamps covers a terminal shorter than the chrome
// reserves. The budget must clamp to zero, not go negative and index
// backwards.
func TestNegativeBudgetClamps(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 2, 10) // reserved exceeds the height
	if got := m.budget(); got != 0 {
		t.Errorf("got budget %d, want 0", got)
	}
	next, cmd := m.HandleEvent(noticeEvent("hi"))
	if cmd == nil {
		t.Error("expected an immediate commit when nothing fits")
	}
	if next.View() != "" {
		t.Errorf("got %q, want an empty live window", next.View())
	}
}

// TestRetainedRingIsBounded pins that the ring cannot grow without
// limit. It holds at most config.MaxTranscriptLines blocks.
func TestRetainedRingIsBounded(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 5, 4) // budget 1, so almost everything evicts

	for i := 0; i < uikitconfig.MaxTranscriptLines+50; i++ {
		next, _ := m.HandleEvent(noticeEvent("n" + strconv.Itoa(i)))
		m = next
	}
	if got := len(m.Retained()); got > uikitconfig.MaxTranscriptLines {
		t.Errorf("got %d retained blocks, want at most %d", got, uikitconfig.MaxTranscriptLines)
	}
	// The newest survive, the oldest are dropped.
	retained := m.Retained()
	if len(retained) > 0 && retained[0].Header.Detail == "n0" {
		t.Error("expected the oldest blocks dropped from the ring first")
	}
}

// TestFocusFollowsEviction pins that focus never lands on nothing: when
// the focused block is evicted, focus moves to the oldest survivor.
func TestFocusFollowsEviction(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 8, 4) // budget 4
	for i := 0; i < 4; i++ {
		next, _ := m.HandleEvent(noticeEvent("n" + strconv.Itoa(i)))
		m = next
	}
	m.focus = 0 // oldest live block

	// Push enough to evict the focused one.
	for i := 4; i < 8; i++ {
		next, _ := m.HandleEvent(noticeEvent("n" + strconv.Itoa(i)))
		m = next
	}
	if m.focus < 0 {
		t.Fatal("focus was dropped entirely")
	}
	if m.focus >= len(m.Live()) {
		t.Fatalf("focus %d is out of range for %d live blocks", m.focus, len(m.Live()))
	}
	// "Follows" means the OLDEST SURVIVOR, not merely some valid index.
	// An unconditional focus = 0 also satisfies a range check, and it is
	// wrong the moment focus was not on the oldest block.
	if got := m.Live()[m.focus].Header.Detail; got != m.Live()[0].Header.Detail {
		t.Errorf("focus landed on %q, want the oldest survivor %q", got, m.Live()[0].Header.Detail)
	}
}

// TestFocusClampsPastEndOfLiveWindow covers the branch where eviction
// leaves fewer live blocks than the reindexed focus points at.
func TestFocusClampsPastEndOfLiveWindow(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 8, 4) // budget 4
	for i := 0; i < 4; i++ {
		next, _ := m.HandleEvent(noticeEvent("n" + strconv.Itoa(i)))
		m = next
	}
	// Focus the newest, then shrink hard so the window holds one block.
	m.focus = len(m.Live()) - 1
	m.SetSize(80, 5, 4) // budget 1
	next, _ := m.HandleEvent(noticeEvent("tiny"))
	m = next

	if m.focus >= len(m.Live()) {
		t.Errorf("focus %d is past the end of %d live blocks", m.focus, len(m.Live()))
	}
	if m.focus < 0 {
		t.Error("focus was dropped instead of clamped")
	}
}

func TestUnfocusedModelIsUnaffectedByEviction(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 6, 4)
	for i := 0; i < 6; i++ {
		next, _ := m.HandleEvent(noticeEvent("n" + strconv.Itoa(i)))
		m = next
	}
	if m.focus != -1 {
		t.Errorf("got focus %d, want -1 when nothing was focused", m.focus)
	}
}

func TestTallBlockCollapsesByDefault(t *testing.T) {
	body := make([]uievent.PlanItem, 20)
	for i := range body {
		body[i] = uievent.PlanItem{Text: "step " + strconv.Itoa(i)}
	}
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40, 4)
	next, _ := m.HandleEvent(uievent.Event{
		Kind: uievent.KindPlan,
		Body: uievent.PlanBody{Items: body, Total: len(body)},
	})
	live := next.Live()
	if len(live) != 1 {
		t.Fatalf("got %d live blocks, want 1", len(live))
	}
	if !live[0].Collapsed {
		t.Error("a block at or above the collapse threshold must start collapsed")
	}
	if live[0].Height(80) != 1 {
		t.Errorf("got height %d, want 1 for a collapsed block", live[0].Height(80))
	}
}

func TestProseBlockIsNotCollapsible(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40, 4)
	next, _ := m.HandleEvent(uievent.Event{
		Kind: uievent.KindTextEnd,
		Body: uievent.TextEndBody{Text: "a\nb\nc"},
	})
	live := next.Live()
	if len(live) != 1 {
		t.Fatalf("got %d live blocks, want 1", len(live))
	}
	if live[0].Collapsible || live[0].Collapsed {
		t.Error("assistant prose has no header, so it cannot collapse")
	}
}

// TestOneToolCallIsOneBlock pins wireframes-panes.md section 5's premise:
// pending -> running -> ok is one block whose header updates in place,
// not three stacked headers for a single call.
func TestOneToolCallIsOneBlock(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40, 4)

	const id = "call-1"
	for _, ev := range []uievent.Event{
		{Kind: uievent.KindToolPending, Body: uievent.ToolPendingBody{ToolCallID: id, Name: "edit"}},
		{Kind: uievent.KindToolStart, Body: uievent.ToolStartBody{ToolCallID: id, Name: "edit", Args: map[string]any{"path": "main.go"}}},
		{Kind: uievent.KindToolOutput, Body: uievent.ToolOutputBody{ToolCallID: id, Chunk: "line one\nline two"}},
		{Kind: uievent.KindToolEnd, Body: uievent.ToolEndBody{ToolCallID: id, Name: "edit", OK: true, Result: "48 lines", DurationMS: 12}},
	} {
		next, _ := m.HandleEvent(ev)
		m = next
	}

	live := m.Live()
	if len(live) != 1 {
		t.Fatalf("got %d blocks for one tool call, want 1", len(live))
	}
	blk := live[0]
	if blk.Header.State != "ok" {
		t.Errorf("got state %q, want the header advanced to ok", blk.Header.State)
	}
	if !strings.Contains(blk.Header.Detail, "main.go") {
		t.Errorf("got detail %q, want the path from the start event preserved", blk.Header.Detail)
	}
	if !strings.Contains(blk.Header.Meta, "48 lines") || !strings.Contains(blk.Header.Meta, "12ms") {
		t.Errorf("got meta %q, want the result and duration", blk.Header.Meta)
	}
	if len(blk.Body) != 2 {
		t.Errorf("got %d body lines, want the tool output kept in the call's own body", len(blk.Body))
	}
}

func TestToolEventsWithoutALiveBlockPushAFreshOne(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40, 4)
	// A tool.end whose call was never seen (already evicted, or a
	// truncated stream) must still surface, not vanish.
	next, _ := m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "unknown", Name: "edit", OK: false, Err: "boom"},
	})
	if len(next.Live()) != 1 {
		t.Fatalf("got %d blocks, want the orphan tool.end shown", len(next.Live()))
	}
}

// TestCancelledTurnKeepsPartialTextAndSaysWhy pins section 13: a turn
// that did not complete must keep whatever streamed and state the
// reason. Dropping it silently is the transcript lying.
func TestCancelledTurnKeepsPartialTextAndSaysWhy(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40, 4)

	next, _ := m.HandleEvent(uievent.Event{
		Kind: uievent.KindTextDelta,
		Body: uievent.TextDeltaBody{Text: "I will add a bounded ret"},
	})
	m = next
	next, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindTurnEnd,
		Body: uievent.TurnEndBody{Reason: "cancelled"},
	})
	m = next

	view := m.View()
	if !strings.Contains(view, "bounded ret") {
		t.Errorf("partial text was dropped on cancellation:\n%s", view)
	}
	if !strings.Contains(view, "cancelled") {
		t.Errorf("cancellation reason missing:\n%s", view)
	}
	if m.View() != "" && strings.Count(view, "bounded ret") != 1 {
		t.Errorf("partial text duplicated:\n%s", view)
	}
}

func TestCompletedTurnCommitsNoBlock(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40, 4)
	next, cmd := m.HandleEvent(uievent.Event{
		Kind: uievent.KindTurnEnd,
		Body: uievent.TurnEndBody{Reason: "completed"},
	})
	if cmd != nil {
		t.Error("a completed turn must commit nothing")
	}
	if len(next.Live()) != 0 {
		t.Errorf("got %d blocks, want none for a completed turn", len(next.Live()))
	}
}

// TestToolOutputGrowingPastThresholdCollapses pins that a block which
// grows past the collapse threshold while live closes itself, the same
// rule it would have received on first render.
func TestToolOutputGrowingPastThresholdCollapses(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 200, 4)

	const id = "call-1"
	next, _ := m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: id, Name: "run_command"},
	})
	m = next
	if m.Live()[0].Collapsed {
		t.Fatal("a short block should start open")
	}

	lines := make([]string, uikitconfig.CollapseThresholdLines+2)
	for i := range lines {
		lines[i] = "out " + strconv.Itoa(i)
	}
	next, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: id, Chunk: strings.Join(lines, "\n")},
	})
	m = next
	if !m.Live()[0].Collapsed {
		t.Error("a block that grew past the threshold must close itself")
	}
}

// TestToolEndWithDiffKeepsDiffHeader covers the branch where a tool end
// carries a diff: the path and counts come from the diff, not from the
// start event's args.
func TestToolEndWithDiffKeepsDiffHeader(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 200, 4)

	const id = "call-diff"
	next, _ := m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: id, Name: "edit", Args: map[string]any{"path": "x.go"}},
	})
	m = next
	next, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{
			ToolCallID: id, Name: "edit", OK: true, DurationMS: 31,
			Diff: &uievent.Diff{Path: "internal/x.go", Added: 4, Removed: 1, Hunks: []uievent.DiffHunk{
				{Header: "@@ -1 +1 @@", Lines: []uievent.DiffLine{{Kind: uievent.DiffLineAdd, Text: "a"}}},
			}},
		},
	})
	m = next

	blk := m.Live()[0]
	if blk.Header.Detail != "internal/x.go" {
		t.Errorf("got detail %q, want the diff path", blk.Header.Detail)
	}
	if !strings.Contains(blk.Header.Meta, "+4 -1") {
		t.Errorf("got meta %q, want the diff counts", blk.Header.Meta)
	}
	if len(blk.Body) == 0 {
		t.Error("expected the diff rendered into the block body")
	}
}

// TestCancelledTurnEvictsWhenTheWindowIsFull covers cancellation under
// a budget too small to hold the flushed partial text plus the reason
// block: both must still reach scrollback, in order.
func TestCancelledTurnEvictsWhenTheWindowIsFull(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 5, 4) // budget 1

	next, _ := m.HandleEvent(uievent.Event{
		Kind: uievent.KindTextDelta,
		Body: uievent.TextDeltaBody{Text: "partial words here"},
	})
	m = next
	next, cmd := m.HandleEvent(uievent.Event{
		Kind: uievent.KindTurnEnd,
		Body: uievent.TurnEndBody{Reason: "cancelled"},
	})
	m = next

	if cmd == nil {
		t.Fatal("expected a commit: the window cannot hold both blocks")
	}
	committed := cmd().(CommitMsg).Text
	seen := committed + "\n" + m.View()
	if !strings.Contains(seen, "partial words here") {
		t.Errorf("partial text lost across eviction:\n%s", seen)
	}
	if !strings.Contains(seen, "cancelled") {
		t.Errorf("cancellation reason lost across eviction:\n%s", seen)
	}
}

// TestCancelledTurnAtZeroBudget covers cancellation before any
// WindowSizeMsg: both the partial text and the reason evict on push.
func TestCancelledTurnAtZeroBudget(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII) // budget 0
	next, _ := m.HandleEvent(uievent.Event{
		Kind: uievent.KindTextDelta,
		Body: uievent.TextDeltaBody{Text: "partial"},
	})
	m = next
	_, cmd := m.HandleEvent(uievent.Event{
		Kind: uievent.KindTurnEnd,
		Body: uievent.TurnEndBody{Reason: "cancelled"},
	})
	if cmd == nil {
		t.Fatal("expected both blocks committed at zero budget")
	}
	got := cmd().(CommitMsg).Text
	iPartial, iReason := strings.Index(got, "partial"), strings.Index(got, "cancelled")
	if iPartial < 0 || iReason < 0 {
		t.Fatalf("expected both the partial text and the reason:\n%s", got)
	}
	if iPartial > iReason {
		t.Error("the reason was committed before the partial text it explains")
	}
}

// TestUnknownBodyIsIgnored covers the switch fallthrough: an event whose
// Body is nil must be a no-op, not a panic.
func TestUnknownBodyIsIgnored(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40, 4)
	next, cmd := m.HandleEvent(uievent.Event{Kind: uievent.Kind("bogus"), Body: nil})
	if cmd != nil {
		t.Error("expected no Cmd for an unhandled body")
	}
	if len(next.Live()) != 0 {
		t.Error("expected no block for an unhandled body")
	}
}

// TestToolEndResultPlacement pins where a tool result goes. A short one
// is a metric and sits in the meta column beside the duration. A long
// one is a message and goes in the body, because forcing it into the
// right-aligned meta clips the detail away entirely.
func TestToolEndResultPlacement(t *testing.T) {
	run := func(t *testing.T, result string) Block {
		t.Helper()
		m := New(loadTheme(t), theme.TierASCII)
		m.SetSize(80, 200, 4)
		const id = "c1"
		next, _ := m.HandleEvent(uievent.Event{
			Kind: uievent.KindToolStart,
			Body: uievent.ToolStartBody{ToolCallID: id, Name: "run_command", Args: map[string]any{"cmd": "go test"}},
		})
		m = next
		next, _ = m.HandleEvent(uievent.Event{
			Kind: uievent.KindToolEnd,
			Body: uievent.ToolEndBody{ToolCallID: id, Name: "run_command", OK: true, Result: result, DurationMS: 12},
		})
		return next.Live()[0]
	}

	short := run(t, "48 lines")
	if !strings.Contains(short.Header.Meta, "48 lines") {
		t.Errorf("got meta %q, want a short result in the meta column", short.Header.Meta)
	}
	if !strings.Contains(short.Header.Detail, "go test") {
		t.Errorf("got detail %q, want the command preserved", short.Header.Detail)
	}

	long := run(t, "s3_uploader_test.go:88: want 3 attempts, got 1")
	if strings.Contains(long.Header.Meta, "want 3 attempts") {
		t.Errorf("got meta %q, want a long result kept out of the meta column", long.Header.Meta)
	}
	if len(long.Body) == 0 || !strings.Contains(long.Body[0], "want 3 attempts") {
		t.Errorf("got body %q, want the long result in the body", long.Body)
	}
	if !strings.Contains(long.Header.Detail, "go test") {
		t.Errorf("got detail %q, want the command still visible", long.Header.Detail)
	}
}

// TestSubagentProgressBarRendersOnALiveBlock covers the live-update
// path: the bar must appear whether the progress event lands on an
// existing block or creates one.
func TestSubagentProgressBarRendersOnALiveBlock(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 200, 4)
	const id = "sub-1"
	next, _ := m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: id, Name: "subagent"},
	})
	m = next
	next, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: id, Progress: &uievent.Progress{
			Step: 2, TotalSteps: 3, Status: "running", Log: []string{"step a"},
		}},
	})
	m = next

	body := strings.Join(m.Live()[0].Body, "\n")
	if !strings.Contains(body, "#") || !strings.Contains(body, "%") {
		t.Errorf("got body %q, want the progress bar", body)
	}
	if !strings.Contains(body, "step a") {
		t.Errorf("got body %q, want the step log kept", body)
	}
}

// TestProgressWithoutTotalKeepsTheLog covers a progress event whose
// total is zero: there is no honest bar to draw, but the log must
// still reach the block body.
func TestProgressWithoutTotalKeepsTheLog(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 200, 4)
	const id = "sub-2"
	next, _ := m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: id, Name: "subagent"},
	})
	m = next
	next, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: id, Progress: &uievent.Progress{
			Step: 1, TotalSteps: 0, Status: "running", Log: []string{"only line"},
		}},
	})
	m = next

	body := strings.Join(m.Live()[0].Body, "\n")
	if strings.Contains(body, "#") {
		t.Errorf("got body %q, want no bar without a total", body)
	}
	if !strings.Contains(body, "only line") {
		t.Errorf("got body %q, want the log kept", body)
	}
}

// TestToolEndOnABlockWithNoDetail covers the branch where the start
// event carried no args, so there is no detail to preserve.
func TestToolEndOnABlockWithNoDetail(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 200, 4)
	const id = "c2"
	next, _ := m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: id, Name: "noop"},
	})
	m = next
	next, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: id, Name: "noop", OK: true, Result: "fine", DurationMS: 3},
	})
	blk := next.Live()[0]
	if blk.Header.State != "ok" {
		t.Errorf("got state %q, want ok", blk.Header.State)
	}
	if !strings.Contains(blk.Header.Detail, "fine") {
		t.Errorf("got detail %q, want the result as the detail when there was none", blk.Header.Detail)
	}
}

// TestHandleToolEventRejectsOtherBodies pins the helper's own guard.
// HandleEvent only routes the four tool kinds here, but the switch needs
// a fallthrough and it must be a no-op rather than a panic.
func TestHandleToolEventRejectsOtherBodies(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40, 4)
	next, cmd := m.handleToolEvent(uievent.NoticeBody{Text: "not a tool event"})
	if cmd != nil {
		t.Error("expected no Cmd for a body this helper does not own")
	}
	if len(next.Live()) != 0 {
		t.Error("expected no block")
	}
}
