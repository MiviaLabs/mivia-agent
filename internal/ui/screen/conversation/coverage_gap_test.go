package conversation

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func TestChildCallSettledUnsettledSkipped(t *testing.T) {
	unsettled := &scriptedThread{history: []ports.Message{
		{Role: "assistant", ToolCalls: []ports.ToolCall{
			{ID: "c1", Name: "bash", OK: false, Output: "", Diff: nil},
		}},
	}}
	threads := stubThreads{"t1": unsettled}
	calls := collectChildCalls(threads, []string{"t1"}, "c1")
	if len(calls) != 0 {
		t.Fatalf("expected 0 child calls for unsettled call, got %d", len(calls))
	}
}

func TestSendTextWithPersisted_RunOwnsSession(t *testing.T) {
	conv := newFakeLiveConv("session-1")
	conv.background = true
	s := newScreen(t, conv, nil, nil)
	s.SetRunActivitySource(func(sessionID string) bool {
		return sessionID == "session-1"
	})
	next, cmd := s.sendText("hello")
	if cmd != nil {
		t.Fatal("expected nil cmd when run owns session")
	}
	sc := next.(Screen)
	if len(sc.conv.(*fakeLiveConv).sends) != 0 {
		t.Fatal("send should not have reached conversation")
	}
}

func TestApprovalToggleSplitKey(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.approval.SetRequest(uievent.ToolPendingBody{
		ToolCallID: "call-1",
		Name:       "patch",
		Diff:       &uievent.Diff{Path: "test.go"},
	})
	// Press toggle split key in approval context ("t")
	next, _, handled := s.handleApprovalKey(tea.KeyPressMsg{Code: 't', Text: "t"})
	if !handled {
		t.Fatal("expected approval key 't' to be handled")
	}
	_ = next
}

func TestTranscriptToggleDiffSplitAction(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	before := s.transcript
	next, _ := s.transcriptAction(keymap.IDToggleDiffSplit)
	if next == nil {
		t.Fatal("transcriptAction(IDToggleDiffSplit) returned a nil screen")
	}
	// ToggleFocusedDiffSplit is a no-op with nothing focused, so the action
	// must leave the transcript addressable and unchanged rather than
	// panicking or dropping it.
	if len(s.transcript.Blocks()) != len(before.Blocks()) {
		t.Fatalf("transcript blocks = %d, want %d (toggle with no focus must not add or drop blocks)", len(s.transcript.Blocks()), len(before.Blocks()))
	}
}

func TestLiveCapableNilConv(t *testing.T) {
	if _, ok := liveCapable(nil); ok {
		t.Fatal("expected false for nil conv")
	}
}

func TestRunOwnsSessionNotBg(t *testing.T) {
	conv := newFakeLiveConv("session-1")
	conv.background = false
	s := newScreen(t, conv, nil, nil)
	s.SetRunActivitySource(func(string) bool { return true })
	if s.runOwnsSession(conv) {
		t.Fatal("expected false when not background")
	}
}

func TestAwaitLiveEventSelectArms(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	sub := newFakeLiveSub(nil)

	// 1. Channel closed
	evCh := make(chan uievent.Event)
	close(evCh)
	cmd := s.awaitLiveEvent("s1", evCh, sub)
	msg := cmd()
	if done, ok := msg.(liveDoneMsg); !ok || done.sessionID != "s1" {
		t.Fatalf("expected liveDoneMsg for closed channel, got %#v", msg)
	}

	// 2. Event received
	evChEvent := make(chan uievent.Event, 1)
	evChEvent <- uievent.Event{Kind: uievent.KindNotice}
	cmdEvent := s.awaitLiveEvent("s1", evChEvent, sub)
	msgEvent := cmdEvent()
	if evMsg, ok := msgEvent.(liveEventMsg); !ok || evMsg.sessionID != "s1" {
		t.Fatalf("expected liveEventMsg, got %#v", msgEvent)
	}

	// 3. sub.Done()
	evCh2 := make(chan uievent.Event)
	subDone := newFakeLiveSub(nil)
	close(subDone.done)
	cmdDone := s.awaitLiveEvent("s1", evCh2, subDone)
	msgDone := cmdDone()
	if done, ok := msgDone.(liveDoneMsg); !ok || done.sessionID != "s1" {
		t.Fatalf("expected liveDoneMsg for done sub, got %#v", msgDone)
	}

	// 4. sub.Stale()
	evCh3 := make(chan uievent.Event)
	subStale := newFakeLiveSub(nil)
	close(subStale.stale)
	cmdStale := s.awaitLiveEvent("s1", evCh3, subStale)
	msgStale := cmdStale()
	if stale, ok := msgStale.(liveStaleMsg); !ok || stale.sessionID != "s1" {
		t.Fatalf("expected liveStaleMsg for stale sub, got %#v", msgStale)
	}
}

