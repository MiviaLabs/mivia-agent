// live_view_test.go pins the screen side of the live view: a switch to a
// background conversation subscribes and streams, ownership markers
// follow the screen, a run-active session refuses sends (composer text
// intact, remote ack fired), and the Stale/Done signals do what their
// contract says - reload-and-resubscribe, and a silent stop.
package conversation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// fakeLiveConv is a ports.Conversation with the live-view capabilities:
// BackgroundConversation, ForegroundMarker, and LiveEventsConversation.
type fakeLiveConv struct {
	id         string
	background bool
	foreground bool
	sendErr    error
	sends      []intent.Send
	subscribeN int
	history    []ports.Message

	sub    *fakeLiveSub
	events chan uievent.Event
}

func newFakeLiveConv(id string) *fakeLiveConv {
	return &fakeLiveConv{id: id}
}

func (c *fakeLiveConv) IsBackground() bool        { return c.background }
func (c *fakeLiveConv) SetForeground(on bool)     { c.foreground = on }
func (c *fakeLiveConv) IsForeground() bool        { return c.foreground }
func (c *fakeLiveConv) ID() string                { return c.id }
func (c *fakeLiveConv) Title() string             { return c.id }
func (c *fakeLiveConv) Model() ports.ModelInfo    { return ports.ModelInfo{Name: "test"} }
func (c *fakeLiveConv) ContextUsage() ports.Usage { return ports.Usage{} }
func (c *fakeLiveConv) History() []ports.Message  { return c.history }
func (c *fakeLiveConv) ActiveTurn() (ports.TurnHandle, bool) {
	return nil, false
}

func (c *fakeLiveConv) Send(_ context.Context, in intent.Send) (ports.TurnHandle, error) {
	c.sends = append(c.sends, in)
	if c.sendErr != nil {
		return nil, c.sendErr
	}
	ch := make(chan uievent.Event)
	close(ch)
	return &fakeTurnHandle{events: ch}, nil
}

func (c *fakeLiveConv) SubscribeLive() (<-chan uievent.Event, ports.LiveSubscription) {
	c.subscribeN++
	c.events = make(chan uievent.Event, 32)
	c.sub = newFakeLiveSub(c.events)
	return c.events, c.sub
}

// emit pushes one event into the viewer channel; buffered at 32, so the
// overflow test just sends more than that without reading.
func (c *fakeLiveConv) emit(ev uievent.Event) { c.sub.events <- ev }

// fakeLiveSub is the viewer-side subscription handle.
type fakeLiveSub struct {
	once   sync.Once
	done   chan struct{}
	stale  chan struct{}
	events chan uievent.Event
}

func newFakeLiveSub(events chan uievent.Event) *fakeLiveSub {
	return &fakeLiveSub{done: make(chan struct{}), stale: make(chan struct{}), events: events}
}

func (s *fakeLiveSub) Done() <-chan struct{}  { return s.done }
func (s *fakeLiveSub) Stale() <-chan struct{} { return s.stale }
func (s *fakeLiveSub) Close() {
	s.once.Do(func() { close(s.done) })
}

func TestSwitchToBackgroundConversationSubscribesAndTakesOwnership(t *testing.T) {
	primary := &fakeMountConv{id: "primary"}
	scr := newScreen(t, primary, nil, nil)
	bg := newFakeLiveConv("bg-run")
	bg.background = true

	cmd := scr.switchConversation(bg)
	if cmd == nil {
		t.Fatal("switching to a background conversation must arm the live view (non-nil cmd)")
	}
	if scr.liveSub == nil {
		t.Fatal("live subscription not stored on the screen")
	}
	if !bg.foreground {
		t.Fatal("an adopted background conversation must be marked foreground (its dispatches belong on the panel)")
	}
	if bg.subscribeN != 1 {
		t.Fatalf("SubscribeLive called %d times, want 1", bg.subscribeN)
	}
}

