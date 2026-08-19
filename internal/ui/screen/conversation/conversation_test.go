package conversation

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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
func (errConversation) Title() string             { return "error session" }

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
	if got.active != nil {
		t.Error("expected no active turn when Send fails")
	}
	// The failure is reported in the transcript, where the user is
	// already looking, not swallowed.
	if !strings.Contains(ansi.Strip(got.transcript.Dump()), context.DeadlineExceeded.Error()) {
		t.Errorf("the transcript does not carry the Send error:\n%s", got.transcript.Dump())
	}
	_ = cmd
}

func TestTurnEventUpdatesTranscriptAndReschedulesRead(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = fakeHandle{id: "t1"}

	next, cmd := s.Update(uievent.EventMsg{Event: uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "hello"}}})
	got := next.(Screen)
	if n := len(got.transcript.Blocks()); n != 1 {
		t.Errorf("got %d blocks, want the finished span added to the transcript", n)
	}
	if !strings.Contains(ansi.Strip(got.transcript.Dump()), "hello") {
		t.Errorf("the transcript does not carry the text:\n%s", got.transcript.Dump())
	}
	if cmd == nil {
		t.Fatal("expected a re-issued event read")
	}

	msgs := batchMsgs(t, cmd)
	sawCommit := true // the transcript now owns the content; nothing is emitted
	var sawRead bool
	for _, msg := range msgs {
		if _, ok := msg.(turnEndedMsg); ok {
			sawRead = true // fakeHandle's closed channel: the re-issued read observes it immediately
		}
	}
	if !sawCommit {
		t.Errorf("expected a CommitMsg among %v", msgs)
	}
	if !sawRead {
		t.Errorf("expected the re-issued read Cmd among %v", msgs)
	}
}

func TestTurnEventArmsApprovalOnToolPending(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = fakeHandle{id: "t1"}
	next, _ := s.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolPending,
		Body: uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"},
	}})
	got := next.(Screen)
	if !got.approval.Active() {
		t.Error("expected a tool.pending event to arm the approval prompt")
	}
}