func TestHandleLiveEventUntrackedSession(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	sub := newFakeLiveSub(nil)
	evCh := make(chan uievent.Event)
	next, cmd := s.handleLiveEvent(liveEventMsg{
		sessionID: "untracked-session",
		ev:        uievent.Event{Kind: uievent.KindNotice},
		events:    evCh,
		sub:       sub,
	})
	if cmd == nil {
		t.Fatal("expected cmd to rearm")
	}
	_ = next
}

func TestHandleLiveDoneAndStaleTrackedSession(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	sub := newFakeLiveSub(nil)
	st := s.snapshotSessionState()
	st.live = sub
	s.sessions["tracked-session"] = st

	// handleLiveDone on tracked session
	next, _ := s.handleLiveDone(liveDoneMsg{sessionID: "tracked-session", sub: sub})
	sc := next.(Screen)
	if sc.sessions["tracked-session"].live != nil {
		t.Fatal("expected tracked session live to be cleared")
	}

	// handleLiveStale on tracked session
	st.live = sub
	next2, _ := s.handleLiveStale(liveStaleMsg{sessionID: "tracked-session", sub: sub})
	sc2 := next2.(Screen)
	if sc2.sessions["tracked-session"].live != nil {
		t.Fatal("expected tracked session live to be cleared on stale")
	}
}

func TestHandleLiveStaleNotCapable(t *testing.T) {
	conv := newFakeLiveConv("session-1")
	conv.background = false // not liveCapable
	s := newScreen(t, conv, nil, nil)
	sub := newFakeLiveSub(nil)
	s.liveSub = sub
	next, cmd := s.handleLiveStale(liveStaleMsg{sessionID: "session-1", sub: sub})
	if cmd != nil {
		t.Fatal("expected nil cmd when conv is not liveCapable")
	}
	_ = next
}

func TestMountedDirectSendRunOwnsSession(t *testing.T) {
	conv := newFakeLiveConv("bg-1")
	conv.background = true
	s := newScreen(t, conv, nil, nil)
	s.SetRunActivitySource(func(string) bool { return true })

	st := s.snapshotSessionState()
	st.conv = conv
	acked := false
	evt := ports.RemoteInputEvent{
		AckReceived: func() { acked = true },
	}
	_, cmd := s.mountedDirectSend("bg-1", st, "msg", "", evt, nil)
	if cmd == nil {
		t.Fatal("expected batch command")
	}
	// Run cmd to ensure ack fires
	cmd()
	if !acked {
		t.Fatal("expected ack to be fired")
	}
}

func TestSessionReplayOrResumeLiveAlreadyArmed(t *testing.T) {
	conv := newFakeLiveConv("bg-1")
	conv.background = true
	s := newScreen(t, conv, nil, nil)
	sub := newFakeLiveSub(nil)
	ch := make(chan uievent.Event)
	st := s.snapshotSessionState()
	st.live = sub
	st.liveEvents = ch

	cmd := s.replayOrResumeLive("bg-1", st, conv)
	if cmd == nil {
		t.Fatal("expected awaitLiveEvent cmd")
	}
}

func TestDrainTrackedSessionSendError(t *testing.T) {
	conv := newFakeLiveConv("bg-1")
	conv.background = true
	conv.sendErr = errors.New("send failed")
	s := newScreen(t, conv, nil, nil)

	st := s.snapshotSessionState()
	st.conv = conv
	st.queue = []string{"queued text"}

	next, cmd := s.drainTrackedSession("bg-1", st)
	if cmd != nil {
		t.Fatal("expected nil cmd on send failure")
	}
	if len(st.queue) != 1 || st.queue[0] != "queued text" {
		t.Fatalf("expected queue restored, got %v", st.queue)
	}
	_ = next
}

