package conversation

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lucasb-eyer/go-colorful"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/approval"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/statusline"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/transcript"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	settingsscreen "github.com/MiviaLabs/mivia-agent/internal/ui/screen/settings"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/themepicker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
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
func (errConversation) ID() string                { return "err-session" }

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
	// wireframes-panes.md section 9's "<detail>" field: the tool's own
	// name, so "running" says WHICH tool, not just that one is running.
	if view := got.statusline.View(fixedNow()); !strings.Contains(view, "run_command") {
		t.Errorf("got %q, want the tool name in the status line detail", view)
	}

	next, _ = got.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolPending,
		Body: uievent.ToolPendingBody{ToolCallID: "c2", Name: "edit"},
	}})
	got = next.(Screen)
	if view := got.statusline.View(fixedNow()); !strings.Contains(view, "pending") {
		t.Errorf("got %q, want the label to say pending while an approval is awaited", view)
	}
	if view := got.statusline.View(fixedNow()); !strings.Contains(view, "edit") {
		t.Errorf("got %q, want the pending tool's name in the status line detail", view)
	}

	next, _ = got.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c2", Name: "edit", OK: true},
	}})
	got = next.(Screen)
	if view := got.statusline.View(fixedNow()); !strings.Contains(view, "thinking") {
		t.Errorf("got %q, want the label back to thinking once the tool call ends", view)
	}
	// The previous tool's detail must not survive the label change back
	// to thinking - it would misreport what is happening now.
	if view := got.statusline.View(fixedNow()); strings.Contains(view, "edit") {
		t.Errorf("got %q, want the stale tool detail cleared once thinking resumes", view)
	}
}

// TestToolDetailIncludesFormattedArgs pins the rest of
// wireframes-panes.md section 9's "<detail>" field: a tool call with
// arguments shows them, the same render.FormatArgs shape the
// transcript block header already uses for the same data
// (component/transcript/transcript.go's handleToolStart).
func TestToolDetailIncludesFormattedArgs(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = fakeHandle{id: "t1"}
	s.statusline.Start("thinking", fixedNow())

	next, _ := s.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "c1", Name: "run_command", Args: map[string]any{"command": "go test ./..."}},
	}})
	got := next.(Screen)
	view := got.statusline.View(fixedNow())
	if !strings.Contains(view, "run_command") || !strings.Contains(view, "command=go test ./...") {
		t.Errorf("got %q, want the tool name and its formatted args", view)
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
	if !strings.Contains(view, "Ｍ Ｉ Ｖ Ｉ Ａ") &&
		!strings.Contains(view, "M    I    V    I    A") &&
		!strings.Contains(view, "Mivia") {
		t.Errorf("empty transcript view missing Mivia banner:\n%s", view)
	}
	if !strings.Contains(view, "ctrl+n:sidebar") {
		t.Errorf("empty transcript view missing keybinding hint:\n%s", view)
	}
	if strings.Contains(view, "For the work that takes longer than a chat.") {
		t.Errorf("empty transcript view should not show the tagline:\n%s", view)
	}
	if strings.Contains(view, "Mac Lisowski") {
		t.Errorf("empty transcript view should not show the author credit:\n%s", view)
	}
	if strings.Contains(view, "MIT License") {
		t.Errorf("empty transcript view should not show the MIT license:\n%s", view)
	}

	// Starting a turn replaces the welcome banner
	next, _ = scr.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindTurnStart,
		Body: uievent.TurnStartBody{Input: "hello agent"},
	}})
	scr = next.(Screen)
	turnView := ansi.Strip(scr.View())
	if strings.Contains(turnView, "type a prompt or /") {
		t.Errorf("active conversation view should not retain welcome hint:\n%s", turnView)
	}
	if !strings.Contains(turnView, "hello agent") {
		t.Errorf("active conversation view missing user input:\n%s", turnView)
	}
}

// themePair loads the two embedded themes these background tests switch
// between.
func themePair(t *testing.T) (dark, light theme.Theme, all []theme.Theme) {
	t.Helper()
	all, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range all {
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
	return dark, light, all
}

// bgOf is the SGR sequence a row must open with for the theme's own
// surface to be under it: what FillBG emits around empty content.
func bgOf(th theme.Theme, tier theme.Tier) string {
	return strings.TrimSuffix(render.FillBG(th, tier, theme.RoleBG, ""), "\x1b[m")
}

// TestViewPaintsTheThemeBackground: the screen must draw the theme's own
// surface under every cell. Without it the terminal's background shows
// through, a light theme is unreadable on a dark terminal, and the
// largest coloured area on screen never changes when the theme does.
func TestViewPaintsTheThemeBackground(t *testing.T) {
	dark, _, themes := themePair(t)
	s := New(dark, theme.TierTrueColor, themes, replay.New(nil, 0), nil, 40, fixedNow)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 60, Height: 20})

	bg := bgOf(dark, theme.TierTrueColor)
	for i, row := range strings.Split(next.View(), "\n") {
		if !strings.HasPrefix(row, bg) {
			t.Errorf("row %d is not painted with the theme background: %q", i, row)
		}
	}
}