// TestSwitchAwayReleasesOwnership pins the slice-0 regression fix: the
// background flag marked a conversation forever; ownership now follows
// the screen, so the previous session stops being foreground the moment
// the user switches away.
func TestSwitchAwayReleasesOwnership(t *testing.T) {
	primary := &fakeMountConv{id: "primary"}
	scr := newScreen(t, primary, nil, nil)
	bg := newFakeLiveConv("bg-own")
	bg.background = true

	scr.switchConversation(bg)
	if !bg.foreground {
		t.Fatal("adopted conversation must be foreground")
	}
	other := &fakeMountConv{id: "other"}
	scr.switchConversation(other)
	if bg.foreground {
		t.Fatal("a conversation the screen switched away from must stop being foreground")
	}
	_ = other
}

func TestSwitchRestoresForegroundOwnership(t *testing.T) {
	convA := newFakeLiveConv("conv-a")
	convA.SetForeground(true)
	convB := newFakeLiveConv("conv-b")
	convB.SetForeground(true)

	scr := newScreen(t, convA, nil, nil)
	scr.switchConversation(convB)
	if convA.IsForeground() {
		t.Fatal("switching away from A must mark A not foreground")
	}
	if !convB.IsForeground() {
		t.Fatal("switching to B must mark B foreground")
	}

	scr.switchConversation(convA)
	if !convA.IsForeground() {
		t.Fatal("switching back to A must restore A's foreground ownership")
	}
	if convB.IsForeground() {
		t.Fatal("switching back to A must leave B not foreground")
	}
}

func TestRevisitMidRunArmsLiveViewAndStatusline(t *testing.T) {
	primary := &fakeMountConv{id: "primary"}
	scr := newScreen(t, primary, nil, nil)
	bg := newFakeLiveConv("bg-revisit")
	bg.background = true

	// A run is active on bg-revisit.
	scr.SetRunActivitySource(func(id string) bool { return id == "bg-revisit" })

	// Initial visit: subscribes and stores live view.
	scr.switchConversation(bg)
	if scr.liveSub == nil {
		t.Fatal("expected liveSub non-nil on first visit")
	}

	// Switch away to primary: bg-revisit live view rides the snapshot.
	scr.switchConversation(primary)
	st := scr.sessions["bg-revisit"]
	if st == nil || st.live == nil {
		t.Fatal("expected bg-revisit tracked with live subscription")
	}

	// Live subscription goes stale off-screen (e.g. buffer overflow).
	next, _ := scr.Update(liveStaleMsg{sessionID: "bg-revisit", sub: st.live})
	scr = next.(Screen)
	if st.live != nil {
		t.Fatal("expected st.live cleared after stale message")
	}

	// User revisits mid-run: replayOrResumeLive must re-arm live view and statusline.
	cmd := scr.switchConversation(bg)
	if cmd == nil {
		t.Fatal("expected non-nil cmd on revisit mid-run")
	}
	if scr.liveSub == nil {
		t.Fatal("expected liveSub non-nil on screen after revisit mid-run")
	}
	if !scr.statusline.Animating() {
		t.Fatal("expected statusline animating on revisit mid-run")
	}
	if v := scr.statusline.View(fixedNow()); !strings.Contains(v, "AUTO") {
		t.Fatalf("status row %q, want AUTO badge", v)
	}
}

func TestRunActiveGuardRefusesComposerSendAndKeepsText(t *testing.T) {
	primary := &fakeMountConv{id: "primary"}
	scr := newScreen(t, primary, nil, nil)
	bg := newFakeLiveConv("bg-guard")
	bg.background = true

	active := false
	scr.SetRunActivitySource(func(string) bool { return active })

	scr.switchConversation(bg)

	// The user types while the run is active, then submits: refused, and
	// the typed text stays in the composer.
	active = true
	typed := typeText(t, scr, "hello")
	guarded, _ := typed.send()
	g := guarded.(Screen)
	if len(bg.sends) != 0 {
		t.Fatalf("send went through while a run owned the session (%d sends)", len(bg.sends))
	}
	if got := g.composer.SubmitText(); got != "hello" {
		t.Fatalf("composer text %q after a refused submit, want it preserved", got)
	}

	// The run finishes: the same submit now goes through.
	active = false
	after, _ := scr.send()
	if len(bg.sends) != 1 {
		t.Fatalf("send refused after the run finished (%d sends)", len(bg.sends))
	}
	_ = after
}