func TestStatusHintsBranches(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	// joinHintParts empty
	if got := s.joinHintParts(nil); got != "" {
		t.Fatalf("expected empty string for empty parts, got %q", got)
	}

	// driveStatuslineFromLive TurnStartBody
	cmd := s.driveStatuslineFromLive(uievent.Event{
		Kind: uievent.KindTurnStart,
		Body: uievent.TurnStartBody{},
	})
	if cmd == nil {
		t.Fatal("expected armTick cmd from TurnStartBody")
	}

	// idleHints avail too small for lead
	s.queue = []string{"queued msg"} // escHint returns ok=true
	// avail = 1 will make StringWidth(lead) > avail
	if got := s.idleHints(1, nil); got != "" {
		t.Fatalf("expected empty string when lead does not fit avail, got %q", got)
	}

	// idleHints candidateList empty but lead fits
	gotLead := s.idleHints(100, nil)
	if gotLead == "" {
		t.Fatal("expected lead to return when candidateList is empty")
	}

	// idleHints candidateList where base == ""
	gotLeadOnly := s.idleHints(100, [][]keymap.ID{{}})
	if gotLeadOnly == "" {
		t.Fatal("expected lead when base is empty")
	}

	// panelFocusedHints avail too small
	if got := s.panelFocusedHints(1); got != "" {
		t.Fatalf("expected empty string when tab exceeds avail, got %q", got)
	}
}

func TestTabsSwitchToSessionWithLiveCmd(t *testing.T) {
	conv1 := newFakeLiveConv("bg-1")
	conv1.background = true
	conv2 := newFakeLiveConv("bg-2")
	conv2.background = true

	s := newScreen(t, conv1, nil, nil)
	s.sessions = make(map[string]*sessionState)
	s.sessionOrder = []string{"bg-1", "bg-2"}

	st2 := s.snapshotSessionState()
	st2.conv = conv2
	s.sessions["bg-2"] = st2

	next, cmd := s.switchToSessionID("bg-2")
	if cmd == nil {
		t.Fatal("expected cmd from switchToSessionID")
	}
	_ = next
}

func TestCommandsApplyOutcomeLiveCmd(t *testing.T) {
	conv := newFakeLiveConv("bg-1")
	conv.background = true
	s := newScreen(t, conv, nil, nil)
	newConv := newFakeLiveConv("bg-2")
	newConv.background = true

	next, cmd := s.applyCommandOutcome(ports.CommandOutcome{
		ClearTranscript: true,
		Conversation:    newConv,
	})
	if cmd == nil {
		t.Fatal("expected cmd to include liveCmd")
	}
	_ = next
}

func TestArmSessionLiveNewBgConv(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	conv := newFakeLiveConv("bg-2")
	conv.background = true
	st := s.snapshotSessionState()
	st.conv = conv

	cmd := s.armSessionLive("bg-2", st)
	if cmd == nil {
		t.Fatal("expected cmd from armSessionLive")
	}
	if st.live == nil {
		t.Fatal("expected st.live to be set")
	}
}

func TestHandleTurnEndedMsgBgSessionLiveArm(t *testing.T) {
	conv1 := newFakeLiveConv("bg-1")
	conv1.background = true
	conv2 := newFakeLiveConv("bg-2")
	conv2.background = true

	s := newScreen(t, conv1, nil, nil)
	s.sessions = make(map[string]*sessionState)
	st2 := s.snapshotSessionState()
	st2.conv = conv2
	st2.active = fakeHandle{id: "turn-1"}
	st2.live = nil
	s.sessions["bg-2"] = st2

	next, cmd := s.handleTurnEndedMsg(turnEndedMsg{sessionID: "bg-2"})
	if cmd == nil {
		t.Fatal("expected liveCmd batch on bg session turn end")
	}
	_ = next
}

func TestHandleTurnEndedMsgForegroundSessionAdoptLive(t *testing.T) {
	conv := newFakeLiveConv("bg-1")
	conv.background = true
	s := newScreen(t, conv, nil, nil)
	s.active = fakeHandle{id: "turn-1"}
	s.liveSub = nil

	next, cmd := s.handleTurnEndedMsg(turnEndedMsg{sessionID: "bg-1"})
	if cmd == nil {
		t.Fatal("expected adoptLive cmd on foreground turn end")
	}
	_ = next
}
