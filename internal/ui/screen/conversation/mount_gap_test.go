package conversation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// failingSendConv wraps fakeMountConv and always fails Send, used to drive
// the send-error branch in handleSessionMountedMsg (mount.go:89-96).
type failingSendConv struct {
	*fakeMountConv
	err error
}

func (c *failingSendConv) Send(ctx context.Context, in intent.Send) (ports.TurnHandle, error) {
	return nil, c.err
}

// TestMountSessionCmd_NilMounterReturnsNilCmd covers mount.go:21-23 - the
// nil-mounter guard on mountSessionCmd itself, exercised directly rather
// than through handleRemoteInput (which never calls mountSessionCmd when
// s.mounter is nil).
func TestMountSessionCmd_NilMounterReturnsNilCmd(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)

	if cmd := s.mountSessionCmd("some-session"); cmd != nil {
		t.Fatal("expected nil cmd when no mounter is set")
	}
}

// TestMountSessionCmd_ExecutesMountAndBuildsMsg covers mount.go:25-32 - the
// body of the tea.Cmd closure, which is only exercised by actually invoking
// the returned command (bubbletea would do this on its own goroutine; the
// existing remote_mount_test.go tests only check the cmd is non-nil).
func TestMountSessionCmd_ExecutesMountAndBuildsMsg(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)

	mounted := &fakeMountConv{id: "bg-1"}
	mounter := &fakeMounter{convs: map[string]ports.Conversation{"bg-1": mounted}}
	s.SetSessionMounter(mounter)

	cmd := s.mountSessionCmd("bg-1")
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}

	msg := cmd()
	sm, ok := msg.(sessionMountedMsg)
	if !ok {
		t.Fatalf("expected sessionMountedMsg, got %T", msg)
	}
	if sm.sessionID != "bg-1" {
		t.Errorf("got sessionID %q, want %q", sm.sessionID, "bg-1")
	}
	if sm.conv != mounted {
		t.Error("expected returned conv to be the mounted conversation")
	}
	if sm.err != nil {
		t.Errorf("expected nil err, got %v", sm.err)
	}
	if len(mounter.mounted) != 1 || mounter.mounted[0] != "bg-1" {
		t.Errorf("expected Mount called once with bg-1, got %v", mounter.mounted)
	}
}

// TestMountSessionCmd_PropagatesMountError covers the error path through
// the same closure body (mount.go:26-31), confirming err flows into the
// resulting message unchanged.
func TestMountSessionCmd_PropagatesMountError(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)

	mountErr := errors.New("boom")
	mounter := &fakeMounter{err: mountErr}
	s.SetSessionMounter(mounter)

	cmd := s.mountSessionCmd("bg-err")
	msg := cmd()
	sm := msg.(sessionMountedMsg)
	if sm.err != mountErr {
		t.Errorf("got err %v, want %v", sm.err, mountErr)
	}
	if sm.conv != nil {
		t.Error("expected nil conv on mount error")
	}
}

// TestHandleSessionMountedMsg_NoQueuedEvents covers mount.go:50-52 - the
// session is not present in s.mounting at all (e.g. a stray/duplicate
// sessionMountedMsg), so events is empty and the handler returns early
// without creating any session state.
func TestHandleSessionMountedMsg_NoQueuedEvents(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)

	mountedConv := &fakeMountConv{id: "untracked"}
	msg := sessionMountedMsg{sessionID: "untracked", conv: mountedConv}

	next, cmd := s.handleSessionMountedMsg(msg)
	if cmd != nil {
		t.Error("expected nil cmd when there are no queued events")
	}
	if next.sessions["untracked"] != nil {
		t.Error("expected no session state created when there are no queued events")
	}
}

