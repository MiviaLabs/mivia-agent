package conversation

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/composer"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func key(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: rune(s[0]), Text: s} }

func ctrl(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl} }

// sized returns a screen with a measured terminal and n notice blocks in
// the live window, so focus movement has something to move over.
func sized(t *testing.T, blocks int) Screen {
	t.Helper()
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	scr := next.(Screen)
	for i := 0; i < blocks; i++ {
		n, _ := scr.Update(turnEventMsg{ev: uievent.Event{
			Kind: uievent.KindNotice,
			Body: uievent.NoticeBody{Text: "notice-" + string(rune('a'+i))},
		}})
		scr = n.(Screen)
	}
	return scr
}

func press(t *testing.T, s Screen, k tea.KeyPressMsg) (Screen, tea.Cmd) {
	t.Helper()
	next, cmd := s.Update(k)
	return next.(Screen), cmd
}

// shiftTab moves the focus UP; tab moves it DOWN. The composer is the
// bottom row, so Shift-Tab is the way into the transcript.
var (
	tabKey      = tea.KeyPressMsg{Code: tea.KeyTab}
	shiftTabKey = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
)

// TestFocusIsVertical pins the direction model: Shift-Tab enters the
// transcript at the NEWEST block, because that is the block directly
// above the composer.
func TestFocusIsVertical(t *testing.T) {
	s := sized(t, 3)
	if s.transcript.Focused() {
		t.Fatal("the composer must hold the focus at rest")
	}

	s, _ = press(t, s, shiftTabKey)
	if got, want := s.transcript.FocusIndex(), len(s.transcript.Live())-1; got != want {
		t.Fatalf("got focus %d, want the newest block %d directly above the composer", got, want)
	}

	s, _ = press(t, s, shiftTabKey)
	if got, want := s.transcript.FocusIndex(), len(s.transcript.Live())-2; got != want {
		t.Errorf("got focus %d, want one block further up (%d)", got, want)
	}

	s, _ = press(t, s, tabKey)
	s, _ = press(t, s, tabKey)
	if s.transcript.Focused() {
		t.Errorf("got focus %d, want Tab past the newest block to reach the composer",
			s.transcript.FocusIndex())
	}
}

// TestFocusStopsAtTheOldestBlock pins the top wall. Above the oldest
// live block is scrollback, which is frozen and cannot take the focus.
func TestFocusStopsAtTheOldestBlock(t *testing.T) {
	s := sized(t, 3)
	for i := 0; i < 10; i++ {
		s, _ = press(t, s, shiftTabKey)
	}
	if got := s.transcript.FocusIndex(); got != 0 {
		t.Errorf("got focus %d, want it held at the oldest live block", got)
	}
}

// TestTabFromTheComposerDoesNotEnterTheTranscript: the composer is the
// bottom row, so there is nothing below it to move to.
func TestTabFromTheComposerDoesNotEnterTheTranscript(t *testing.T) {
	s := sized(t, 3)
	s, _ = press(t, s, tabKey)
	if s.transcript.Focused() {
		t.Errorf("Tab moved the focus to %d, want the composer kept", s.transcript.FocusIndex())
	}
}

// TestTypingIsNotSwallowedByTranscriptKeys pins the precedence that
// matters most: with the composer focused, "y" is a character, not copy.
func TestTypingIsNotSwallowedByTranscriptKeys(t *testing.T) {
	s := sized(t, 2)
	for _, k := range []string{"y", " ", "e"} {
		s, _ = press(t, s, key(k))
	}
	if got := s.composer.Value(); got != "y e" {
		t.Errorf("got composer %q, want the characters typed through", got)
	}
}

// TestSpaceTogglesOnlyAFocusedBlock is the same rule from the other
// side: once a block holds the focus, Space is a toggle.
func TestSpaceTogglesOnlyAFocusedBlock(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	scr := next.(Screen)
	n, _ := scr.Update(turnEventMsg{ev: uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c1", Name: "run_command", OK: true, Result: "x\ny\nz"},
	}})
	scr = n.(Screen)

	scr, _ = press(t, scr, shiftTabKey)
	before := scr.transcript.Live()[0].Collapsed
	scr, _ = press(t, scr, key(" "))
	if scr.transcript.Live()[0].Collapsed == before {
		t.Error("space did not toggle the focused block")
	}
	if got := scr.composer.Value(); got != "" {
		t.Errorf("space reached the composer as text: %q", got)
	}
}