// TestStatuslineLabelTracksTurnActivity pins wireframes-panes.md's state
// word vocabulary (section on the activity indicator) and ux-rules.md
// rule 9.10: "show the active tool or step, not only a spinner." Before
// this, Start("thinking", ...) was the only place that ever set the
// label, so the line kept saying "thinking" through a running tool call
// and while blocked on an approval - a still-correct spinner next to a
// stale, misleading word.
func TestStatuslineLabelTracksTurnActivity(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = fakeHandle{id: "t1"}
	s.statusline.Start("thinking", fixedNow())

	next, _ := s.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "c1", Name: "run_command"},
	}})
	got := next.(Screen)
	if view := got.statusline.View(fixedNow()); !strings.Contains(view, "running") {
		t.Errorf("got %q, want the label to say running once a tool starts", view)
	}

	next, _ = got.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolPending,
		Body: uievent.ToolPendingBody{ToolCallID: "c2", Name: "edit"},
	}})
	got = next.(Screen)
	if view := got.statusline.View(fixedNow()); !strings.Contains(view, "pending") {
		t.Errorf("got %q, want the label to say pending while an approval is awaited", view)
	}

	next, _ = got.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c2", Name: "edit", OK: true},
	}})
	got = next.(Screen)
	if view := got.statusline.View(fixedNow()); !strings.Contains(view, "thinking") {
		t.Errorf("got %q, want the label back to thinking once the tool call ends", view)
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

	next, cmd := s.Update(keyMsg("o"))
	got := next.(Screen)
	if cmd == nil {
		t.Fatal("expected the approval component to emit a DecisionMsg Cmd for \"o\"")
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

func TestWindowSizeMsgResizesScreenAndComposer(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, cmd := s.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if cmd != nil {
		t.Error("expected no Cmd from a resize")
	}
	got := next.(Screen)
	if got.width != 120 || got.height != 40 {
		t.Errorf("got %dx%d, want 120x40 tracked on the screen", got.width, got.height)
	}

	// The composer must actually widen: typing past the old 40-column
	// width has to stay on one line rather than being clipped to it.
	long := strings.Repeat("x", 90)
	got.composer.SetWidth(120)
	for _, r := range long {
		nextC, _ := got.composer.Update(tea.KeyPressMsg{Text: string(r), Code: r})
		got.composer = nextC
	}
	if v := got.composer.Value(); len(v) != 90 {
		t.Errorf("got %d chars in the composer, want 90 (input truncated by a stale width?)", len(v))
	}
}

func TestThemeChangedMsgUpdatesScreenAndAllComponents(t *testing.T) {
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	var dark, light theme.Theme
	for _, th := range themes {
		switch th.Name {
		case "mivia-dark":
			dark = th
		case "mivia-light":
			light = th
		}
	}
	if dark.Name == "" || light.Name == "" {
		t.Fatal("need both mivia-dark and mivia-light embedded")
	}

	s := New(dark, theme.TierASCII, themes, replay.New(nil, 0), nil, 40, nil)
	next, cmd := s.Update(app.ThemeChangedMsg{Theme: light, Tier: theme.TierTrueColor})
	if cmd != nil {
		t.Error("expected no Cmd from a theme change")
	}
	got := next.(Screen)

	for name, th := range map[string]theme.Theme{
		"Screen":     got.Theme,
		"transcript": got.transcript.Theme,
		"composer":   got.composer.Theme,
		"statusline": got.statusline.Theme,
		"approval":   got.approval.Theme,
	} {
		if th.Name != light.Name {
			t.Errorf("%s.Theme = %q, want %q", name, th.Name, light.Name)
		}
	}
	if got.Tier != theme.TierTrueColor {
		t.Errorf("got Tier %v, want TierTrueColor", got.Tier)
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

// unknownMsg is a Msg type this screen has no case for, so it exercises
// Update's fallthrough. (Not WindowSizeMsg - that is handled now.)
type unknownMsg struct{}

func TestUpdateIgnoresUnrecognisedMsg(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, cmd := s.Update(unknownMsg{})
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
	got, ok := msg.(uievent.EventMsg)
	if !ok || got.Event.Kind != uievent.KindNotice {
		t.Fatalf("got %+v, want uievent.EventMsg carrying the queued event", msg)
	}

	close(ch)
	msg = waitForEvent(ch)()
	if _, ok := msg.(turnEndedMsg); !ok {
		t.Errorf("got %T, want turnEndedMsg once the channel is closed and drained", msg)
	}
}

func TestViewComposesTranscriptStatuslineAndComposer(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = next.(Screen)
	// A still-streaming delta, not a committed block: only the live tail
	// shows up in View() (see transcript's package doc comment) -
	// finalized content leaves via CommitMsg, tested separately.
	s.transcript, _ = s.transcript.HandleEvent(uievent.Event{Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{Text: "live text"}})
	s.statusline.Start("thinking", fixedNow())
	s.approval.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"})

	got := s.View()
	for _, want := range []string{"live text", "thinking", "run_command"} {
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
// and its neighbours exercise uievent.EventMsg directly; they don't read

// through Events() themselves). Events() returns an already-closed
// channel, not nil: handleTurnEvent's batched readCmd is safe to
// actually execute in a test this way (immediate turnEndedMsg, no
// block), instead of hanging on a nil-channel read.
type fakeHandle struct{ id string }

func (h fakeHandle) ID() string { return h.id }
func (h fakeHandle) Events() <-chan uievent.Event {
	ch := make(chan uievent.Event)
	close(ch)
	return ch
}
func (h fakeHandle) Cancel() {}

// batchMsgs runs cmd, expands it if it's a tea.BatchMsg, and executes
// every sub-Cmd, returning the resulting Msgs. Safe here because every
// Cmd handleTurnEvent/send can batch is either a pure literal-return
// closure (commit, flush) or fakeHandle's own immediate-close read -
// none of them block.
func batchMsgs(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, c := range batch {
		if c == nil {
			continue
		}
		out = append(out, c())
	}
	return out
}

// TestStatusRowIsPermanent pins the cockpit's layout anchor. The status
// row is reserved whether or not it has anything to say, so it never
// appears or disappears and never reflows the transcript above it
// (docs/design/ux-rules.md rule 2.7).
func TestStatusRowIsPermanent(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	bare := s.reservedRows()

	s.statusline.Start("thinking", fixedNow())
	if got := s.reservedRows(); got != bare {
		t.Errorf("got %d reserved rows with a turn running, want %d: the row is always reserved", got, bare)
	}

	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	scr := next.(Screen)
	quiet := len(strings.Split(scr.View(), "\n"))
	scr.statusline.Start("thinking", fixedNow())
	busy := len(strings.Split(scr.View(), "\n"))
	if quiet != busy {
		t.Errorf("the view is %d rows idle and %d rows busy; the status row must not move the layout", quiet, busy)
	}
}

// TestTopBarHasAOneRowMargin pins the blank row between the top bar and
// whatever renders under it (transcript, dialog, or overlay), so content
// never touches the bar's edge. reservedRows must budget for it, and
// View's second line must be the blank margin itself.
func TestTopBarHasAOneRowMargin(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	want := s.topbar.Height() + 1 + s.composer.Height() + 1
	if got := s.reservedRows(); got != want {
		t.Errorf("reservedRows() = %d, want %d (topbar + 1 margin row + composer + status)", got, want)
	}

	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	scr := next.(Screen)
	lines := strings.Split(scr.View(), "\n")
	if len(lines) < scr.topbar.Height()+1 {
		t.Fatalf("got %d view lines, want at least a top bar and a margin row", len(lines))
	}
	// The final layout pass pads every line to the terminal width, so the
	// margin row is width spaces, not a literal empty string - assert on
	// content, not exact bytes.
	marginLine := lines[scr.topbar.Height()]
	if strings.TrimSpace(marginLine) != "" {
		t.Errorf("line %d (right under the top bar) = %q, want a blank margin row", scr.topbar.Height(), marginLine)
	}
}

// TestReservedRowsGrowsWithTheApprovalPrompt pins the chrome that DOES
// claim extra rows.
func TestReservedRowsGrowsWithTheApprovalPrompt(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	bare := s.reservedRows()

	s.approval.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"})
	if got := s.reservedRows(); got <= bare {
		t.Errorf("got %d, want more than %d once the approval prompt is armed", got, bare)
	}
}

// TestViewNeverExceedsTerminalHeight is the invariant the arithmetic in
// reservedRows only serves. Asserting that reservedRows returns a bigger
// number proves nothing on its own: the number has to reach the
// transcript. It used to be delivered only inside the WindowSizeMsg
// case, so arming the status line or an approval prompt claimed rows the
// transcript was still budgeting for, and View drew more rows than the
// terminal had.
func TestViewNeverExceedsTerminalHeight(t *testing.T) {
	const width, height = 80, 11

	// A long pace keeps the turn open, so the status line stays armed.
	s := newScreen(t, replay.New([]uievent.Event{
		{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "hi"}},
	}, time.Hour), nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: width, Height: height})
	scr := next.(Screen)

	assertFits := func(t *testing.T, s Screen, stage string) {
		t.Helper()
		rows := 0
		for _, line := range strings.Split(s.View(), "\n") {
			rows += max(1, (ansi.StringWidth(line)+width-1)/width)
		}
		if rows > height {
			t.Fatalf("%s: view is %d rows in a %d-row terminal:\n%s", stage, rows, height, s.View())
		}
	}

	// Fill the transcript first, so any budget slack is used up.
	for i := 0; i < 40; i++ {
		n, _ := scr.Update(uievent.EventMsg{Event: uievent.Event{
			Kind: uievent.KindNotice,
			Body: uievent.NoticeBody{Text: fmt.Sprintf("notice %d", i)},
		}})
		scr = n.(Screen)
	}
	assertFits(t, scr, "transcript full")

	// Now claim rows with chrome, through the real paths: sending arms
	// the status line, and a pending tool call arms the approval prompt.
	scr = typeText(t, scr, "hi")
	n, _ := scr.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	scr = n.(Screen)
	if !scr.statusline.Active() {
		t.Fatal("expected the status line armed after sending")
	}
	assertFits(t, scr, "status line armed")

	n, _ = scr.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolPending,
		Body: uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"},
	}})
	scr = n.(Screen)
	if !scr.approval.Active() {
		t.Fatal("expected the approval prompt armed")
	}
	assertFits(t, scr, "approval armed")
}

// TestResizeWithNothingToEvictCommitsNothing covers resize's quiet path.
// A resize that evicts nothing must produce no Cmd, or the router would
// queue an empty print and the pop-flush would emit a blank line.
func TestResizeWithNothingToEvictCommitsNothing(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, cmd := s.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	if cmd != nil {
		t.Errorf("an empty transcript resized and produced %+v, want no Cmd", cmd())
	}
	// Growing further still evicts nothing.
	if _, cmd := next.Update(tea.WindowSizeMsg{Width: 100, Height: 60}); cmd != nil {
		t.Errorf("a grow produced %+v, want no Cmd", cmd())
	}
}

// TestShrinkKeepsEveryBlock pins the cockpit's replacement for
// eviction. The inline renderer committed blocks to scrollback when the
// terminal shrank. The cockpit has no scrollback, so a shrink must keep
// every block and simply show fewer of them.
func TestShrinkKeepsEveryBlock(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	scr := next.(Screen)

	for i := 0; i < 10; i++ {
		n, _ := scr.Update(uievent.EventMsg{Event: uievent.Event{
			Kind: uievent.KindNotice,
			Body: uievent.NoticeBody{Text: fmt.Sprintf("notice %d", i)},
		}})
		scr = n.(Screen)
	}
	before := len(scr.transcript.Blocks())

	next, _ = scr.Update(tea.WindowSizeMsg{Width: 80, Height: 7})
	scr = next.(Screen)
	if got := len(scr.transcript.Blocks()); got != before {
		t.Errorf("got %d blocks after the shrink, want all %d kept", got, before)
	}
	if !strings.Contains(ansi.Strip(scr.transcript.Dump()), "notice 0") {
		t.Error("the oldest block was lost on a shrink")
	}
	// And the view fits the smaller terminal.
	if rows := len(strings.Split(scr.View(), "\n")); rows > 7 {
		t.Errorf("view is %d rows in a 7-row terminal", rows)
	}
}

func TestTurnEventClearsApprovalOnToolStartOrTurnEnd(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	// Arm approval
	next, _ := s.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolPending,
		Body: uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"},
	}})
	scr := next.(Screen)
	if !scr.approval.Active() {
		t.Fatal("expected approval active after ToolPending")
	}

	// ToolStart clears approval
	next, _ = scr.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "c1", Name: "run_command"},
	}})
	scr = next.(Screen)
	if scr.approval.Active() {
		t.Error("expected approval cleared after ToolStart")
	}

	// Arm approval again, then TurnEnd clears approval
	next, _ = scr.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolPending,
		Body: uievent.ToolPendingBody{ToolCallID: "c2", Name: "run_command"},
	}})
	scr = next.(Screen)
	if !scr.approval.Active() {
		t.Fatal("expected approval active after ToolPending")
	}
	next, _ = scr.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindTurnEnd,
		Body: uievent.TurnEndBody{Reason: "completed"},
	}})
	scr = next.(Screen)
	if scr.approval.Active() {
		t.Error("expected approval cleared after TurnEnd")
	}
}

func TestWelcomeBannerRendersOnEmptyTranscript(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	scr := next.(Screen)
	view := ansi.Strip(scr.View())
	if !strings.Contains(view, "█▀▄▀█") && !strings.Contains(view, "Mivia Agent") && !strings.Contains(view, "M I V I A   A G E N T") {
		t.Errorf("empty transcript view missing Mivia Agent banner:\n%s", view)
	}
	if !strings.Contains(view, "Mac Lisowski") {
		t.Errorf("empty transcript view missing author credit:\n%s", view)
	}
	if !strings.Contains(view, "MIT License") {
		t.Errorf("empty transcript view missing MIT license:\n%s", view)
	}

	// Starting a turn replaces the welcome banner
	next, _ = scr.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindTurnStart,
		Body: uievent.TurnStartBody{Input: "hello agent"},
	}})
	scr = next.(Screen)
	turnView := ansi.Strip(scr.View())
	if strings.Contains(turnView, "Mac Lisowski") {
		t.Errorf("active conversation view should not retain welcome credits:\n%s", turnView)
	}
	if !strings.Contains(turnView, "hello agent") {
		t.Errorf("active conversation view missing user input:\n%s", turnView)
	}
}