// TestRemoteInput_MountRace_MultipleQueuedEventsGoToForegroundQueue covers
// mount.go:62-64 - the race branch's loop over events[1:], which the
// existing single-event race test (remote_mount_test.go) never reaches.
func TestRemoteInput_MountRace_MultipleQueuedEventsGoToForegroundQueue(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)

	mounter := &fakeMounter{}
	s.SetSessionMounter(mounter)

	ev1 := ports.RemoteInputEvent{SessionID: "bg-race2", Body: "first"}
	ev2 := ports.RemoteInputEvent{SessionID: "bg-race2", Body: "second"}
	ev3 := ports.RemoteInputEvent{SessionID: "bg-race2", Body: "third"}

	next, _ := s.handleRemoteInput(ev1)
	scr := next.(Screen)
	next2, _ := scr.handleRemoteInput(ev2)
	scr2 := next2.(Screen)
	next3, _ := scr2.handleRemoteInput(ev3)
	scr3 := next3.(Screen)

	bgConv := &fakeMountConv{id: "bg-race2"}
	scr3.switchConversation(bgConv)

	msg := sessionMountedMsg{sessionID: "bg-race2", conv: bgConv}
	sc, _ := scr3.handleSessionMountedMsg(msg)

	if len(sc.queue) != 2 || sc.queue[0] != "second" || sc.queue[1] != "third" {
		t.Fatalf("expected foreground queue [second third], got %v", sc.queue)
	}
	if len(bgConv.sends) != 1 || bgConv.sends[0].Text != "first" {
		t.Fatalf("expected only the first message sent, got %v", bgConv.sends)
	}
}

// TestHandleSessionMountedMsg_ExistingSessionAlreadyActive covers
// mount.go:84-87 - a session that already has an active turn (tracked in
// s.sessions from a prior mount) simply gets the new text appended to its
// queue instead of sending again.
func TestHandleSessionMountedMsg_ExistingSessionAlreadyActive(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)

	bgConv := &fakeMountConv{id: "bg-busy"}
	st := s.newSessionState(bgConv)
	st.active = &fakeTurnHandle{}
	s.sessions = map[string]*sessionState{"bg-busy": st}

	s.mounting = map[string][]ports.RemoteInputEvent{
		"bg-busy": {{SessionID: "bg-busy", Body: "queued while busy"}},
	}

	next, cmd := s.handleSessionMountedMsg(sessionMountedMsg{sessionID: "bg-busy", conv: bgConv})
	if cmd != nil {
		t.Error("expected nil cmd; no new send should be dispatched")
	}
	if len(bgConv.sends) != 0 {
		t.Errorf("expected no Send call, got %d", len(bgConv.sends))
	}
	gotSt := next.sessions["bg-busy"]
	if gotSt == nil {
		t.Fatal("expected session state to remain tracked")
	}
	if len(gotSt.queue) != 1 || gotSt.queue[0] != "queued while busy" {
		t.Errorf("expected queue to contain the pending text, got %v", gotSt.queue)
	}
}

// TestHandleSessionMountedMsg_SendFailureRecordsErrorEvent covers
// mount.go:89-96 - conv.Send failing for a background (not foreground, not
// yet tracked) session records an error turn event and returns a nil cmd,
// mirroring handleRemoteInput's identical branch for an already-tracked
// session (remote_input.go:124-131).
func TestHandleSessionMountedMsg_SendFailureRecordsErrorEvent(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)

	failConv := &failingSendConv{fakeMountConv: &fakeMountConv{id: "bg-fail"}, err: errors.New("send exploded")}
	mounter := &fakeMounter{convs: map[string]ports.Conversation{"bg-fail": failConv}}
	s.SetSessionMounter(mounter)

	ev := ports.RemoteInputEvent{SessionID: "bg-fail", Body: "hello"}
	next, _ := s.handleRemoteInput(ev)
	scr := next.(Screen)

	msg := sessionMountedMsg{sessionID: "bg-fail", conv: failConv}
	next2, cmd := scr.handleSessionMountedMsg(msg)
	if cmd != nil {
		t.Error("expected nil cmd on send failure")
	}
	st := next2.sessions["bg-fail"]
	if st == nil {
		t.Fatal("expected session state to have been created before the failed send")
	}
	if st.active != nil {
		t.Error("expected st.active to remain nil on send failure")
	}
	text := st.transcript.View()
	if !strings.Contains(text, "remote send failed") {
		t.Errorf("expected transcript to record the send failure, got: %q", text)
	}
}

