package conversation

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// cancelTrackingHandle records whether Cancel was actually invoked, unlike
// compactionTestHandle's no-op Cancel - handleCompactionKey's real branch
// (s.compaction.Cancel() then arming compactionCancelRequested) needs a
// handle that can prove it was called.
type cancelTrackingHandle struct {
	events    chan ports.CompactionEvent
	cancelled *bool
}

func (h cancelTrackingHandle) Events() <-chan ports.CompactionEvent { return h.events }
func (h cancelTrackingHandle) Cancel()                              { *h.cancelled = true }

// TestHandleCompactionMessageNilHandleIsNoOp covers the guard at the top of
// handleCompactionMessage: a compaction event arriving after the handle was
// already cleared (e.g. a stale goroutine racing session teardown) must not
// panic or mutate state - it is a plain no-op.
func TestHandleCompactionMessageNilHandleIsNoOp(t *testing.T) {
	conv := &compactionTestConversation{}
	s := newScreen(t, conv, nil, nil)
	s.compaction = nil

	next, cmd := s.handleCompactionMessage(ports.CompactionEvent{SessionID: conv.ID(), Done: true})
	got := next.(Screen)
	if cmd != nil {
		t.Fatalf("cmd = %v, want nil for a nil compaction handle", cmd)
	}
	if got.compaction != nil {
		t.Fatal("nil-handle message must not resurrect a compaction handle")
	}
}

// TestHandleCompactionMessageMatchingSessionReachesEvent covers the pass-
// through line: once the guards clear (handle present, SessionID matches),
// handleCompactionMessage must hand off to handleCompactionEvent rather than
// silently dropping the event.
func TestHandleCompactionMessageMatchingSessionReachesEvent(t *testing.T) {
	conv := &compactionTestConversation{}
	s := newScreen(t, conv, nil, nil)
	s.compaction = compactionTestHandle{events: make(chan ports.CompactionEvent)}
	s.compactionSessionID = conv.ID()
	s.statusline.Start("compact", fixedNow())

	next, _ := s.handleCompactionMessage(ports.CompactionEvent{SessionID: conv.ID(), Done: true})
	got := next.(Screen)
	if got.compaction != nil {
		t.Fatal("matching-session Done event should have cleared the compaction handle via handleCompactionEvent")
	}
}

// TestHandleCompactionKeyFirstPressCancelsAndArms covers the first ctrl+c
// press while compacting: it must call Cancel on the live handle and arm
// compactionCancelRequested, without touching quit's own double-press state.
func TestHandleCompactionKeyFirstPressCancelsAndArms(t *testing.T) {
	conv := &compactionTestConversation{}
	s := newScreen(t, conv, nil, nil)
	cancelled := false
	s.compaction = cancelTrackingHandle{events: make(chan ports.CompactionEvent), cancelled: &cancelled}
	s.statusline.Start("compact", fixedNow())

	next, cmd, handled := s.handleCompactionKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	got := next.(Screen)
	if !handled {
		t.Fatal("first ctrl+c during compaction must be handled here, not fall through")
	}
	if cmd != nil {
		t.Fatalf("cmd = %v, want nil for the cancel-arming press", cmd)
	}
	if !cancelled {
		t.Fatal("first ctrl+c must call Cancel on the compaction handle")
	}
	if !got.compactionCancelRequested {
		t.Fatal("first ctrl+c must arm compactionCancelRequested")
	}
	if view := got.statusline.View(fixedNow()); !strings.Contains(view, "CANCEL") {
		t.Fatalf("statusline view = %q, want it to reflect the canceling label", view)
	}
}

// TestHandleCompactionKeySecondPressQuits covers the already-cancelling
// branch: a second ctrl+c must defer to Screen.quit instead of cancelling
// again.
func TestHandleCompactionKeySecondPressQuits(t *testing.T) {
	conv := &compactionTestConversation{}
	s := newScreen(t, conv, nil, nil)
	cancelled := false
	s.compaction = cancelTrackingHandle{events: make(chan ports.CompactionEvent), cancelled: &cancelled}
	s.compactionCancelRequested = true

	next, _, handled := s.handleCompactionKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	got := next.(Screen)
	if !handled {
		t.Fatal("second ctrl+c during compaction must still be handled here")
	}
	if cancelled {
		t.Fatal("second ctrl+c must not call Cancel again - it defers to quit")
	}
	if !got.quitArmed {
		t.Fatal("second ctrl+c must arm the shared quit sequence via Screen.quit")
	}
}