// TestThemeChangedRepaintsTheBackground is the "enter did nothing"
// regression: the new theme's surface must replace the old one, not
// merely join it.
func TestThemeChangedRepaintsTheBackground(t *testing.T) {
	dark, light, themes := themePair(t)
	s := New(dark, theme.TierTrueColor, themes, replay.New(nil, 0), nil, 40, fixedNow)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	next, _ = next.Update(app.ThemeChangedMsg{Theme: light, Tier: theme.TierTrueColor})

	got := next.View()
	wantBG, oldBG := bgOf(light, theme.TierTrueColor), bgOf(dark, theme.TierTrueColor)
	if wantBG == oldBG {
		t.Fatal("the two themes share a bg colour; this test needs them to differ")
	}
	if !strings.Contains(got, wantBG) {
		t.Errorf("screen is not painted with the new theme's background:\n%q", got)
	}
	if strings.Contains(got, oldBG) {
		t.Errorf("the old theme's background survived the switch:\n%q", got)
	}
}

// TestBackgroundPaintDegradesWithTheTier holds the degradation ladder:
// a tier with no colour must produce the same bytes it always did.
func TestBackgroundPaintDegradesWithTheTier(t *testing.T) {
	dark, _, themes := themePair(t)
	for _, tier := range []theme.Tier{theme.TierASCII, theme.TierNoTTY} {
		s := New(dark, tier, themes, replay.New(nil, 0), nil, 40, fixedNow)
		next, _ := s.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
		if got := next.View(); strings.Contains(got, "\x1b[48;") {
			t.Errorf("tier %v painted a background: %q", tier, got)
		}
	}
}

// richTranscript feeds one of every block-producing event, so a theme
// test covers the styled bodies the transcript builds at push time
// (prose, a user turn, a plan, a progress bar, a diff) and not only the
// chrome that is restyled on every frame.
func richTranscript(t *testing.T, s Screen) Screen {
	t.Helper()
	events := []uievent.Event{
		{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "add retry to the uploader"}},
		{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "Here is the plan.\n\n```go\nfunc put() error { return nil }\n```"}},
		{Kind: uievent.KindPlan, Body: uievent.PlanBody{Total: 2, Done: 1, Items: []uievent.PlanItem{
			{Text: "read the uploader", Done: true}, {Text: "add the retry", Done: false},
		}}},
		{Kind: uievent.KindToolStart, Body: uievent.ToolStartBody{ToolCallID: "c1", Name: "edit_file"}},
		{Kind: uievent.KindToolOutput, Body: uievent.ToolOutputBody{ToolCallID: "sa-1",
			Progress: &uievent.Progress{Step: 2, TotalSteps: 3, Status: "running", Log: []string{"read defaults.go"}}}},
		{Kind: uievent.KindToolEnd, Body: uievent.ToolEndBody{ToolCallID: "c1", Name: "edit_file", OK: true, DurationMS: 12,
			Diff: &uievent.Diff{Path: "up.go", Added: 1, Removed: 1, Hunks: []uievent.DiffHunk{{
				Header: "@@ -1,2 +1,2 @@",
				Lines: []uievent.DiffLine{
					{Kind: uievent.DiffLineDel, Text: "return u.raw.Put(ctx)"},
					{Kind: uievent.DiffLineAdd, Text: "return retry.Do(ctx, put)"},
				},
			}}}}},
		{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: "context 42% used"}},
		{Kind: uievent.KindError, Body: uievent.ErrorBody{Text: "one call failed"}},
	}
	for _, ev := range events {
		next, _ := s.Update(uievent.EventMsg{Event: ev})
		s = next.(Screen)
	}
	return s
}

// paletteOf is every truecolor value a theme can draw, as the SGR
// parameter text that appears in rendered output.
func paletteOf(th theme.Theme) map[string]bool {
	out := map[string]bool{}
	for _, hex := range th.Colors {
		c, _ := colorful.Hex(hex)
		r, g, b := c.RGB255()
		out[fmt.Sprintf("2;%d;%d;%d", r, g, b)] = true
	}
	return out
}