// TestHandleSessionMountedMsg_InitializesNilSessionsMap covers mount.go:68-69
// - the lazy s.sessions init. New() already constructs a non-nil map, so no
// test built through it reaches this guard; forcing s.sessions back to nil
// exercises the defensive path directly, the same way this file's nil-
// receiver-style tests cover other constructor-guaranteed guards.
func TestHandleSessionMountedMsg_InitializesNilSessionsMap(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)
	s.sessions = nil
	s.mounting = map[string][]ports.RemoteInputEvent{
		"bg-new": {{SessionID: "bg-new", Body: "hello"}},
	}

	bgConv := &fakeMountConv{id: "bg-new"}
	next, _ := s.handleSessionMountedMsg(sessionMountedMsg{sessionID: "bg-new", conv: bgConv})
	if next.sessions == nil {
		t.Fatal("handleSessionMountedMsg did not initialize a nil sessions map")
	}
	if _, ok := next.sessions["bg-new"]; !ok {
		t.Fatal("expected the new background session to be tracked after init")
	}
}

// TestDroppedRemoteInputNotice_NoEvents covers mount.go:108-110 - the
// no-events branch of droppedRemoteInputNotice, unreachable from
// handleSessionMountedMsg (which never calls it with an empty slice) but
// directly testable as a pure helper.
func TestDroppedRemoteInputNotice_NoEvents(t *testing.T) {
	cause := errors.New("mount failed hard")
	got := droppedRemoteInputNotice("sess-1", cause, nil)
	want := "session sess-1 failed to mount: mount failed hard"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestDroppedRemoteInputNotice_TruncatesLongBodyAndCountsExtra covers
// mount.go:111-120: a body over maxQuoted is truncated with an ellipsis,
// and a "+N more" suffix appears when more than one event was dropped.
func TestDroppedRemoteInputNotice_TruncatesLongBodyAndCountsExtra(t *testing.T) {
	longBody := strings.Repeat("x", 100)
	events := []ports.RemoteInputEvent{
		{Body: longBody},
		{Body: "second"},
		{Body: "third"},
	}
	cause := errors.New("mount failed")
	got := droppedRemoteInputNotice("sess-2", cause, events)

	wantQuoted := strings.Repeat("x", 80) + "..."
	if !strings.Contains(got, wantQuoted) {
		t.Errorf("expected truncated body %q in notice, got %q", wantQuoted, got)
	}
	if !strings.Contains(got, "(+2 more)") {
		t.Errorf("expected '(+2 more)' suffix, got %q", got)
	}
	if !strings.Contains(got, "sess-2") || !strings.Contains(got, "mount failed") {
		t.Errorf("expected session id and cause in notice, got %q", got)
	}
}

// TestDroppedRemoteInputNotice_ShortBodySingleEvent is the complementary
// case to the truncation test above: a short body under maxQuoted is not
// truncated, and a single dropped event carries no "+N more" suffix.
func TestDroppedRemoteInputNotice_ShortBodySingleEvent(t *testing.T) {
	events := []ports.RemoteInputEvent{{Body: "short"}}
	cause := errors.New("nope")
	got := droppedRemoteInputNotice("sess-3", cause, events)
	if !strings.Contains(got, `"short"`) {
		t.Errorf("expected unquoted-truncated body in notice, got %q", got)
	}
	if strings.Contains(got, "more)") {
		t.Errorf("did not expect a '+N more' suffix for a single event, got %q", got)
	}
}
