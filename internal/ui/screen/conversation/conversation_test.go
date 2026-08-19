package conversation

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/approval"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/statusline"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/transcript"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/themepicker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func loadTheme(t *testing.T) theme.Theme {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if th.Name == "mivia-dark" {
			return th
		}
	}
	t.Fatal("mivia-dark theme not found")
	return theme.Theme{}
}

func keyMsg(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Text: s, Code: rune(s[0])} }

func fixedNow() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

func newScreen(t *testing.T, conv ports.Conversation, approver ports.Approver, themes []theme.Theme) Screen {
	t.Helper()
	return New(loadTheme(t), theme.TierASCII, themes, conv, approver, 40, fixedNow)
}

// errConversation is a hand-written ports.Conversation whose Send always
// fails - the one path replay.Conversation can't produce, exercised
// through the real interface type like every other call in these tests.
type errConversation struct{ err error }

func (e errConversation) Send(context.Context, intent.Send) (ports.TurnHandle, error) {
	return nil, e.err
}
func (errConversation) History() []ports.Message  { return nil }
func (errConversation) Model() ports.ModelInfo    { return ports.ModelInfo{} }
func (errConversation) ContextUsage() ports.Usage { return ports.Usage{} }

func typeText(t *testing.T, s Screen, text string) Screen {
	t.Helper()
	for _, r := range text {
		next, _ := s.Update(keyMsg(string(r)))
		s = next.(Screen)
	}
	return s
}

func TestNewSatisfiesAppScreen(t *testing.T) {
	var _ app.Screen = New(theme.Theme{}, theme.TierASCII, nil, replay.New(nil, 0), nil, 40, nil)
}

func TestEnterWithEmptyComposerIsNoOp(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Error("expected no Cmd for enter with an empty composer")
	}
	if next.(Screen).active != nil {
		t.Error("expected no active turn to have started")
	}
}

func TestEnterStartsTurnAndArmsStatusline(t *testing.T) {
	events := []uievent.Event{{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "hi"}}}
	s := newScreen(t, replay.New(events, time.Hour), nil, nil) // long pace: turn stays open
	s = typeText(t, s, "hi")

	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := next.(Screen)
	if cmd == nil {
		t.Fatal("expected a Cmd (statusline tick + event read) after sending")
	}
	if got.active == nil {
		t.Fatal("expected an active turn after sending")
	}
	if !got.statusline.Active() {
		t.Error("expected the statusline armed after sending")
	}
	if got.composer.Value() != "" {
		t.Errorf("got composer value %q, want cleared after send", got.composer.Value())
	}
}

func TestEnterWhileTurnActiveIsNoOp(t *testing.T) {
	events := []uievent.Event{{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "hi"}}}
	s := newScreen(t, replay.New(events, time.Hour), nil, nil)
	s = typeText(t, s, "hi")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	firstActive := s.active

	s = typeText(t, s, "again")
	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := next.(Screen)
	if cmd != nil {
		t.Error("expected no Cmd: a turn is already active")
	}
	if got.active != firstActive {
		t.Error("expected the active handle to be unchanged while a turn is in flight")
	}
}

func TestSendErrorAppendsErrorBlockNotActive(t *testing.T) {
	s := newScreen(t, errConversation{err: context.DeadlineExceeded}, nil, nil)
	s = typeText(t, s, "hi")
	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := next.(Screen)
	if cmd != nil {
		t.Error("expected no Cmd when Send fails")
	}
	if got.active != nil {
		t.Error("expected no active turn when Send fails")
	}
	if !strings.Contains(got.transcript.View(), context.DeadlineExceeded.Error()) {
		t.Errorf("expected the Send error surfaced in the transcript, got:\n%s", got.transcript.View())
	}
}

func TestTurnEventUpdatesTranscriptAndReschedulesRead(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = fakeHandle{id: "t1"}

	next, cmd := s.Update(turnEventMsg{ev: uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "hello"}}})
	got := next.(Screen)
	if !strings.Contains(got.transcript.View(), "hello") {
		t.Errorf("expected the event applied to the transcript, got:\n%s", got.transcript.View())
	}
	if cmd == nil {
		t.Fatal("expected a Cmd that re-issues the event read while a turn is active")
	}
}

func TestTurnEventArmsApprovalOnToolPending(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = fakeHandle{id: "t1"}
	next, _ := s.Update(turnEventMsg{ev: uievent.Event{
		Kind: uievent.KindToolPending,
		Body: uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"},
	}})
	got := next.(Screen)
	if !got.approval.Active() {
		t.Error("expected a tool.pending event to arm the approval prompt")
	}
}

func TestTurnEndedStopsStatuslineAndClearsActive(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = fakeHandle{id: "t1"}
	s.statusline.Start("thinking", fixedNow())

	next, cmd := s.Update(turnEndedMsg{})
	got := next.(Screen)
	if cmd != nil {
		t.Error("expected no Cmd on turn end")
	}
	if got.active != nil {
		t.Error("expected active cleared on turn end")
	}
	if got.statusline.Active() {
		t.Error("expected the statusline stopped on turn end")
	}
}