func TestRunActiveGuardRefusesRemoteDirectSendAndAcks(t *testing.T) {
	primary := &fakeMountConv{id: "primary"}
	scr := newScreen(t, primary, nil, nil)
	bg := newFakeLiveConv("bg-remote")
	bg.background = true

	ackN := 0
	ack := func() { ackN++ }

	// Track the background session without switching to it.
	st := scr.newSessionState(bg)
	if scr.sessions == nil {
		scr.sessions = make(map[string]*sessionState)
	}
	scr.sessions["bg-remote"] = st

	scr.SetRunActivitySource(func(id string) bool { return id == "bg-remote" })
	ev := ports.RemoteInputEvent{
		ID: "r1", SessionID: "bg-remote", Kind: "message",
		Body: "steer", ReceivedAt: time.Now(), AckReceived: ack,
	}
	next, cmd := scr.handleRemoteInput(ev)
	_ = next
	runAllCmds(t, cmd)
	if len(bg.sends) != 0 {
		t.Fatalf("remote send went through while a run owned the session (%d sends)", len(bg.sends))
	}
	if ackN != 1 {
		t.Fatalf("ack fired %d times, want 1 (custody taken)", ackN)
	}
}

func TestLiveStaleReloadsHistoryAndResubscribes(t *testing.T) {
	primary := &fakeMountConv{id: "primary"}
	scr := newScreen(t, primary, nil, nil)
	bg := newFakeLiveConv("bg-stale")
	bg.background = true
	bg.history = []ports.Message{{Role: "assistant", Text: "HISTORY-MARKER"}}

	liveCmd := scr.switchConversation(bg)
	if liveCmd == nil {
		t.Fatal("expected a live continuation from the switch")
	}
	if bg.subscribeN != 1 {
		t.Fatalf("SubscribeLive called %d times before stale, want 1", bg.subscribeN)
	}

	staleSub := bg.sub
	next, cmd := scr.Update(liveStaleMsg{sessionID: "bg-stale", sub: staleSub})
	sc := next.(Screen)
	select {
	case <-staleSub.Done():
	default:
		t.Fatal("the stale subscription was not closed")
	}
	if bg.subscribeN != 2 {
		t.Fatalf("SubscribeLive called %d times after stale, want 2 (reload + resubscribe)", bg.subscribeN)
	}
	if cmd == nil {
		t.Fatal("resubscription must arm a new read continuation")
	}
	found := false
	for _, m := range sc.conv.History() {
		if m.Text == "HISTORY-MARKER" {
			found = true
		}
	}
	if !found {
		t.Fatal("history marker lost across the stale reload")
	}
}

// TestLiveEventUpdatesTrackedSessionOffScreen is the feature's core
// scenario: the user is on tab A while a run drives background session
// B; B's fanned-out events update B's snapshotted transcript and the
// read loop re-arms so the next event keeps flowing.
func TestLiveEventUpdatesTrackedSessionOffScreen(t *testing.T) {
	primary := &fakeMountConv{id: "primary"}
	scr := newScreen(t, primary, nil, nil)
	bg := newFakeLiveConv("bg-off")
	bg.background = true

	// Visit B once: this subscribes and stores the view on B's session
	// state. Switching back to A snapshots B with its live view.
	scr.switchConversation(bg)
	primaryAgain := &fakeMountConv{id: "primary"}
	scr.switchConversation(primaryAgain)

	st := scr.sessions["bg-off"]
	if st == nil || st.live == nil {
		t.Fatal("the tracked background session must keep its live view across switch-away")
	}

	msg := liveEventMsg{
		sessionID: "bg-off",
		ev:        uievent.Event{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "step"}},
		events:    st.liveEvents,
		sub:       st.live,
	}
	next, cmd := scr.Update(msg)
	sc := next.(Screen)
	if cmd == nil {
		t.Fatal("the live read loop must re-arm after delivering an off-screen event")
	}
	if sc.convID() != "primary" {
		t.Fatalf("the user must stay on their tab (%q)", sc.convID())
	}
}