func TestCollapseAllAndExpandAll(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	scr := next.(Screen)
	for i := 0; i < 3; i++ {
		n, _ := scr.Update(turnEventMsg{ev: uievent.Event{
			Kind: uievent.KindToolEnd,
			Body: uievent.ToolEndBody{ToolCallID: string(rune('a' + i)), Name: "edit", OK: true, Result: "1\n2"},
		}})
		scr = n.(Screen)
	}
	scr, _ = press(t, scr, shiftTabKey)

	scr, _ = press(t, scr, ctrl('g'))
	for i, b := range scr.transcript.Live() {
		if b.Collapsible && !b.Collapsed {
			t.Errorf("block %d stayed open after collapse-all", i)
		}
	}
	scr, _ = press(t, scr, ctrl('e'))
	for i, b := range scr.transcript.Live() {
		if b.Collapsible && b.Collapsed {
			t.Errorf("block %d stayed closed after expand-all", i)
		}
	}
}

// TestCopyBlockSetsTheClipboardAndSaysSo pins both halves. OSC 52 fails
// silently on VTE and Terminal.app, so the line must state what was
// attempted rather than claim the paste buffer holds the text.
func TestCopyBlockSetsTheClipboardAndSaysSo(t *testing.T) {
	s := sized(t, 2)
	s, _ = press(t, s, shiftTabKey)
	s, cmd := press(t, s, key("y"))
	if cmd == nil {
		t.Fatal("expected a clipboard Cmd")
	}
	if got := s.statusline.View(fixedNow()); !strings.Contains(ansi.Strip(got), "copied") {
		t.Errorf("status line says %q, want the copy stated", got)
	}
}

// TestEscUnfocusesBeforeItCancels pins the Esc ladder. With a block
// focused, Esc returns to the composer; only then does it reach the turn.
func TestEscUnfocusesBeforeItCancels(t *testing.T) {
	s := sized(t, 2)
	s, _ = press(t, s, shiftTabKey)
	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.transcript.Focused() {
		t.Error("esc did not return the focus to the composer")
	}
}

// TestCancelKeepsTheComposerText pins the reported defect: a cancel that
// discards what the user typed loses work they cannot recover.
func TestCancelKeepsTheComposerText(t *testing.T) {
	events := []uievent.Event{{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "hi"}}}
	s := newScreen(t, replay.New(events, time.Hour), nil, nil)
	s = typeText(t, s, "keep me")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	scr := next.(Screen)
	// Send clears the composer, so type again while the turn runs.
	scr = typeText(t, scr, "draft")

	scr, _ = press(t, scr, tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := scr.composer.Value(); got != "draft" {
		t.Errorf("got composer %q, want the draft kept across a cancel", got)
	}
}

// TestCtrlCCancelsThenQuits pins rule 1.3. One press must never discard
// a running turn AND the session at once.
func TestCtrlCCancelsThenQuits(t *testing.T) {
	events := []uievent.Event{{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "hi"}}}
	s := newScreen(t, replay.New(events, time.Hour), nil, nil)
	s = typeText(t, s, "hi")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	scr := next.(Screen)

	scr, cmd := press(t, scr, ctrl('c'))
	if cmd != nil {
		t.Fatalf("the first ctrl+c produced %+v, want a cancel and no quit", cmd())
	}
	if !strings.Contains(ansi.Strip(scr.statusline.View(fixedNow())), "again to quit") {
		t.Error("the first ctrl+c did not say a second press quits")
	}

	_, cmd = press(t, scr, ctrl('c'))
	if cmd == nil {
		t.Fatal("the second ctrl+c did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("got %T, want tea.QuitMsg", cmd())
	}
}

// TestAnyOtherKeyDisarmsTheQuit stops a stray press leaving the session
// one keystroke from exiting.
func TestAnyOtherKeyDisarmsTheQuit(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s = typeText(t, s, "draft")
	s, _ = press(t, s, ctrl('c'))
	if !s.quitArmed {
		t.Fatal("expected the quit armed after one ctrl+c with text in the composer")
	}
	s, _ = press(t, s, key("x"))
	if s.quitArmed {
		t.Error("a plain keystroke left the quit armed")
	}
}

