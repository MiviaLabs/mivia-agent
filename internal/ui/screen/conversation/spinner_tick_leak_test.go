package conversation

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/statusline"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// runCmd expands one Cmd the way the bubbletea runtime does (batches fan
// out, every leaf runs) and returns the Msgs it produced. Leaf Cmds that do
// not deliver within the budget (a blocked port read) are dropped: this
// helper only counts the spinner clock.
func expandTickCmd(cmd tea.Cmd, budget time.Duration) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if batch, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Msg
			for _, c := range batch {
				out = append(out, expandTickCmd(c, budget)...)
			}
			return out
		}
		if msg == nil {
			return nil
		}
		return []tea.Msg{msg}
	case <-time.After(budget):
		return nil
	}
}

// countTicks reports how many statusline.TickMsg a Cmd delivers, i.e. how
// many independent spinner clocks are alive in that branch of the loop.
func countTicks(cmd tea.Cmd) int {
	n := 0
	for _, msg := range expandTickCmd(cmd, 2*time.Second) {
		if _, ok := msg.(statusline.TickMsg); ok {
			n++
		}
	}
	return n
}

// TestZeroValueScreenStillArmsOneClock pins armTick's nil-flag path: a
// Screen built as a bare struct (not through New) has no shared flag, and
// must still animate rather than returning a nil Cmd forever.
func TestZeroValueScreenStillArmsOneClock(t *testing.T) {
	var s Screen // deliberately not New: tickArmed is nil
	if cmd := s.armTick(); cmd == nil {
		t.Fatal("a Screen with no shared tick flag armed no clock, want one")
	}
}

// TestEmbeddedThreadTickDoesNotTouchTheSharedClock pins that the embedded
// thread screen advances its own frame but neither consumes nor re-arms
// the parent's clock. Its Cmd is discarded by forwardSharedMsg, so
// clearing the flag here would let the next arm start a SECOND clock
// beside the parent's still-in-flight tick.
func TestEmbeddedThreadTickDoesNotTouchTheSharedClock(t *testing.T) {
	parent := newScreen(t, replay.New(nil, 0), nil, nil)
	thread := NewThread(loadTheme(t), theme.TierASCII, replay.New(nil, 0), 60, fixedNow)
	thread.tickArmed = parent.tickArmed
	thread.statusline.Start("thinking", fixedNow())

	*parent.tickArmed = true // the parent's clock is in flight
	before := thread.statusline.Frame()

	next, cmd := thread.Update(statusline.TickMsg{})
	got, ok := next.(Screen)
	if !ok {
		t.Fatalf("Update returned %T, want Screen", next)
	}
	if cmd != nil {
		t.Error("the embedded thread screen re-armed the clock, want nil Cmd")
	}
	if !*parent.tickArmed {
		t.Error("the embedded thread screen consumed the parent's in-flight clock flag")
	}
	if got.statusline.Frame() == before {
		t.Error("the embedded thread screen did not advance its own frame")
	}
}

// TestOpenThreadTurnKeepsTheClockAlive pins the counterpart in
// hasActiveSession: since the embedded screen no longer sustains the
// clock itself, an open thread's in-flight turn has to keep the parent's
// loop running, or it would animate for exactly one frame.
func TestOpenThreadTurnKeepsTheClockAlive(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	if s.hasActiveSession() {
		t.Fatal("a fresh screen reports an active session")
	}

	thread := NewThread(loadTheme(t), theme.TierASCII, replay.New(nil, 0), 60, fixedNow)
	thread.active = newStableHandle("thread-turn")
	s.thread = &thread

	if !s.hasActiveSession() {
		t.Fatal("an open thread with an in-flight turn does not keep the clock alive")
	}
}

// TestSubagentProgressDoesNotMultiplySpinnerClocks pins that subagent
// progress events do not each arm their own spinner tick loop. The
// statusline clock is self-re-arming (every TickMsg returns the next
// TickCmd) and handleStatuslineTick keeps it alive while subagents run, so
// arming a second loop doubles the frame rate permanently: the sidebar and
// status bar animate N times too fast and the whole UI repaints N times per
// interval.
func TestSubagentProgressDoesNotMultiplySpinnerClocks(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)

	next, _ := s.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{
			ToolCallID: "dispatch_tasks-call-1",
			Name:       "dispatch_tasks",
			Args: map[string]any{
				"tasks": []any{
					map[string]any{"id": "task-a", "prompt": "a"},
					map[string]any{"id": "task-b", "prompt": "b"},
				},
			},
		},
	}})
	scr := next.(Screen)

	// Several progress updates arrive while no turn statusline is active
	// (the outer turn ended, or none was started for this surface) - the
	// ordinary case for a long dispatch_tasks batch.
	live := 0
	for i := 0; i < 5; i++ {
		next, cmd := scr.Update(uievent.EventMsg{Event: uievent.Event{
			Kind: uievent.KindToolOutput,
			Body: uievent.ToolOutputBody{
				ToolCallID: "dispatch_tasks-call-1:task-a",
				Progress:   &uievent.Progress{Status: "running"},
			},
		}})
		scr = next.(Screen)
		live += countTicks(cmd)
	}

	if live > 1 {
		t.Fatalf("subagent progress armed %d concurrent spinner clocks, want at most 1", live)
	}

	// Whatever is alive must stay exactly one clock across a tick cycle.
	for cycle := 0; cycle < 3; cycle++ {
		total := 0
		for i := 0; i < live; i++ {
			nxt, cmd := scr.Update(statusline.TickMsg{})
			scr = nxt.(Screen)
			total += countTicks(cmd)
		}
		if total > live {
			t.Fatalf("cycle %d: spinner clocks grew from %d to %d", cycle, live, total)
		}
		live = total
	}
}
