package conversation

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/statusline"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
)

// TestClockLapsesAfterTurnEndWithLeftoverNotice pins that a leftover
// one-line notice does not keep the spinner clock alive.
//
// statusline.Active() is `m.active || m.notice != ""` - it answers "does
// this line draw anything", which is a RENDER question. The clock's
// lifetime predicates asked it an ACTIVITY question. A notice ("copied the
// block", "queue cleared") outlives the turn: Stop() only clears m.active,
// and the sole production reset is Start()'s. So after a turn ends with a
// notice on screen, hasActiveSession stayed true and every tick re-armed
// the next one - a full cockpit Update/View at SpinnerFPS, broadcast to
// every screen on the stack, forever, with nothing running.
//
// That is the exact repaint cost commit 506e5cd5 removed, reintroduced
// with no bound in time.
func TestClockLapsesAfterTurnEndWithLeftoverNotice(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = fakeHandle{id: "t1"}
	_ = s.statusline.Start("thinking", fixedNow())

	// Mid-turn the user copies a block; the notice outlives the turn.
	s.statusline.Notice("copied the block")

	next, _ := s.Update(turnEndedMsg{})
	got, ok := next.(Screen)
	if !ok {
		t.Fatalf("Update returned %T, want Screen", next)
	}
	if got.active != nil {
		t.Fatal("turn did not end")
	}
	if !got.statusline.Active() {
		t.Fatal("fixture invalid: the notice did not survive the turn end")
	}

	next, cmd := got.Update(statusline.TickMsg{})
	if _, ok := next.(Screen); !ok {
		t.Fatalf("Update returned %T, want Screen", next)
	}
	if cmd != nil {
		t.Error("a leftover notice re-armed the spinner clock; it now repaints the cockpit at 10 FPS forever with nothing running")
	}
}

// TestProgressArmsTheClockWhileANoticeIsShown pins the other half of the
// same root cause. The subagent-progress arm is guarded by
// `!s.statusline.Active()`, so while any notice is on screen it armed
// nothing - a dispatch batch would stream progress with frozen marks.
func TestProgressArmsTheClockWhileANoticeIsShown(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.panel.observeAgentGroupStart("call-1", []string{"call-1:task-a"}, nil)
	if s.panel.activeAgentCount() != 1 {
		t.Fatal("fixture invalid: no active subagent")
	}
	// No turn is active; only a notice is on screen.
	s.statusline.Notice("copied the block")

	if s.armTick() == nil {
		t.Fatal("fixture invalid: the clock was already armed")
	}
	s.disarmTick()

	if !s.hasActiveSession() {
		t.Error("a running subagent must keep the clock alive")
	}
	if s.statusline.Active() && s.statusline.Animating() {
		t.Error("a notice-only statusline must not report itself as animating")
	}
}