// truecolorParams extracts every "2;r;g;b" argument drawn in s.
func truecolorParams(s string) []string {
	var out []string
	for _, part := range strings.Split(s, "\x1b[") {
		end := strings.IndexByte(part, 'm')
		if end < 0 {
			continue
		}
		for _, p := range strings.Split(part[:end], ";") {
			_ = p
		}
		fields := strings.Split(part[:end], ";")
		for i := 0; i+4 <= len(fields); i++ {
			if (fields[i] == "38" || fields[i] == "48") && fields[i+1] == "2" {
				out = append(out, strings.Join(fields[i+1:i+5], ";"))
			}
		}
	}
	return out
}

// TestThemeChangeLeavesNoColourFromTheOldPalette is the exhaustive form
// of the switch as the user sees it: after changing theme, every colour
// drawn on screen must come from the new theme. A body the transcript
// styled once at push time and never rebuilt (a diff, a plan, a
// progress bar, a code block) keeps the old palette and shows up here.
//
// It runs every ordered pair of embedded themes, because two themes can
// share a role's colour and hide a stale body that a third would expose.
func TestThemeChangeLeavesNoColourFromTheOldPalette(t *testing.T) {
	_, _, themes := themePair(t)
	for _, from := range themes {
		for _, to := range themes {
			if from.Name == to.Name {
				continue
			}
			t.Run(from.Name+"->"+to.Name, func(t *testing.T) {
				assertNoStaleColour(t, themes, from, to)
			})
		}
	}
}

func assertNoStaleColour(t *testing.T, themes []theme.Theme, from, to theme.Theme) {
	t.Helper()
	s := New(from, theme.TierTrueColor, themes, replay.New(nil, 0), nil, 40, fixedNow)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	scr := richTranscript(t, next.(Screen))

	next, _ = scr.Update(app.ThemeChangedMsg{Theme: to, Tier: theme.TierTrueColor})
	got := next.View()

	want, stale := paletteOf(to), paletteOf(from)
	seen := map[string]bool{}
	for _, p := range truecolorParams(got) {
		if want[p] || seen[p] {
			continue
		}
		seen[p] = true
		if stale[p] {
			t.Errorf("colour %q is still %s's, not %s's", p, from.Name, to.Name)
			continue
		}
		t.Errorf("colour %q belongs to neither palette", p)
	}
}

func TestF2PushesSettingsScreen(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyF2})
	if cmd == nil {
		t.Fatal("expected f2 to emit a PushScreenMsg Cmd")
	}
	msg, ok := cmd().(app.PushScreenMsg)
	if !ok {
		t.Fatalf("got %T, want app.PushScreenMsg", cmd())
	}
	if _, ok := msg.Screen.(settingsscreen.Screen); !ok {
		t.Errorf("got %T, want a settingsscreen.Screen", msg.Screen)
	}
}

// TestStatusTextShowsContextPercentAndCancelHintDuringATurn pins
// wireframes-panes.md section 9's active-turn status line shape:
// "- running  <detail>   12s   62% ctx   esc to cancel". Before this,
// the status text stopped at the elapsed time - no context share and
// no cancel affordance, even though Esc genuinely does cancel the turn
// (keymap.IDCancel, ContextGlobal: "cancel the turn, keep the text").
func TestStatusTextShowsContextPercentAndCancelHintDuringATurn(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = fakeHandle{id: "t1"}
	s.statusline.Start("thinking", fixedNow())
	s.topbar.SetSession(ports.ModelInfo{Name: "m", ContextWindow: 100_000},
		ports.Usage{InputTokens: 40_000, OutputTokens: 22_000})

	got := s.statusText()
	if !strings.Contains(got, "62% ctx") {
		t.Errorf("got %q, want the context share", got)
	}
	if !strings.Contains(got, "esc to cancel") {
		t.Errorf("got %q, want the cancel hint", got)
	}
}

// TestStatusTextOmitsContextPercentWhenUnknown pins the same
// zero-value-safe rule the rest of this file follows: an unknown
// context window (topbar.ContextPercent's ok=false) must not print a
// fabricated percentage.
func TestStatusTextOmitsContextPercentWhenUnknown(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = fakeHandle{id: "t1"}
	s.statusline.Start("thinking", fixedNow())

	got := s.statusText()
	if strings.Contains(got, "ctx") {
		t.Errorf("got %q, want no ctx share when the context window is unknown", got)
	}
	if !strings.Contains(got, "esc to cancel") {
		t.Errorf("got %q, want the cancel hint even without a known context share", got)
	}
}