// TestLiveTurnDrivesStatusline pins the status-row fix: a watched run's
// TurnStart must give the row the same life a foreground send does
// (Start + spinner clock; without Start, View renders nothing and a
// working run looks dead), and its TurnEnd must stop it.
func TestLiveTurnDrivesStatusline(t *testing.T) {
	primary := &fakeMountConv{id: "primary"}
	scr := newScreen(t, primary, nil, nil)
	bg := newFakeLiveConv("bg-status")
	bg.background = true
	scr.switchConversation(bg)

	startMsg := liveEventMsg{
		sessionID: "bg-status",
		ev:        uievent.Event{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "step"}},
		events:    bg.events, sub: bg.sub,
	}
	next, cmd := scr.Update(startMsg)
	sc := next.(Screen)
	if !sc.statusline.Animating() {
		t.Fatal("a watched run's TurnStart must start the status row")
	}
	if cmd == nil {
		t.Fatal("the spinner clock must be armed with the turn")
	}
	if v := sc.statusline.View(fixedNow()); !strings.Contains(v, "AUTO") {
		t.Fatalf("status row %q, want the AUTO badge", v)
	}

	endMsg := liveEventMsg{
		sessionID: "bg-status",
		ev:        uievent.Event{Kind: uievent.KindTurnEnd, Body: uievent.TurnEndBody{Reason: "completed"}},
		events:    bg.events, sub: bg.sub,
	}
	next2, _ := sc.Update(endMsg)
	sc2 := next2.(Screen)
	if sc2.statusline.Animating() {
		t.Fatal("a watched run's TurnEnd must stop the status row")
	}
}

func TestLiveDoneIsASilentStop(t *testing.T) {
	primary := &fakeMountConv{id: "primary"}
	scr := newScreen(t, primary, nil, nil)
	bg := newFakeLiveConv("bg-done")
	bg.background = true
	scr.switchConversation(bg)

	next, cmd := scr.Update(liveDoneMsg{sessionID: "bg-done", sub: bg.sub})
	if cmd != nil {
		t.Fatal("Done is a silent stop: no continuation may be armed")
	}
	sc := next.(Screen)
	if sc.liveSub != nil {
		t.Fatal("the finished subscription must be cleared from the screen")
	}
}

// TestLiveMsgsAreHandledByUpdate asserts the three live msgs route
// through updateAsyncPortMsg (handled=true) rather than falling through
// update's switch to "unknown message".
func TestLiveMsgsAreHandledByUpdate(t *testing.T) {
	primary := &fakeMountConv{id: "primary"}
	scr := newScreen(t, primary, nil, nil)
	bg := newFakeLiveConv("bg-msgs")
	bg.background = true
	scr.switchConversation(bg)

	for _, msg := range []tea.Msg{
		liveEventMsg{sessionID: "bg-msgs", ev: uievent.Event{Kind: uievent.KindNotice}, events: bg.events, sub: bg.sub},
		liveDoneMsg{sessionID: "bg-done-x", sub: bg.sub},
		liveStaleMsg{sessionID: "bg-stale-x", sub: bg.sub},
	} {
		if _, _, handled := scr.updateAsyncPortMsg(msg); !handled {
			t.Fatalf("%T not handled by updateAsyncPortMsg", msg)
		}
	}
}

