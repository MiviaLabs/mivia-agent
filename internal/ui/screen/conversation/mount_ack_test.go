package conversation

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// ackLog records AckReceived firings from a mutex-free test fixture that
// runAllCmds deliberately invokes on separate goroutines - matching real
// bubbletea, which runs every Cmd in a tea.Batch concurrently (mount.go
// returns each buffered event's ack as its own Cmd in one batch). The real
// AckReceived closure (uiadapter/remote_input.go) only ever calls
// chatsync.SyncSession.MarkInputReceived, which is already safe for
// concurrent use; a plain `append` to a shared `[]string` from the test
// fixture is not, and raced under -race (and, without it, occasionally
// dropped one of two concurrent appends outright - the flake this replaces).
type ackLog struct {
	mu  sync.Mutex
	got []string
}

func (l *ackLog) add(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, name)
}

func (l *ackLog) slice() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.got...)
}

// ackCounter is ackLog's single-count sibling, for the many fixtures here
// that only need "did the ack fire, exactly once". Some of the Cmd batches
// under test (mount.go/remote_input.go) mix ackCmd with a Cmd that blocks
// forever in these fixtures (awaitSessionEvent/awaitRemoteInput reading a
// channel nothing ever signals) - runAllCmds tolerates that by design (a
// bounded sleep instead of waiting on its WaitGroup), but a sleep is not a
// happens-before edge, so the ack's write and the test's later read need
// their own synchronization regardless of how that sleep resolves.
type ackCounter struct {
	n atomic.Int32
}

func (c *ackCounter) inc()      { c.n.Add(1) }
func (c *ackCounter) load() int { return int(c.n.Load()) }

// TestHandleRemoteInput_BusyQueue_AckDecoupledFromSend drives one remote
// "message" while s.active != nil (busy foreground turn): the enqueue-vs-
// execute risk window. AckReceived (custody) must fire once the returned
// Cmd is invoked, well before the queued text is ever handed to Send - Send
// for the queued text only happens once the active turn's turnEndedMsg
// drains the queue (session.go).
func TestHandleRemoteInput_BusyQueue_AckDecoupledFromSend(t *testing.T) {
	th := testTheme()
	conv := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, conv, nil, 80, nil)

	// Put the foreground turn into a busy state by sending directly and
	// keeping the handle's events channel open (never closed): the turn
	// stays active until the test explicitly ends it.
	handle := &fakeTurnHandle{events: make(chan uievent.Event)}
	s.active = handle

	ackCount := &ackCounter{}
	ev := ports.RemoteInputEvent{
		SessionID: s.convID(), Kind: "message", Body: "queued while busy",
		AckReceived: ackCount.inc,
	}
	next, cmd := s.handleRemoteInput(ev)
	scr := next.(Screen)

	if len(scr.queue) != 1 || scr.queue[0] != "queued while busy" {
		t.Fatalf("expected the busy branch to queue the text, got %v", scr.queue)
	}
	if len(conv.sends) != 0 {
		t.Fatalf("Send must not be called while busy, got %d calls", len(conv.sends))
	}
	if got := ackCount.load(); got != 0 {
		t.Fatal("ack fired before the returned Cmd was ever invoked")
	}
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd carrying the ack")
	}

	// Invoke the returned Cmd (bubbletea would do this on its own
	// goroutine). rearm's read on s.remoteInputs (nil here, so
	// awaitRemoteInput already returned nil and rearm is nil too) never
	// blocks, so this batch resolves to just the ackCmd.
	runAllCmds(t, cmd)
	if got := ackCount.load(); got != 1 {
		t.Fatalf("ackCount after invoking the Cmd = %d, want 1", got)
	}
	if len(conv.sends) != 0 {
		t.Fatal("ack firing must not itself trigger Send")
	}

	// Now end the active turn: session.go's turnEndedMsg handling drains
	// the queue and finally calls Send for the queued text.
	next2, _ := scr.Update(turnEndedMsg{sessionID: s.convID()})
	scr2 := next2.(Screen)
	if len(conv.sends) != 1 || conv.sends[0].Text != "queued while busy" {
		t.Fatalf("expected Send to run exactly once for the queued text after turnEndedMsg, got %v", conv.sends)
	}
	_ = scr2
}

// TestHandleSessionMountedMsg_MountFailure_AcksAllBufferedEvents buffers
// 2+ ports.RemoteInputEvent fixtures, drives with msg.err != nil, and
// asserts every buffered event's AckReceived fires exactly once and the
// notice still shows.
func TestHandleSessionMountedMsg_MountFailure_AcksAllBufferedEvents(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)

	acked := &ackLog{}
	s.mounting = map[string][]ports.RemoteInputEvent{
		"bg-mount-fail": {
			{SessionID: "bg-mount-fail", Body: "first", AckReceived: func() { acked.add("first") }},
			{SessionID: "bg-mount-fail", Body: "second", AckReceived: func() { acked.add("second") }},
		},
	}

	msg := sessionMountedMsg{sessionID: "bg-mount-fail", err: errBoom}
	next, cmd := s.handleSessionMountedMsg(msg)
	if cmd == nil {
		t.Fatal("expected a non-nil batched Cmd carrying every buffered ack")
	}
	runAllCmds(t, cmd)
	if got := acked.slice(); len(got) != 2 {
		t.Fatalf("acked = %v, want both events acked", got)
	}
	if got := next.statusline.View(fixedNow()); got == "" {
		t.Fatal("expected the dropped-input notice to be set")
	}
}