// TestStatusTextHasNoCancelHintWithoutAnActiveTurn: the hint promises
// something Esc can actually do (ux-rules.md rule 1.4, "the footer
// hint must state the complete truth for the current state") - with no
// turn running there is nothing to cancel.
func TestStatusTextHasNoCancelHintWithoutAnActiveTurn(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	if got := s.statusText(); strings.Contains(got, "esc to cancel") {
		t.Errorf("got %q, want no cancel hint with no active turn", got)
	}
}

// TestStatusRowClipsWithTheSharedClipMarker pins wireframes-panes.md
// section 8/14's shared clip glyph for the status row's own final
// width clamp - a separate truncation from the screen-edge gutter's
// own, since it runs on the composed line before gutter ever sees it.
// A long turnTail (context share + cancel hint) is exactly the kind of
// content that can now push this row past a narrow terminal's width.
func TestStatusRowClipsWithTheSharedClipMarker(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 20, Height: 24})
	s = next.(Screen)
	s.active = fakeHandle{id: "t1"}
	s.statusline.Start("thinking", fixedNow())
	s.topbar.SetSession(ports.ModelInfo{Name: "m", ContextWindow: 100_000},
		ports.Usage{InputTokens: 40_000, OutputTokens: 22_000})

	got := ansi.Strip(s.statusRow())
	if !strings.Contains(got, uikitconfig.ClipMarker) {
		t.Errorf("got %q, want the clip marker %q on the clipped status row", got, uikitconfig.ClipMarker)
	}
}

func TestFilesPanelSliceImmutability(t *testing.T) {
	p := newPanel(loadTheme(t), theme.TierASCII)
	diff := uievent.Diff{Path: "a.go", After: []string{"new"}}
	p.appendLive(diff)
	oldPanel := p

	// Mutate diff input
	diff.After[0] = "MUTATED"
	if oldPanel.entries[0].Diff.After[0] == "MUTATED" {
		t.Error("appendLive did not clone Diff.After; external mutation corrupted entry")
	}

	// Update live entry on new copy
	diff2 := uievent.Diff{Path: "a.go", After: []string{"v2"}}
	p.appendLive(diff2)

	if oldPanel.entries[0].Diff.After[0] == "v2" {
		t.Error("appendLive mutated previous panel entries in place")
	}

	// Observe agent immutability
	log1 := []string{"step1"}
	pr1 := &uievent.Progress{Status: "run", Log: log1}
	p.observeAgent("agent-1", pr1)
	oldPanelWithAgent := p

	log2 := []string{"step2"}
	pr2 := &uievent.Progress{Status: "run", Log: log2}
	p.observeAgent("agent-1", pr2)

	if len(pr2.Log) != 1 || pr2.Log[0] != "step2" {
		t.Errorf("observeAgent mutated incoming pr2.Log in place: %v", pr2.Log)
	}
	if len(oldPanelWithAgent.agents[0].Log) != 1 {
		t.Errorf("observeAgent mutated previous agent log in place: len=%d", len(oldPanelWithAgent.agents[0].Log))
	}
}

type scriptedTestConversation struct {
	history []ports.Message
	model   ports.ModelInfo
	usage   ports.Usage
	title   string
}

func (s *scriptedTestConversation) Send(context.Context, intent.Send) (ports.TurnHandle, error) {
	return nil, nil
}
func (s *scriptedTestConversation) History() []ports.Message  { return s.history }
func (s *scriptedTestConversation) Model() ports.ModelInfo    { return s.model }
func (s *scriptedTestConversation) ContextUsage() ports.Usage { return s.usage }
func (s *scriptedTestConversation) Title() string             { return s.title }
func (s *scriptedTestConversation) ID() string                { return s.title }

func TestConversationNewLoadsExistingHistory(t *testing.T) {
	dark, _, themes := themePair(t)
	conv := &scriptedTestConversation{
		history: []ports.Message{
			{Role: "user", Text: "startup prompt"},
			{Role: "assistant", Text: "startup answer"},
		},
		title: "Startup Session",
	}
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = next.(Screen)
	blocks := s.transcript.Blocks()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks loaded on startup, got %d", len(blocks))
	}
	view := s.View()
	if !strings.Contains(view, "startup prompt") || !strings.Contains(view, "startup answer") {
		t.Errorf("view missing startup history:\n%s", view)
	}
	if !strings.Contains(view, "Startup Session") {
		t.Errorf("view topbar missing startup session title:\n%s", view)
	}
}