func TestDecisionMsgResolvesApprover(t *testing.T) {
	approver := replay.NewApprover()
	s := newScreen(t, replay.New(nil, 0), approver, nil)
	_, cmd := s.Update(approval.DecisionMsg{ToolCallID: "c1", Decision: ports.DecisionOnce})
	if cmd != nil {
		t.Error("expected no Cmd from resolving a decision")
	}
	got := approver.Resolutions()
	if len(got) != 1 || got[0].ID != "c1" || got[0].Decision != ports.DecisionOnce {
		t.Errorf("got %+v, want one resolution for c1/DecisionOnce", got)
	}
}

func TestDecisionMsgWithNilApproverIsSafe(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	if _, cmd := s.Update(approval.DecisionMsg{ToolCallID: "c1"}); cmd != nil {
		t.Error("expected no Cmd")
	}
}

func TestApprovalActiveRoutesKeysToApprovalNotComposer(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), replay.NewApprover(), nil)
	s.approval.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"})

	next, cmd := s.Update(keyMsg("y"))
	got := next.(Screen)
	if cmd == nil {
		t.Fatal("expected the approval component to emit a DecisionMsg Cmd for \"y\"")
	}
	if got.composer.Value() != "" {
		t.Errorf("got composer value %q, want the keypress consumed by approval, not typed", got.composer.Value())
	}
}

func TestCtrlTPushesThemePickerWhenThemesPresent(t *testing.T) {
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	s := newScreen(t, replay.New(nil, 0), nil, themes)
	_, cmd := s.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("expected ctrl+t to emit a PushScreenMsg Cmd")
	}
	msg, ok := cmd().(app.PushScreenMsg)
	if !ok {
		t.Fatalf("got %T, want app.PushScreenMsg", cmd())
	}
	if _, ok := msg.Screen.(themepicker.Screen); !ok {
		t.Errorf("got %T, want a themepicker.Screen", msg.Screen)
	}
}

func TestCtrlTNoOpWithoutThemes(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	_, cmd := s.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Error("expected ctrl+t to be a no-op with no theme list configured")
	}
}

func TestOrdinaryKeyTypesIntoComposer(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(keyMsg("h"))
	if got := next.(Screen).composer.Value(); got != "h" {
		t.Errorf("got composer value %q, want \"h\"", got)
	}
}

func TestStatuslineTickMsgForwardsToStatusline(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.statusline.Start("thinking", fixedNow())
	_, cmd := s.Update(statusline.TickMsg{})
	if cmd == nil {
		t.Error("expected the statusline's own reschedule Cmd forwarded")
	}
}

func TestFlushMsgForwardsToTranscript(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.transcript, _ = s.transcript.HandleEvent(uievent.Event{Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{Text: "a"}})
	_, cmd := s.Update(transcript.FlushMsg{})
	if cmd == nil {
		t.Error("expected the transcript's own reschedule Cmd forwarded while a span is streaming")
	}
}

func TestInitIsNil(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	if cmd := s.Init(); cmd != nil {
		t.Error("expected a nil Init Cmd")
	}
}

func TestUpdateIgnoresUnrecognisedMsg(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, cmd := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if cmd != nil {
		t.Error("expected no Cmd for an unrecognised Msg")
	}
	if next.(Screen).composer.Value() != "" {
		t.Error("expected an unrecognised Msg to be a pure no-op")
	}
}

func TestWaitForEventReadsOneEventThenSignalsClose(t *testing.T) {
	ch := make(chan uievent.Event, 1)
	ch <- uievent.Event{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: "n"}}
	cmd := waitForEvent(ch)

	msg := cmd()
	got, ok := msg.(turnEventMsg)
	if !ok || got.ev.Kind != uievent.KindNotice {
		t.Fatalf("got %+v, want turnEventMsg carrying the queued event", msg)
	}

	close(ch)
	msg = waitForEvent(ch)()
	if _, ok := msg.(turnEndedMsg); !ok {
		t.Errorf("got %T, want turnEndedMsg once the channel is closed and drained", msg)
	}
}

func TestViewComposesTranscriptStatuslineAndComposer(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.transcript, _ = s.transcript.HandleEvent(uievent.Event{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: "notice text"}})
	s.statusline.Start("thinking", fixedNow())
	s.approval.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"})

	got := s.View()
	for _, want := range []string{"notice text", "thinking", "run_command"} {
		if !strings.Contains(got, want) {
			t.Errorf("view missing %q:\n%s", want, got)
		}
	}
}

func TestViewOmitsInactiveStatuslineAndApproval(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	got := s.View()
	if strings.Contains(got, "approve") {
		t.Errorf("expected no approval line when nothing is pending: %q", got)
	}
}

// fakeHandle is a minimal ports.TurnHandle standing in for "some turn is
// active" in tests that only need Update's `s.active != nil` branch,
// not a real event stream (TestTurnEventUpdatesTranscriptAndReschedulesRead
// and its neighbours exercise turnEventMsg directly; they don't read
// through Events() themselves).
type fakeHandle struct{ id string }

func (h fakeHandle) ID() string                   { return h.id }
func (h fakeHandle) Events() <-chan uievent.Event { return nil }
func (h fakeHandle) Cancel()                      {}
