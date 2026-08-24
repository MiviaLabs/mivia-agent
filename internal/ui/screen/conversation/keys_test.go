package conversation

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/composer"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/transcript"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
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
		n, _ := scr.Update(uievent.EventMsg{Event: uievent.Event{
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
	if got, want := s.transcript.FocusIndex(), len(s.transcript.Blocks())-1; got != want {
		t.Fatalf("got focus %d, want the newest block %d directly above the composer", got, want)
	}

	s, _ = press(t, s, shiftTabKey)
	if got, want := s.transcript.FocusIndex(), len(s.transcript.Blocks())-2; got != want {
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
	n, _ := scr.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c1", Name: "run_command", OK: true, Result: "x\ny\nz"},
	}})
	scr = n.(Screen)

	scr, _ = press(t, scr, shiftTabKey)
	before := scr.transcript.Blocks()[0].Collapsed
	scr, _ = press(t, scr, key(" "))
	if scr.transcript.Blocks()[0].Collapsed == before {
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
		n, _ := scr.Update(uievent.EventMsg{Event: uievent.Event{
			Kind: uievent.KindToolEnd,
			Body: uievent.ToolEndBody{ToolCallID: string(rune('a' + i)), Name: "edit", OK: true, Result: "1\n2"},
		}})
		scr = n.(Screen)
	}
	scr, _ = press(t, scr, shiftTabKey)

	scr, _ = press(t, scr, ctrl('g'))
	for i, b := range scr.transcript.Blocks() {
		if b.Collapsible && !b.Collapsed {
			t.Errorf("block %d stayed open after collapse-all", i)
		}
	}
	scr, _ = press(t, scr, ctrl('e'))
	for i, b := range scr.transcript.Blocks() {
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
	if !strings.Contains(ansi.Strip(scr.statusRow()), "again to quit") {
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

// TestCtrlCOnAnIdleEmptyComposerArmsQuitFirst: on the first ctrl+c, quit is armed,
// the status hint changes to warn "ctrl+c:press again to quit". Any other key disarms it;
// a second ctrl+c quits.
func TestCtrlCOnAnIdleEmptyComposerArmsQuitFirst(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s, cmd := press(t, s, ctrl('c'))
	if cmd != nil {
		t.Fatal("expected no immediate quit on first press")
	}
	if !s.quitArmed {
		t.Fatal("expected quit to be armed on first press")
	}
	if !strings.Contains(s.statusRow(), "press again to quit") {
		t.Errorf("statusRow missing warning hint:\n%s", s.statusRow())
	}

	// Any other key disarms quit
	s, _ = press(t, s, key("x"))
	if s.quitArmed {
		t.Fatal("key 'x' did not disarm quit")
	}
	if strings.Contains(s.statusRow(), "press again to quit") {
		t.Errorf("statusRow should restore normal hint after disarm:\n%s", s.statusRow())
	}

	// First press arms again
	s, _ = press(t, s, ctrl('c'))
	if !s.quitArmed {
		t.Fatal("expected quit to be armed again")
	}

	// Second press quits
	_, cmd = press(t, s, ctrl('c'))
	if cmd == nil {
		t.Fatal("expected quit on second press")
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
	if s.overlay == "" {
		t.Fatal("expected the help overlay on an empty composer")
	}
	for _, want := range []string{"send", "theme", "newline"} {
		if !strings.Contains(ansi.Strip(s.overlay), want) {
			t.Errorf("generated help is missing %q:\n%s", want, ansi.Strip(s.overlay))
		}
	}
	// The overlay draws over whatever the composer/transcript last drew
	// there; without a clear, that content can bleed through underneath
	// it (see hasClearScreen's doc comment).
	if !hasClearScreen(cmd) {
		t.Error("expected opening the help overlay to clear the screen")
	}
	// The overlay covers the transcript, so the next key dismisses it and
	// does nothing else.
	s, cmd = press(t, s, key("x"))
	if s.overlay != "" {
		t.Error("the overlay survived a keystroke")
	}
	if got := s.composer.Value(); got != "" {
		t.Errorf("the dismissing key reached the composer as %q", got)
	}
	if !hasClearScreen(cmd) {
		t.Error("expected dismissing the help overlay to clear the screen")
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
	n, _ := s.Update(uievent.EventMsg{Event: uievent.Event{
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

// TestThemeKeyWithNoThemesIsInert: the picker would open on an empty
// list, so the key does nothing rather than showing a dead dialog.
func TestThemeKeyWithNoThemesIsInert(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil) // no themes
	_, cmd := press(t, s, ctrl('t'))
	if cmd != nil {
		t.Errorf("got %+v, want no dialog with no themes to pick", cmd())
	}
}

// TestEscWithNothingToCancelFallsThrough: Esc is a global binding, but
// with no turn and no focus it must not swallow the key.
func TestEscWithNothingToCancelFallsThrough(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s, cmd := press(t, s, tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil {
		t.Errorf("got %+v, want nothing to cancel", cmd())
	}
	if s.transcript.Focused() {
		t.Error("esc changed the focus with nothing focused")
	}
}

// TestCtrlOOpensTranscriptMode pins rule 6.2's entry point: ctrl+o
// pushes the transcript pager, and the pager it builds sees the same
// conversation the cockpit holds. The key must never fall through to
// the composer as text.
func TestCtrlOOpensTranscriptMode(t *testing.T) {
	s := sized(t, 2)
	s, cmd := press(t, s, ctrl('o'))
	if got := s.composer.Value(); got != "" {
		t.Errorf("ctrl+o reached the composer as %q", got)
	}
	if cmd == nil {
		t.Fatal("ctrl+o must emit the PushScreenMsg Cmd")
	}
	pushed, ok := cmd().(app.PushScreenMsg)
	if !ok {
		t.Fatalf("ctrl+o emitted %T, want app.PushScreenMsg", cmd())
	}
	pager, ok := pushed.Screen.(transcript.Screen)
	if !ok {
		t.Fatalf("ctrl+o pushed %T, want the transcript pager", pushed.Screen)
	}
	// The router resizes a pushed screen before its first view; do the
	// same here so the pager is measured.
	next, _ := pager.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	pager = next.(transcript.Screen)
	joined := pager.View()
	if !strings.Contains(joined, "notice-a") || !strings.Contains(joined, "notice-b") {
		t.Errorf("the pager must see the conversation's blocks; got:\n%s", joined)
	}
}

// TestCtrlOWithABlockFocusedStillOpensThePager: the pager key is global
// in every focus state.
func TestCtrlOWithABlockFocusedStillOpensThePager(t *testing.T) {
	s := sized(t, 2)
	s, _ = press(t, s, shiftTabKey)
	s, cmd := press(t, s, ctrl('o'))
	if cmd == nil {
		t.Fatal("ctrl+o must emit the PushScreenMsg Cmd with a block focused")
	}
	if _, ok := cmd().(app.PushScreenMsg); !ok {
		t.Fatalf("ctrl+o emitted %T, want app.PushScreenMsg", cmd())
	}
}

// TestMenuDismissThenEscCancels pins the ladder's second rung: once the
// menu is gone, Esc reaches the turn.
func TestMenuDismissThenEscReachesTheTurn(t *testing.T) {
	events := []uievent.Event{{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "hi"}}}
	s := newScreen(t, replay.New(events, time.Hour), nil, nil)
	s.SetCommands([]composer.Command{{Name: "model"}})
	s = typeText(t, s, "hi")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	scr := next.(Screen)
	scr = typeText(t, scr, "/mo")

	scr, _ = press(t, scr, tea.KeyPressMsg{Code: tea.KeyEscape})
	if scr.composer.MenuActive() {
		t.Fatal("the first esc must dismiss the menu")
	}
	if !scr.statusline.Active() {
		t.Fatal("expected the turn still running after the menu closed")
	}

	scr, _ = press(t, scr, tea.KeyPressMsg{Code: tea.KeyEscape})
	if scr.statusline.Active() {
		t.Error("the second esc did not cancel the turn")
	}
}

// TestTabWithOneMatchAcceptsIt: the common prefix is the whole name, so
// the prefix step adds nothing and Tab must still complete.
func TestTabWithOneMatchAcceptsIt(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommands([]composer.Command{{Name: "quit"}})
	s = typeText(t, s, "/quit")
	if !s.composer.MenuActive() {
		t.Fatal("expected the menu open on an exact single match")
	}
	s, _ = press(t, s, tabKey)
	if s.composer.MenuActive() {
		t.Error("Tab did nothing visible; it must accept when the prefix cannot grow")
	}
	if got := s.composer.Value(); got != "/quit" {
		t.Errorf("got %q, want the command accepted", got)
	}
}

// TestComposerOnlyBindingsFallThroughToTheInput covers the composer
// context's fallthrough. ctrl+u and ctrl+j are bound in the table so
// they appear in the generated help, but the text input implements them,
// so the screen must pass them on rather than swallow them.
func TestComposerOnlyBindingsFallThroughToTheInput(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s = typeText(t, s, "hello")
	s, _ = press(t, s, ctrl('u'))
	if got := s.composer.Value(); got != "" {
		t.Errorf("got %q, want ctrl+u handled by the input as clear-line", got)
	}
}

// TestGlobalFallthroughForAnUnhandledID pins the defensive arm: a
// binding added to the global context with no case here must fall
// through to the composer, never be silently swallowed.
func TestGlobalFallthroughForAnUnhandledID(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	_, cmd, handled := s.globalAction(keymap.ID("not-a-real-action"))
	if handled {
		t.Error("an unknown global action reported itself handled")
	}
	if cmd != nil {
		t.Errorf("got %+v, want no Cmd for an unknown action", cmd())
	}
}

// TestComposerFallthroughForAnUnhandledID is the same guard one context
// down.
func TestComposerFallthroughForAnUnhandledID(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	if _, _, handled := s.composerAction(keymap.ID("not-a-real-action")); handled {
		t.Error("an unknown composer action reported itself handled")
	}
}

func TestMenuUpArrowMovesTheHighlight(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommands([]composer.Command{{Name: "model"}, {Name: "modes"}})
	s = typeText(t, s, "/mo")
	// Up from the first row wraps to the last.
	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyUp})
	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := s.composer.Value(); got != "/modes" {
		t.Errorf("got %q, want Up to wrap to the last row", got)
	}
}

// TestScrollKeysMoveTheViewport pins the cockpit's replacement for
// native terminal scrolling. The alternate screen has none, so these
// keys are the only way to read what left the screen.
func TestScrollKeysMoveTheViewport(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	scr := next.(Screen)
	for i := 0; i < 40; i++ {
		n, _ := scr.Update(uievent.EventMsg{Event: uievent.Event{
			Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: fmt.Sprintf("notice %d", i)},
		}})
		scr = n.(Screen)
	}
	if !scr.transcript.Following() {
		t.Fatal("a fresh transcript follows the tail")
	}

	scr, _ = press(t, scr, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if scr.transcript.Following() {
		t.Error("page up did not pause auto-follow")
	}
	up := scr.transcript.Offset()

	scr, _ = press(t, scr, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if scr.transcript.Offset() <= up {
		t.Error("page down did not move back towards the tail")
	}

	scr, _ = press(t, scr, tea.KeyPressMsg{Code: tea.KeyHome, Mod: tea.ModCtrl})
	if scr.transcript.Offset() != 0 {
		t.Errorf("got offset %d, want the start of the conversation", scr.transcript.Offset())
	}
	if !strings.Contains(ansi.Strip(scr.View()), "notice 0") {
		t.Error("the oldest block is not on screen at the top")
	}

	scr, _ = press(t, scr, tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl})
	if !scr.transcript.Following() {
		t.Error("ctrl+end did not resume auto-follow")
	}
}

// TestWheelScrollsTheTranscript pins the mouse path. The wheel is the
// gesture most users reach for first, and the cockpit captures it.
func TestWheelScrollsTheTranscript(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	scr := next.(Screen)
	for i := 0; i < 40; i++ {
		n, _ := scr.Update(uievent.EventMsg{Event: uievent.Event{
			Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: fmt.Sprintf("notice %d", i)},
		}})
		scr = n.(Screen)
	}
	bottom := scr.transcript.Offset()

	n, _ := scr.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	scr = n.(Screen)
	if got, want := scr.transcript.Offset(), bottom-uikitconfig.CockpitScrollLines; got != want {
		t.Errorf("got offset %d after one notch up, want %d", got, want)
	}

	n, _ = scr.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	scr = n.(Screen)
	if got := scr.transcript.Offset(); got != bottom {
		t.Errorf("got offset %d after one notch back down, want %d", got, bottom)
	}
}

// TestStatusRowStatesWhenScrolledAway pins the affordance: a reader who
// scrolled up must be told how to get back.
func TestStatusRowStatesWhenScrolledAway(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	scr := next.(Screen)
	for i := 0; i < 40; i++ {
		n, _ := scr.Update(uievent.EventMsg{Event: uievent.Event{
			Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: fmt.Sprintf("notice %d", i)},
		}})
		scr = n.(Screen)
	}
	if got := ansi.Strip(scr.statusRow()); got != "?:help  ctrl+o:transcript  ctrl+n:sidebar  ctrl+c:quit" {
		t.Errorf("got %q, want only the persistent key hint while following", got)
	}

	scr, _ = press(t, scr, tea.KeyPressMsg{Code: tea.KeyPgUp})
	got := ansi.Strip(scr.statusRow())
	if !strings.Contains(got, "scrolled up") || !strings.Contains(got, "ctrl+end") {
		t.Errorf("got %q, want the scrolled-away state and the way back", got)
	}
}

// TestStatusRowStatesTruncation: the transcript is bounded, so it must
// say when it dropped the start rather than pretend the session began
// where it now begins.
func TestStatusRowStatesTruncation(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	scr := next.(Screen)
	for i := 0; i < uikitconfig.MaxTranscriptLines+3; i++ {
		n, _ := scr.Update(uievent.EventMsg{Event: uievent.Event{
			Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: fmt.Sprintf("notice %d", i)},
		}})
		scr = n.(Screen)
	}
	if got := ansi.Strip(scr.statusRow()); !strings.Contains(got, "dropped") {
		t.Errorf("got %q, want the status row to state the truncation", got)
	}
}

// TestOverlayFillsTheTranscriptRows: an overlay must claim exactly the
// transcript's rows, or the chrome below it moves when it opens.
func TestOverlayFillsTheTranscriptRows(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	scr := next.(Screen)
	before := len(strings.Split(scr.View(), "\n"))

	scr, _ = press(t, scr, key("?"))
	if scr.overlay == "" {
		t.Fatal("expected the help overlay")
	}
	if got := len(strings.Split(scr.View(), "\n")); got != before {
		t.Errorf("the view is %d rows with the overlay open and %d without", got, before)
	}
}

// TestOverlayTallerThanTheScreenIsClipped: a long keymap must not push
// the composer off the bottom.
func TestOverlayTallerThanTheScreenIsClipped(t *testing.T) {
	rows := overlayRows(strings.Repeat("row\n", 100), 5)
	if len(rows) != 5 {
		t.Errorf("got %d rows, want the overlay clipped to 5", len(rows))
	}
	if got := overlayRows("one", 0); len(got) != 1 {
		t.Errorf("got %d rows at an unknown height, want the text unchanged", len(got))
	}
}

func TestOverlayShorterThanTheScreenIsPadded(t *testing.T) {
	rows := overlayRows("one\ntwo", 6)
	if len(rows) != 6 {
		t.Errorf("got %d rows, want the overlay padded to 6", len(rows))
	}
	if rows[0] != "one" || rows[1] != "two" {
		t.Errorf("got %q, want the content kept at the top", rows[:2])
	}
}

// TestHelpOverlayStatesTheMouseOverrideKey pins rule 6.5: the terminal's
// own override key is on screen, because mouse capture is the most
// common friction point over SSH and inside tmux.
func TestHelpOverlayStatesTheMouseOverrideKey(t *testing.T) {
	s := sized(t, 0)
	s.SetMouseOverrideHint("Option")
	next, _ := s.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	s = next.(Screen)
	if !strings.Contains(s.overlay, "Option") {
		t.Errorf("help overlay must state the override key, got:\n%s", s.overlay)
	}
}

// TestNoticeRecordsAStartupWarning: hazard warnings are permanent
// transcript blocks, drawn once, not transient chrome.
func TestNoticeRecordsAStartupWarning(t *testing.T) {
	s := sized(t, 0)
	s.Notice("old tmux has no synchronized output")
	if !strings.Contains(s.View(), "old tmux has no synchronized output") {
		t.Error("the notice must render in the transcript")
	}
}

// TestStatusRowAlwaysShowsTheHint pins the persistent footer: the key
// hint is on the status row even when no turn is in flight, stays one
// line, and truncates to the terminal width on narrow screens.
func TestStatusRowAlwaysShowsTheHint(t *testing.T) {
	s := sized(t, 1)
	got := ansi.Strip(s.statusRow())
	for _, want := range []string{"?:help", "ctrl+o:transcript", "ctrl+c:quit"} {
		if !strings.Contains(got, want) {
			t.Errorf("status row %q does not show %q", got, want)
		}
	}
	if n := len(strings.Split(s.View(), "\n")); n == 0 {
		t.Error("status row broke the view")
	}

	narrow, _ := newScreen(t, replay.New(nil, 0), nil, nil).Update(tea.WindowSizeMsg{Width: 20, Height: 10})
	row := ansi.Strip(narrow.(Screen).statusRow())
	if w := ansi.StringWidth(row); w > 20 {
		t.Errorf("status row width %d exceeds 20: %q", w, row)
	}
}

// pendingDiffScreen returns a screen with a 31-line diff awaiting
// approval, sized and ready to scroll.
func pendingDiffScreen(t *testing.T) Screen {
	t.Helper()
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	scr := next.(Screen)
	scr.approval.SetRequest(uievent.ToolPendingBody{
		ToolCallID: "c1", Name: "edit_file",
		Diff: &uievent.Diff{
			Path: "a.go",
			Hunks: []uievent.DiffHunk{{
				Header: "@@ -1 +1 @@",
				Lines: func() []uievent.DiffLine {
					lines := make([]uievent.DiffLine, 30)
					for i := range lines {
						lines[i] = uievent.DiffLine{Kind: uievent.DiffLineAdd, Text: fmt.Sprintf("line %d", i)}
					}
					return lines
				}(),
			}},
		},
	})
	return scr
}

// TestApprovalScrollKeysWindowTheDiff pins the key routing: up/down (and
// the k/j spellings) scroll the preview, the request stays pending, and
// the decision keys still resolve after scrolling.
func TestApprovalScrollKeysWindowTheDiff(t *testing.T) {
	scr := pendingDiffScreen(t)
	for _, k := range []string{"down", "j"} {
		var msg tea.KeyPressMsg
		if k == "down" {
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		} else {
			msg = tea.KeyPressMsg{Code: 'j'}
		}
		next, _ := scr.Update(msg)
		scr = next.(Screen)
	}
	if !strings.Contains(ansi.Strip(scr.View()), "lines 3-12 of 31") {
		t.Errorf("two scroll keys did not move the window by 2:\n%s", scr.View())
	}
	if !scr.approval.Active() {
		t.Fatal("a scroll key resolved the approval")
	}
	next, cmd := scr.Update(tea.KeyPressMsg{Code: 'o'})
	scr = next.(Screen)
	if cmd == nil || scr.approval.Active() {
		t.Error("o no longer approves after scrolling")
	}
}

// TestApprovalWheelScrollsThePreviewNotTheTranscript pins the mouse
// routing: while an approval is pending, the wheel windows the diff
// preview and the transcript behind it does not move.
func TestApprovalWheelScrollsThePreviewNotTheTranscript(t *testing.T) {
	scr := pendingDiffScreen(t)
	before := scr.transcript.Offset()
	next, _ := scr.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	scr = next.(Screen)
	// One wheel notch is CockpitScrollLines: the window must have moved
	// by exactly that much, not merely shown a position row.
	want := fmt.Sprintf("lines %d-%d of 31", 1+uikitconfig.CockpitScrollLines, uikitconfig.CockpitScrollLines+10)
	if !strings.Contains(ansi.Strip(scr.View()), want) {
		t.Errorf("wheel did not move the preview window to %q:\n%s", want, scr.View())
	}
	if got := scr.transcript.Offset(); got != before {
		t.Errorf("wheel moved the transcript (%d -> %d) behind a modal approval", before, got)
	}
}

// TestCtrlCIsTheEmergencyExitUnderApproval pins the one global key that
// must not be swallowed by the modal: during a pending approval, the
// first ctrl+c cancels the turn and clears the prompt, and the second
// quits. Before this, ctrl+c was silently eaten and esc-deny was the
// only way out.
func TestCtrlCIsTheEmergencyExitUnderApproval(t *testing.T) {
	scr := pendingDiffScreen(t)
	scr.active = fakeHandle{id: "t1"}
	next, _ := scr.Update(ctrl('c'))
	scr = next.(Screen)
	if scr.approval.Active() {
		t.Error("first ctrl+c left the approval pending")
	}
	if !scr.quitArmed {
		t.Error("first ctrl+c did not arm the quit state")
	}
	next, cmd := scr.Update(ctrl('c'))
	scr = next.(Screen)
	if cmd == nil {
		t.Error("second ctrl+c did not quit")
	}
}

// TestApprovalStillSwallowsUnrelatedKeys: the modal must keep blocking
// the screen behind it - only scroll and ctrl+c pass around the
// decision keys.
func TestApprovalStillSwallowsUnrelatedKeys(t *testing.T) {
	scr := pendingDiffScreen(t)
	next, _ := scr.Update(tea.KeyPressMsg{Code: 'x'})
	scr = next.(Screen)
	if !scr.approval.Active() {
		t.Error("an unrelated key escaped the approval modal")
	}
	if scr.composer.Value() != "" {
		t.Error("an unrelated key reached the composer through the modal")
	}
}

// TestViewHasAOneColumnGutter pins the framing rule: no row starts or
// ends at the screen edge - every row is blank in column 0 and the last
// column, and every row is exactly the terminal width.
func TestViewHasAOneColumnGutter(t *testing.T) {
	s := pendingDiffScreen(t) // exercise top bar, transcript, approval, status, composer
	next, _ := s.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	scr := next.(Screen)
	rows := strings.Split(scr.View(), "\n")
	if len(rows) != 30 {
		t.Fatalf("view is %d rows in a 30-row terminal", len(rows))
	}
	for i, row := range rows {
		if w := ansi.StringWidth(row); w != 60 {
			t.Errorf("row %d width %d, want 60", i, w)
		}
		plain := ansi.Strip(row)
		if plain[0] != ' ' {
			t.Errorf("row %d touches the left edge: %q", i, plain[:min(20, len(plain))])
		}
		if plain[len(plain)-1] != ' ' {
			t.Errorf("row %d touches the right edge", i)
		}
	}
	// The persistent pieces are present: brand mark + wordmark top row,
	// framed composer at the bottom.
	if !strings.Contains(ansi.Strip(rows[0]), "mivia") {
		t.Errorf("top row is not the brand bar: %q", rows[0])
	}
	var foundInput bool
	for _, row := range rows {
		if strings.Contains(ansi.Strip(row), "> ") || strings.Contains(ansi.Strip(row), "› ") {
			foundInput = true
			break
		}
	}
	if !foundInput {
		t.Errorf("input row not found in view")
	}
}

// TestCtrlNWhilePanelFocusedIsHandledByThePanel pins the dispatch
// order: with the panel's list focused, ctrl+n is consumed by the panel
// (focus returns to the composer, panel stays open) rather than
// reaching the global cycle's close step. The full cycle, layout, and
// live-update coverage lives in filespanel_test.go.
func TestCtrlNWhilePanelFocusedIsHandledByThePanel(t *testing.T) {
	s := sized(t, 1)
	next, _ := s.Update(tea.WindowSizeMsg{Width: uikitconfig.BreakpointWide, Height: 24})
	scr := next.(Screen)
	scr, _ = press(t, scr, ctrl('n')) // open + focus
	if !scr.panel.open || !scr.panel.focused {
		t.Fatalf("precondition: panel open and focused, got open=%v focused=%v", scr.panel.open, scr.panel.focused)
	}
	scr, _ = press(t, scr, ctrl('n')) // focused: hand focus back, stay open
	if !scr.panel.open || scr.panel.focused {
		t.Errorf("second ctrl+n: open=%v focused=%v, want open with composer focus", scr.panel.open, scr.panel.focused)
	}
	// The composer takes keys again: typing lands in the input.
	scr, _ = press(t, scr, key("h"))
	if got := scr.composer.Value(); got != "h" {
		t.Errorf("composer value %q after defocus, want \"h\"", got)
	}
}

func TestTabSwitchesFocusBetweenPanelAndComposer(t *testing.T) {
	s := sized(t, 1)
	next, _ := s.Update(tea.WindowSizeMsg{Width: uikitconfig.BreakpointWide, Height: 24})
	scr := next.(Screen)
	scr, _ = press(t, scr, ctrl('n')) // open + focus
	if !scr.panel.open || !scr.panel.focused {
		t.Fatalf("precondition: panel open and focused")
	}

	// Tab while panel focused switches focus to composer
	scr, _ = press(t, scr, key("tab"))
	if !scr.panel.open || scr.panel.focused {
		t.Errorf("tab while panel focused: open=%v focused=%v, want open with composer focus", scr.panel.open, scr.panel.focused)
	}

	// Tab while composer focused switches focus back to panel
	scr, _ = press(t, scr, key("tab"))
	if !scr.panel.open || !scr.panel.focused {
		t.Errorf("tab while composer focused: open=%v focused=%v, want open with panel focus", scr.panel.open, scr.panel.focused)
	}
}

func TestTabWithSlashCommandHasCompletionPriority(t *testing.T) {
	s := sized(t, 1)
	s.SetCommands([]composer.Command{{Name: "help", Desc: "show help"}, {Name: "history", Desc: "show history"}})
	next, _ := s.Update(tea.WindowSizeMsg{Width: uikitconfig.BreakpointWide, Height: 24})
	scr := next.(Screen)
	scr, _ = press(t, scr, ctrl('n'))  // open + focus
	scr, _ = press(t, scr, key("tab")) // switch focus to composer
	if !scr.panel.open || scr.panel.focused {
		t.Fatalf("precondition: open with composer focus")
	}

	// Type "/" to open completion menu
	scr, _ = press(t, scr, key("/"))
	if !scr.composer.MenuActive() {
		t.Fatalf("menu not active after typing /")
	}

	// Tab should complete common prefix /h, not switch to panel
	scr, _ = press(t, scr, key("tab"))
	if scr.panel.focused {
		t.Errorf("tab in completion menu stole focus to panel")
	}
	if got := scr.composer.Value(); got != "/h" {
		t.Errorf("tab did not accept common prefix: got %q, want \"/h\"", got)
	}
}

func TestConversationScreenMentionMenuKeys(t *testing.T) {
	s := sized(t, 0)
	s.SetMentions([]composer.Mention{
		{Path: "internal/ui/component/composer/composer.go"},
		{Path: "internal/ui/component/composer/mention.go"},
	})
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	scr := next.(Screen)

	// Type "@" to open mention menu
	scr, _ = press(t, scr, key("@"))
	if !scr.composer.MentionMenuActive() {
		t.Fatalf("mention menu not active after typing @")
	}

	// Down arrow moves cursor
	scr, _ = press(t, scr, tea.KeyPressMsg{Code: tea.KeyDown})
	// Up arrow moves cursor
	scr, _ = press(t, scr, tea.KeyPressMsg{Code: tea.KeyUp})

	// Enter accepts the selected mention
	scr, _ = press(t, scr, tea.KeyPressMsg{Code: tea.KeyEnter})
	if scr.composer.MentionMenuActive() {
		t.Errorf("mention menu should be closed after Enter")
	}
	if !strings.Contains(scr.composer.Value(), "@internal/ui/component/composer/composer.go") {
		t.Errorf("expected mention path inserted, got %q", scr.composer.Value())
	}

	// Test Esc dismisses mention menu
	scr.composer.Clear()
	scr, _ = press(t, scr, key("@"))
	if !scr.composer.MentionMenuActive() {
		t.Fatalf("mention menu not active")
	}
	scr, _ = press(t, scr, tea.KeyPressMsg{Code: tea.KeyEscape})
	if scr.composer.MentionMenuActive() {
		t.Errorf("mention menu should be dismissed after Esc")
	}
}

// TestGutterClipsAnOverflowingRowWithTheClipMarker pins
// wireframes-panes.md section 8/14's shared clip glyph: gutter is the
// screen-edge fallback clip for any row wider than the frame, and it
// must mark the cut the same way every other clipped row in the tree
// does (render.split.go's clipBlock, render.dialog.go's dialogClip,
// the session picker) rather than truncating silently.
func TestGutterClipsAnOverflowingRowWithTheClipMarker(t *testing.T) {
	s := pendingDiffScreen(t)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
	scr := next.(Screen)
	got := scr.gutter([]string{strings.Repeat("x", 200)})
	if !strings.Contains(got, uikitconfig.ClipMarker) {
		t.Errorf("got %q, want the clip marker %q on the overflowing row", got, uikitconfig.ClipMarker)
	}
}