// TestAttachingToARunningTurnArmsTheStatusline covers the normal order:
// the operator triggers a run and THEN opens its session. TurnStart is
// already in the past and no further one arrives for that turn, so arming
// only from TurnStart left the row blank for the whole run - and
// statusline.View draws nothing until Start has been called, so a working
// automation looked dead.
func TestAttachingToARunningTurnArmsTheStatusline(t *testing.T) {
	primary := &fakeMountConv{id: "primary"}
	scr := newScreen(t, primary, nil, nil)
	bg := newFakeLiveConv("bg-inflight")
	bg.background = true
	// A run is already executing on this session when the view attaches.
	scr.SetRunActivitySource(func(id string) bool { return id == "bg-inflight" })

	scr.switchConversation(bg)

	if !scr.statusline.Animating() {
		t.Fatal("attaching to an in-flight run left the status row dead")
	}
	if v := scr.statusline.View(fixedNow()); !strings.Contains(v, "AUTO") {
		t.Fatalf("status row %q, want the AUTO badge", v)
	}

	// The run's own TurnEnd still stops it, exactly as for a turn whose
	// start the view did see.
	endMsg := liveEventMsg{
		sessionID: "bg-inflight",
		ev:        uievent.Event{Kind: uievent.KindTurnEnd, Body: uievent.TurnEndBody{Reason: "completed"}},
		events:    bg.events, sub: bg.sub,
	}
	next, _ := scr.Update(endMsg)
	if next.(Screen).statusline.Animating() {
		t.Fatal("the watched run's TurnEnd must still stop the status row")
	}
}

// TestAttachingToAnIdleSessionLeavesTheStatuslineAlone pins the other
// half: a background session with no run executing must not get a status
// row claiming activity that is not happening.
func TestAttachingToAnIdleSessionLeavesTheStatuslineAlone(t *testing.T) {
	primary := &fakeMountConv{id: "primary"}
	scr := newScreen(t, primary, nil, nil)
	bg := newFakeLiveConv("bg-idle")
	bg.background = true
	scr.SetRunActivitySource(func(string) bool { return false })

	scr.switchConversation(bg)

	if scr.statusline.Animating() {
		t.Fatal("attaching to an idle session started a status row for a turn that is not running")
	}
}

func TestFailedSendRearmsLiveView(t *testing.T) {
	primary := &fakeMountConv{id: "primary"}
	scr := newScreen(t, primary, nil, nil)
	bg := newFakeLiveConv("bg-failed-send")
	bg.background = true
	bg.sendErr = errors.New("simulated send failure")

	// Switch to background conversation arms the live view.
	switchCmd := scr.switchConversation(bg)
	if switchCmd == nil {
		t.Fatal("precondition: switchConversation should return non-nil cmd")
	}
	if scr.liveSub == nil {
		t.Fatal("precondition: liveSub should be non-nil after switch")
	}

	// Attempt sendTextWithPersisted, which will fail Send.
	next, cmd := scr.sendTextWithPersisted("hello", "")
	sc, ok := next.(Screen)
	if !ok {
		t.Fatalf("sendTextWithPersisted returned %T, want Screen", next)
	}

	if sc.liveSub == nil {
		t.Fatal("liveSub must be non-nil after failed send (re-armed live view)")
	}
	if cmd == nil {
		t.Fatal("cmd must be non-nil after failed send (batched transcript Cmd + adoptCmd)")
	}

	// Subsequently fanned-out live event still lands in the transcript.
	liveEv := uievent.Event{
		Kind: uievent.KindTurnStart,
		Body: uievent.TurnStartBody{Input: "background turn started"},
	}
	scNext, _ := sc.Update(liveEventMsg{
		sessionID: sc.convID(),
		ev:        liveEv,
		events:    sc.liveEvents,
		sub:       sc.liveSub,
	})
	scFinal := scNext.(Screen)
	found := false
	for _, b := range scFinal.transcript.Blocks() {
		if strings.Contains(b.Header.Label, "background turn started") || strings.Contains(scFinal.transcript.Dump(), "background turn started") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("fanned-out live event did not land in transcript; dump:\n%s", scFinal.transcript.Dump())
	}
}
