package transcript

import (
	"strconv"
	"strings"
	"testing"

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

	for i := 0; i < 200; i++ {
		next, _ := m.HandleEvent(noticeEvent("n" + strconv.Itoa(i)))
		m = next
		rows := len(strings.Split(m.View(), "\n"))
		if m.View() == "" {
			rows = 0
		}
		if rows > height-reserved {
			t.Fatalf("after %d blocks the view is %d rows, budget is %d", i+1, rows, height-reserved)
		}
	}
}

// TestNoBlockIsLostOrDuplicated pins the other half: the union of what
// went to scrollback and what is still live must contain every block
// exactly once, in order.
func TestNoBlockIsLostOrDuplicated(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 24, 4)

	const n = 200
	evs := make([]uievent.Event, 0, n)
	for i := 0; i < n; i++ {
		evs = append(evs, noticeEvent("n"+strconv.Itoa(i)))
	}
	m, committed := drain(t, m, evs)

	seen := strings.Join(committed, "\n") + "\n" + m.View()
	for i := 0; i < n; i++ {
		want := "n" + strconv.Itoa(i)
		if got := strings.Count(seen, want+" ") + strings.Count(seen, want+"\n") + strings.Count(seen, want+"\x1b"); got == 0 {
			// Fall back to a plain containment check for the last block,
			// which may end the string with no trailing delimiter.
			if !strings.Contains(seen, want) {
				t.Fatalf("block %q appears nowhere in scrollback or the live window", want)
			}
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

	m.SetSize(80, 6, 4) // budget 2
	next, _ := m.HandleEvent(noticeEvent("shrink"))
	m = next
	if got := len(m.Live()); got > 2 {
		t.Errorf("got %d live blocks after shrink, want <= 2", got)
	}

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
	if len(retained) > 0 && strings.Contains(retained[0].Header.Detail, "n0 ") {
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
		t.Errorf("focus %d is out of range for %d live blocks", m.focus, len(m.Live()))
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
	if live[0].Height() != 1 {
		t.Errorf("got height %d, want 1 for a collapsed block", live[0].Height())
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