// TestCtrlCOnAnIdleEmptyComposerQuitsAtOnce: with no turn and no text
// there is nothing to cancel, so a confirmation step would be noise.
func TestCtrlCOnAnIdleEmptyComposerQuitsAtOnce(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	_, cmd := press(t, s, ctrl('c'))
	if cmd == nil {
		t.Fatal("expected an immediate quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("got %T, want tea.QuitMsg", cmd())
	}
}

func TestCtrlRTogglesReasoning(t *testing.T) {
	s := sized(t, 1)
	if s.transcript.ReasoningHidden() {
		t.Fatal("reasoning starts shown")
	}
	s, _ = press(t, s, ctrl('r'))
	if !s.transcript.ReasoningHidden() {
		t.Error("ctrl+r did not hide reasoning")
	}
	s, _ = press(t, s, ctrl('r'))
	if s.transcript.ReasoningHidden() {
		t.Error("ctrl+r did not show reasoning again")
	}
}

// TestHelpOnlyFiresOnAnEmptyComposer: "?" is an ordinary character in a
// question, so swallowing it mid-sentence would be worse than no key.
func TestHelpOnlyFiresOnAnEmptyComposer(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s, cmd := press(t, s, key("?"))
	if cmd == nil {
		t.Fatal("expected help on an empty composer")
	}
	got, ok := cmd().(app.PrintMsg)
	if !ok {
		t.Fatalf("got %T, want app.PrintMsg", cmd())
	}
	for _, want := range []string{"send", "theme", "newline"} {
		if !strings.Contains(ansi.Strip(got.Text), want) {
			t.Errorf("generated help is missing %q:\n%s", want, ansi.Strip(got.Text))
		}
	}

	s = typeText(t, s, "why")
	s, _ = press(t, s, key("?"))
	if v := s.composer.Value(); v != "why?" {
		t.Errorf("got composer %q, want the question mark typed through", v)
	}
}

// TestCompletionMenuClaimsEnterBeforeSend pins rule 5.3.
func TestCompletionMenuClaimsEnterBeforeSend(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommands([]composer.Command{{Name: "model"}, {Name: "modes"}})
	s = typeText(t, s, "/mod")
	if !s.composer.MenuActive() {
		t.Fatal("expected the completion menu open")
	}

	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.active != nil {
		t.Error("Enter sent the turn instead of accepting the completion")
	}
	if got := s.composer.Value(); got != "/model" {
		t.Errorf("got %q, want the highlighted command accepted", got)
	}
	if s.composer.MenuActive() {
		t.Error("the menu stayed open after Enter accepted a command")
	}
}

// TestTabInTheMenuTakesTheCommonPrefix pins that Tab in a menu is a
// completion, not focus movement.
func TestTabInTheMenuTakesTheCommonPrefix(t *testing.T) {
	s := sized(t, 2)
	s.SetCommands([]composer.Command{{Name: "model"}, {Name: "modes"}})
	s = typeText(t, s, "/mo")

	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyTab})
	if got := s.composer.Value(); got != "/mode" {
		t.Errorf("got %q, want the shared prefix of model and modes", got)
	}
	if s.transcript.Focused() {
		t.Error("Tab moved the transcript focus while the menu was open")
	}
}

func TestEscDismissesTheMenuBeforeAnythingElse(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommands([]composer.Command{{Name: "model"}})
	s = typeText(t, s, "/mo")
	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.composer.MenuActive() {
		t.Error("esc did not dismiss the menu")
	}
	if got := s.composer.Value(); got != "/mo" {
		t.Errorf("got %q, want the text kept when the menu is dismissed", got)
	}
}

func TestMenuArrowsMoveTheHighlight(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommands([]composer.Command{{Name: "model"}, {Name: "modes"}})
	s = typeText(t, s, "/mo")
	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyDown})
	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := s.composer.Value(); got != "/modes" {
		t.Errorf("got %q, want the second row accepted after Down", got)
	}
}

// TestApprovalClaimsEveryKey pins the top of the precedence ladder.
func TestApprovalClaimsEveryKey(t *testing.T) {
	s := sized(t, 2)
	n, _ := s.Update(turnEventMsg{ev: uievent.Event{
		Kind: uievent.KindToolPending,
		Body: uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"},
	}})
	scr := n.(Screen)
	if !scr.approval.Active() {
		t.Fatal("expected the approval prompt armed")
	}
	before := scr.composer.Value()
	scr, _ = press(t, scr, shiftTabKey)
	if scr.transcript.Focused() {
		t.Error("Shift-Tab moved the focus while an approval was pending")
	}
	if scr.composer.Value() != before {
		t.Error("a key reached the composer while an approval was pending")
	}
}