// TestHandleSessionMountedMsg_ForegroundSuccess_AcksFirstEventAndRemaining
// covers the race-check success branch: the first event's ack rides
// sendOrQueueRemote's Cmd, remaining events' acks fire at their sc.queue
// append.
func TestHandleSessionMountedMsg_ForegroundSuccess_AcksFirstEventAndRemaining(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "fg-race"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)

	acked := &ackLog{}
	s.mounting = map[string][]ports.RemoteInputEvent{
		"fg-race": {
			{SessionID: "fg-race", Body: "first", AckReceived: func() { acked.add("first") }},
			{SessionID: "fg-race", Body: "second", AckReceived: func() { acked.add("second") }},
			{SessionID: "fg-race", Body: "third", AckReceived: func() { acked.add("third") }},
		},
	}

	msg := sessionMountedMsg{sessionID: "fg-race", conv: primary}
	next, cmd := s.handleSessionMountedMsg(msg)
	if cmd == nil {
		t.Fatal("expected a non-nil batched Cmd")
	}
	runAllCmds(t, cmd)

	if got := acked.slice(); len(got) != 3 {
		t.Fatalf("acked = %v, want all three events acked", got)
	}
	if len(primary.sends) != 1 || primary.sends[0].Text != "first" {
		t.Fatalf("expected exactly 1 Send call for the first event, got %v", primary.sends)
	}
	if len(next.queue) != 2 || next.queue[0] != "second" || next.queue[1] != "third" {
		t.Fatalf("expected remaining events queued, got %v", next.queue)
	}
}

// TestHandleSessionMountedMsg_BackgroundSuccess_AcksAtQueueAppend covers
// the background-tracked, already-busy path: remaining events' acks fire
// at st.queue append, and the first event's ack fires alongside its own
// queue append (the st.active != nil branch).
func TestHandleSessionMountedMsg_BackgroundSuccess_AcksAtQueueAppend(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)

	bgConv := &fakeMountConv{id: "bg-busy-mount"}
	st := s.newSessionState(bgConv)
	st.active = &fakeTurnHandle{}
	s.sessions = map[string]*sessionState{"bg-busy-mount": st}

	acked := &ackLog{}
	s.mounting = map[string][]ports.RemoteInputEvent{
		"bg-busy-mount": {
			{SessionID: "bg-busy-mount", Body: "first", AckReceived: func() { acked.add("first") }},
			{SessionID: "bg-busy-mount", Body: "second", AckReceived: func() { acked.add("second") }},
		},
	}

	msg := sessionMountedMsg{sessionID: "bg-busy-mount", conv: bgConv}
	next, cmd := s.handleSessionMountedMsg(msg)
	if cmd == nil {
		t.Fatal("expected a non-nil batched Cmd")
	}
	runAllCmds(t, cmd)

	if got := acked.slice(); len(got) != 2 {
		t.Fatalf("acked = %v, want both events acked", got)
	}
	if len(bgConv.sends) != 0 {
		t.Fatal("Send must not run while the background session is busy")
	}
	gotSt := next.sessions["bg-busy-mount"]
	if gotSt == nil || len(gotSt.queue) != 2 || gotSt.queue[0] != "second" || gotSt.queue[1] != "first" {
		t.Fatalf("expected queue [second first], got %v", gotSt.queue)
	}
}

// TestHandleSessionMountedMsg_BackgroundDirectSend_AcksOnSuccess covers the
// background direct-Send branch's success outcome: firstEvent.AckReceived
// fires exactly once.
func TestHandleSessionMountedMsg_BackgroundDirectSend_AcksOnSuccess(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)

	bgConv := &fakeMountConv{id: "bg-direct-mount"}

	ackCount := &ackCounter{}
	s.mounting = map[string][]ports.RemoteInputEvent{
		"bg-direct-mount": {
			{SessionID: "bg-direct-mount", Body: "hello", AckReceived: ackCount.inc},
		},
	}

	msg := sessionMountedMsg{sessionID: "bg-direct-mount", conv: bgConv}
	next, cmd := s.handleSessionMountedMsg(msg)
	if cmd == nil {
		t.Fatal("expected a non-nil batched Cmd")
	}
	runAllCmds(t, cmd)

	if got := ackCount.load(); got != 1 {
		t.Fatalf("ackCount = %d, want 1", got)
	}
	if len(bgConv.sends) != 1 {
		t.Fatalf("expected 1 Send call, got %d", len(bgConv.sends))
	}
	if next.sessions["bg-direct-mount"] == nil || next.sessions["bg-direct-mount"].active == nil {
		t.Fatal("expected st.active to be set on Send success")
	}
}