// TestNextCompactionEventDeliversQueuedEvent covers nextCompactionEvent's
// success path: the returned Cmd, once executed, wraps whatever event was
// waiting on the channel.
func TestNextCompactionEventDeliversQueuedEvent(t *testing.T) {
	h := compactionTestHandle{events: make(chan ports.CompactionEvent, 1)}
	h.events <- ports.CompactionEvent{Phase: "scan"}

	msg := nextCompactionEvent(h)()
	ev, ok := msg.(compactionEventMsg)
	if !ok {
		t.Fatalf("msg = %#v, want compactionEventMsg", msg)
	}
	if ev.event.Phase != "scan" {
		t.Fatalf("event.Phase = %q, want %q", ev.event.Phase, "scan")
	}
}

// TestNextCompactionEventClosedChannelSignalsDone covers the other branch:
// a closed events channel (the handle finishing without one last explicit
// Done event) must still be translated into a synthetic Done event rather
// than blocking forever or panicking on a receive from a closed channel.
func TestNextCompactionEventClosedChannelSignalsDone(t *testing.T) {
	h := compactionTestHandle{events: make(chan ports.CompactionEvent)}
	close(h.events)

	msg := nextCompactionEvent(h)()
	ev, ok := msg.(compactionEventMsg)
	if !ok {
		t.Fatalf("msg = %#v, want compactionEventMsg", msg)
	}
	if !ev.event.Done {
		t.Fatal("a closed events channel must synthesize a Done event")
	}
}

// TestHandleCompactionEventErrShowsFailureNotice covers the Done+Err branch:
// a failed compaction must surface the error text instead of any Notice.
func TestHandleCompactionEventErrShowsFailureNotice(t *testing.T) {
	conv := &compactionTestConversation{}
	s := newScreen(t, conv, nil, nil)
	s.compaction = compactionTestHandle{events: make(chan ports.CompactionEvent)}
	s.statusline.Start("compact", fixedNow())

	next, cmd := s.handleCompactionEvent(ports.CompactionEvent{Done: true, Err: errors.New("boom")})
	got := next.(Screen)
	if cmd != nil {
		t.Fatalf("cmd = %v, want nil once compaction is done", cmd)
	}
	view := got.statusline.View(fixedNow())
	if !strings.Contains(view, "compact failed: boom") {
		t.Fatalf("statusline view = %q, want it to contain the failure notice", view)
	}
}

// TestHandleCompactionEventNoticeShowsNotice covers the Done+Notice branch
// (no Err): a successful compaction with a summary notice must surface that
// notice text.
func TestHandleCompactionEventNoticeShowsNotice(t *testing.T) {
	conv := &compactionTestConversation{}
	s := newScreen(t, conv, nil, nil)
	s.compaction = compactionTestHandle{events: make(chan ports.CompactionEvent)}
	s.statusline.Start("compact", fixedNow())

	next, _ := s.handleCompactionEvent(ports.CompactionEvent{Done: true, Notice: "trimmed 40%"})
	got := next.(Screen)
	view := got.statusline.View(fixedNow())
	if !strings.Contains(view, "trimmed 40%") {
		t.Fatalf("statusline view = %q, want it to contain the completion notice", view)
	}
}

// TestHandleCompactionEventProgressUpdatesLabelAndPolls covers the non-Done
// path: a progress event must update the statusline's phase/detail and keep
// polling by returning another nextCompactionEvent Cmd rather than nil.
func TestHandleCompactionEventProgressUpdatesLabelAndPolls(t *testing.T) {
	conv := &compactionTestConversation{}
	s := newScreen(t, conv, nil, nil)
	s.compaction = compactionTestHandle{events: make(chan ports.CompactionEvent)}
	s.statusline.Start("compact", fixedNow())

	next, cmd := s.handleCompactionEvent(ports.CompactionEvent{Phase: "scanning", Detail: "reading files"})
	got := next.(Screen)
	if cmd == nil {
		t.Fatal("a non-Done event must keep polling by returning a non-nil Cmd")
	}
	view := got.statusline.View(fixedNow())
	if !strings.Contains(view, "SCANNING") {
		t.Fatalf("statusline view = %q, want it to reflect the updated phase label", view)
	}
	if !strings.Contains(view, "reading files") {
		t.Fatalf("statusline view = %q, want it to reflect the updated detail", view)
	}
}

// TestHandleCompactionEventProgressWithNilHandleStopsPolling covers the
// compaction==nil guard reached from a non-Done event: if the handle was
// already cleared (session switch race) a late progress event must stop
// polling instead of returning a Cmd that dereferences a nil handle.
func TestHandleCompactionEventProgressWithNilHandleStopsPolling(t *testing.T) {
	conv := &compactionTestConversation{}
	s := newScreen(t, conv, nil, nil)
	s.compaction = nil

	_, cmd := s.handleCompactionEvent(ports.CompactionEvent{Phase: "scanning"})
	if cmd != nil {
		t.Fatalf("cmd = %v, want nil once the compaction handle is gone", cmd)
	}
}