// TestHandleSessionMountedMsg_BackgroundDirectSend_AcksOnSendError is the
// failure-outcome twin.
func TestHandleSessionMountedMsg_BackgroundDirectSend_AcksOnSendError(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)

	failConv := &failingSendConv{fakeMountConv: &fakeMountConv{id: "bg-direct-mount-fail"}, err: errBoom}

	ackCount := &ackCounter{}
	s.mounting = map[string][]ports.RemoteInputEvent{
		"bg-direct-mount-fail": {
			{SessionID: "bg-direct-mount-fail", Body: "hello", AckReceived: ackCount.inc},
		},
	}

	msg := sessionMountedMsg{sessionID: "bg-direct-mount-fail", conv: failConv}
	next, cmd := s.handleSessionMountedMsg(msg)
	if cmd == nil {
		t.Fatal("expected a non-nil batched Cmd")
	}
	runAllCmds(t, cmd)

	if got := ackCount.load(); got != 1 {
		t.Fatalf("ackCount = %d, want 1", got)
	}
	if next.sessions["bg-direct-mount-fail"] == nil {
		t.Fatal("expected session state to have been created before the failed send")
	}
	if next.sessions["bg-direct-mount-fail"].active != nil {
		t.Fatal("expected st.active to remain nil on send failure")
	}
}

var errBoom = &boomError{}

type boomError struct{}

func (*boomError) Error() string { return "boom" }

// TestHandleRemoteInput_TrackedBackgroundBusy_QueuesAndAcks pins
// handleRemoteInput's own st.active != nil branch (as opposed to
// TestHandleSessionMountedMsg_BackgroundSuccess_AcksAtQueueAppend, which
// drives the mount flow's copy of the same shape): a remote input for an
// already-tracked, busy background session must queue the text and ack
// immediately, without calling Send.
func TestHandleRemoteInput_TrackedBackgroundBusy_QueuesAndAcks(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)

	bgConv := &fakeMountConv{id: "bg-tracked-busy"}
	st := s.newSessionState(bgConv)
	st.active = &fakeTurnHandle{}
	s.sessions = map[string]*sessionState{"bg-tracked-busy": st}

	ackCount := &ackCounter{}
	ev := ports.RemoteInputEvent{SessionID: "bg-tracked-busy", Body: "queued while busy", AckReceived: ackCount.inc}

	next, cmd := s.handleRemoteInput(ev)
	if cmd == nil {
		t.Fatal("expected a non-nil batched Cmd")
	}
	runAllCmds(t, cmd)

	if got := ackCount.load(); got != 1 {
		t.Fatalf("ackCount = %d, want 1", got)
	}
	if len(bgConv.sends) != 0 {
		t.Fatal("Send must not run while the tracked background session is busy")
	}
	gotSt := next.(Screen).sessions["bg-tracked-busy"]
	if gotSt == nil || len(gotSt.queue) != 1 || gotSt.queue[0] != "queued while busy" {
		t.Fatalf("expected queue [\"queued while busy\"], got %v", gotSt)
	}
}

// TestAwaitRemoteInput_ClosedChannelReturnsNilMsg pins awaitRemoteInput's
// own closed-channel branch: once the inbound steering channel closes
// (SessionPool shutdown), the read continuation must return a nil Msg
// rather than a zero-value remoteInputMsg, so the program loop does not
// route a fake empty event.
func TestAwaitRemoteInput_ClosedChannelReturnsNilMsg(t *testing.T) {
	th := testTheme()
	conv := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, conv, nil, 80, nil)

	ch := make(chan ports.RemoteInputEvent)
	s.remoteInputs = ch
	close(ch)

	cmd := s.awaitRemoteInput()
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd for a non-nil channel")
	}
	if msg := cmd(); msg != nil {
		t.Fatalf("awaitRemoteInput() on a closed channel = %#v, want nil", msg)
	}
}

// TestSendOrQueueRemote_RefreshesOpenQueueOverlay pins sendOrQueueRemote's
// own queueOverlay.Active() branch: a remote instruction queued while the
// operator has the queue overlay OPEN must refresh its item list live,
// not leave it showing the stale queue until the next manual toggle.
func TestSendOrQueueRemote_RefreshesOpenQueueOverlay(t *testing.T) {
	th := testTheme()
	conv := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, conv, nil, 80, nil)
	s.active = &fakeTurnHandle{}
	s.queueOverlay.Open(s.queue)
	if !s.queueOverlay.Active() {
		t.Fatal("precondition: queueOverlay did not open")
	}

	next, _ := s.sendOrQueueRemote("queued via remote", "", func() {})
	scr := next.(Screen)

	if !strings.Contains(scr.queueOverlay.View(), "queued via remote") {
		t.Fatalf("queueOverlay view = %q, want it refreshed with the newly queued text", scr.queueOverlay.View())
	}
}
