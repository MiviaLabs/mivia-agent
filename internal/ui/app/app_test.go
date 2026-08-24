package app

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// stubScreen is a minimal app.Screen for router tests: it records every
// Msg it receives and can optionally return a canned Cmd.
type stubScreen struct {
	name     string
	received []tea.Msg
	cmd      tea.Cmd
	initCmd  tea.Cmd
	flags    ViewFlags
	ownsQuit bool
}

func (s stubScreen) Init() tea.Cmd        { return s.initCmd }
func (s stubScreen) ViewFlags() ViewFlags { return s.flags }
func (s stubScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	s.received = append(s.received, msg)
	return s, s.cmd
}
func (s stubScreen) View() string { return s.name }

// OwnsQuit satisfies the OwnsQuit interface unconditionally, reporting
// the ownsQuit field (false by default). stubScreen therefore always
// implements the interface, unlike a real screen that simply lacks the
// method - the router's type assertion still exercises both outcomes
// through the field: TestCtrlCQuitsFromModal never sets it (false, the
// router's default quit-immediately path), TestCtrlCDelegatesToOwnsQuitModal
// does (true, the opt-out path).
func (s stubScreen) OwnsQuit() bool { return s.ownsQuit }

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

func TestViewRendersTopOfStack(t *testing.T) {
	m := New(stubScreen{name: "base"}, loadTheme(t), theme.TierASCII, nil)
	if got := m.View(); got.Content != "base" {
		t.Errorf("got %q, want the base screen's View()", got.Content)
	}
}

func TestPushScreenMsgPushesAndInits(t *testing.T) {
	initCalled := false
	modal := stubScreen{name: "modal", initCmd: func() tea.Msg { initCalled = true; return nil }}
	m := New(stubScreen{name: "base"}, loadTheme(t), theme.TierASCII, nil)

	next, cmd := m.Update(PushScreenMsg{Screen: modal})
	m = next.(Model)
	if got := m.View(); got.Content != "modal" {
		t.Fatalf("got %q, want the pushed modal on top", got.Content)
	}
	if cmd == nil {
		t.Fatal("expected the pushed screen's Init Cmd")
	}
	// The push also batches tea.ClearScreen now (TestPushScreenMsgClearsScreen),
	// so Init is one leaf among several rather than the whole Cmd.
	for _, c := range batchCmds(t, cmd) {
		c()
	}
	if !initCalled {
		t.Error("expected the pushed screen's Init to have been returned and callable")
	}
}

func TestEscPopsModalNotBase(t *testing.T) {
	// Esc is delivered to the top screen; the router pops only when the
	// screen asks. A modal that wants Esc to dismiss returns PopScreenMsg
	// (the theme picker and the transcript pager both do), because a
	// screen must be free to give Esc a first meaning of its own.
	popOnEsc := func() tea.Cmd { return func() tea.Msg { return PopScreenMsg{} } }
	m := New(stubScreen{name: "base", flags: ViewFlags{AltScreen: true}}, loadTheme(t), theme.TierASCII, nil)

	// Esc on the base screen alone must not pop (nothing to pop) and
	// must reach the base screen's own Update instead.
	next, _ := m.Update(keyMsg("esc"))
	m = next.(Model)
	base := m.stack[0].(stubScreen)
	if len(base.received) != 1 {
		t.Fatalf("expected esc forwarded to the lone base screen, got %d received msgs", len(base.received))
	}

	next, _ = m.Update(PushScreenMsg{Screen: stubScreen{name: "modal", flags: ViewFlags{AltScreen: true}, cmd: popOnEsc()}})
	m = next.(Model)
	if got := m.View(); got.Content != "modal" {
		t.Fatalf("got %q, want modal pushed", got.Content)
	}

	next, cmd := m.Update(keyMsg("esc"))
	m = next.(Model)
	if _, ok := cmd().(PopScreenMsg); !ok {
		t.Fatalf("got %T, want the modal's PopScreenMsg Cmd", cmd())
	}
	next, _ = m.Update(PopScreenMsg{})
	m = next.(Model)
	if got := m.View(); got.Content != "base" {
		t.Errorf("got %q, want esc to pop the modal back to base", got.Content)
	}
}

func TestPopScreenMsgOnBaseIsNoOp(t *testing.T) {
	m := New(stubScreen{name: "base"}, loadTheme(t), theme.TierASCII, nil)
	next, _ := m.Update(PopScreenMsg{})
	m = next.(Model)
	if got := m.View(); got.Content != "base" {
		t.Errorf("got %q, want the base screen to survive a pop with nothing above it", got.Content)
	}
}

func TestPopScreenMsgWithModalPops(t *testing.T) {
	m := New(stubScreen{name: "base"}, loadTheme(t), theme.TierASCII, nil)
	next, _ := m.Update(PushScreenMsg{Screen: stubScreen{name: "modal"}})
	m = next.(Model)
	next, _ = m.Update(PopScreenMsg{})
	m = next.(Model)
	if got := m.View(); got.Content != "base" {
		t.Errorf("got %q, want PopScreenMsg to remove the modal", got.Content)
	}
}

func TestCtrlCDelegatesToBaseScreen(t *testing.T) {
	// ctrl+c on base screen must be delegated so the screen can manage
	// turn cancellation vs double-press quit (UX Rule 1.3).
	base := stubScreen{name: "base"}
	m := New(base, loadTheme(t), theme.TierASCII, nil)
	next, _ := m.Update(keyMsg("ctrl+c"))
	m = next.(Model)
	gotBase := m.stack[0].(stubScreen)
	if len(gotBase.received) != 1 {
		t.Fatalf("expected ctrl+c forwarded to base screen, got %d msgs", len(gotBase.received))
	}
	if k, ok := gotBase.received[0].(tea.KeyPressMsg); !ok || k.String() != "ctrl+c" {
		t.Errorf("got %+v, want ctrl+c KeyPressMsg", gotBase.received[0])
	}
}

func TestCtrlCQuitsFromModal(t *testing.T) {
	m := New(stubScreen{name: "base"}, loadTheme(t), theme.TierASCII, nil)
	next, _ := m.Update(PushScreenMsg{Screen: stubScreen{name: "modal"}})
	m = next.(Model)
	_, cmd := m.Update(keyMsg("ctrl+c"))
	if cmd == nil {
		t.Fatal("expected a Cmd for ctrl+c from modal")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("got %T, want tea.QuitMsg", msg)
	}
}

func TestCtrlCDelegatesToOwnsQuitModal(t *testing.T) {
	// A pushed screen that opts in via OwnsQuit()==true must receive
	// ctrl+c itself, exactly like the base screen does, instead of the
	// router quitting on the first press.
	m := New(stubScreen{name: "base"}, loadTheme(t), theme.TierASCII, nil)
	modal := stubScreen{name: "modal", ownsQuit: true}
	next, _ := m.Update(PushScreenMsg{Screen: modal})
	m = next.(Model)

	next, cmd := m.Update(keyMsg("ctrl+c"))
	m = next.(Model)
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("expected ctrl+c NOT to quit immediately for an OwnsQuit modal")
		}
	}
	got := m.stack[len(m.stack)-1].(stubScreen)
	if len(got.received) != 1 {
		t.Fatalf("expected ctrl+c forwarded to the OwnsQuit modal, got %d received msgs", len(got.received))
	}
	if k, ok := got.received[0].(tea.KeyPressMsg); !ok || k.String() != "ctrl+c" {
		t.Errorf("got %+v, want ctrl+c KeyPressMsg", got.received[0])
	}
}

// bareScreen implements only app.Screen, with no OwnsQuit method at
// all - the shape a real theme/session-picker screen has today, and
// the other branch (the type assertion's ok==false case) the router's
// OwnsQuit check must also handle correctly, distinct from stubScreen's
// OwnsQuit()==false (an implementation present that says no).
type bareScreen struct{ name string }

func (s bareScreen) Init() tea.Cmd                        { return nil }
func (s bareScreen) ViewFlags() ViewFlags                 { return ViewFlags{} }
func (s bareScreen) Update(msg tea.Msg) (Screen, tea.Cmd) { return s, nil }
func (s bareScreen) View() string                         { return s.name }

func TestCtrlCStillQuitsFromModalWithoutOwnsQuit(t *testing.T) {
	m := New(stubScreen{name: "base"}, loadTheme(t), theme.TierASCII, nil)
	next, _ := m.Update(PushScreenMsg{Screen: bareScreen{name: "modal"}})
	m = next.(Model)
	_, cmd := m.Update(keyMsg("ctrl+c"))
	if cmd == nil {
		t.Fatal("expected a Cmd for ctrl+c from a modal with no OwnsQuit method")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("got %T, want tea.QuitMsg", cmd())
	}
}

func TestPushScreenMsgInitializesScreenWithTerminalDimensions(t *testing.T) {
	base := stubScreen{name: "base"}
	m := New(base, loadTheme(t), theme.TierASCII, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)

	modal := stubScreen{name: "modal"}
	next, _ = m.Update(PushScreenMsg{Screen: modal})
	m = next.(Model)

	gotModal := m.stack[1].(stubScreen)
	if len(gotModal.received) != 1 {
		t.Fatalf("expected WindowSizeMsg sent to pushed screen, got %d msgs", len(gotModal.received))
	}
	sz, ok := gotModal.received[0].(tea.WindowSizeMsg)
	if !ok {
		t.Fatalf("got %T, want tea.WindowSizeMsg", gotModal.received[0])
	}
	if sz.Width != 100 || sz.Height != 40 {
		t.Errorf("got %dx%d, want 100x40", sz.Width, sz.Height)
	}
}

func TestThemeSelectedMsgAdoptsThemeAndPops(t *testing.T) {
	dark := loadTheme(t)
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	var light theme.Theme
	for _, th := range themes {
		if th.Name == "mivia-light" {
			light = th
		}
	}
	if light.Name == "" {
		t.Fatal("mivia-light theme not found")
	}

	m := New(stubScreen{name: "base"}, dark, theme.TierASCII, themes)
	next, _ := m.Update(PushScreenMsg{Screen: stubScreen{name: "picker"}})
	m = next.(Model)

	next, cmd := m.Update(ThemeSelectedMsg{Name: light.Name})
	m = next.(Model)

	if m.Theme.Name != light.Name {
		t.Errorf("got theme %q, want %q", m.Theme.Name, light.Name)
	}
	if got := m.View(); got.Content != "base" {
		t.Errorf("got %q, want the picker popped after selection", got.Content)
	}
	_ = cmd // stubScreen returns a nil Cmd, so tea.Batch collapses to nil here; that's correct, not what's under test
	// Both the base screen (still on the stack) and the picker (about to
	// be popped) must have received the broadcast - not just the top.
	base := m.stack[0].(stubScreen)
	if len(base.received) != 2 {
		t.Fatalf("expected the base screen to receive ThemeChangedMsg and ScreenResumedMsg, got %d received msgs", len(base.received))
	}
	got, ok := base.received[0].(ThemeChangedMsg)
	if !ok || got.Theme.Name != light.Name {
		t.Errorf("got %+v, want ThemeChangedMsg{Theme: %s}", base.received[0], light.Name)
	}
	if _, ok := base.received[1].(ScreenResumedMsg); !ok {
		t.Errorf("got %+v, want ScreenResumedMsg", base.received[1])
	}
}

func TestPopScreenMsgSendsScreenResumedMsg(t *testing.T) {
	m := New(stubScreen{name: "base"}, loadTheme(t), theme.TierASCII, nil)
	next, _ := m.Update(PushScreenMsg{Screen: stubScreen{name: "modal"}})
	m = next.(Model)
	next, _ = m.Update(PopScreenMsg{})
	m = next.(Model)
	base := m.stack[0].(stubScreen)
	if len(base.received) != 1 {
		t.Fatalf("expected 1 msg on base screen, got %d", len(base.received))
	}
	if _, ok := base.received[0].(ScreenResumedMsg); !ok {
		t.Errorf("expected ScreenResumedMsg, got %T", base.received[0])
	}
}

func TestThemeSelectedMsgUnknownNameDoesNotBroadcast(t *testing.T) {
	m := New(stubScreen{name: "base"}, loadTheme(t), theme.TierASCII, []theme.Theme{loadTheme(t)})
	next, cmd := m.Update(ThemeSelectedMsg{Name: "does-not-exist"})
	m = next.(Model)
	if cmd != nil {
		t.Error("expected no Cmd when the theme name does not resolve")
	}
	base := m.stack[0].(stubScreen)
	if len(base.received) != 0 {
		t.Errorf("expected no broadcast for an unresolved theme name, got %d received msgs", len(base.received))
	}
}

func TestThemeSelectedMsgUnknownNameLeavesThemeUnchanged(t *testing.T) {
	dark := loadTheme(t)
	m := New(stubScreen{name: "base"}, dark, theme.TierASCII, []theme.Theme{dark})
	next, _ := m.Update(ThemeSelectedMsg{Name: "does-not-exist"})
	m = next.(Model)
	if m.Theme.Name != dark.Name {
		t.Errorf("got theme %q, want unchanged %q", m.Theme.Name, dark.Name)
	}
}

func TestWindowSizeMsgStoredAndBroadcastToEveryScreen(t *testing.T) {
	m := New(stubScreen{name: "base"}, loadTheme(t), theme.TierASCII, nil)
	next, _ := m.Update(PushScreenMsg{Screen: stubScreen{name: "modal"}})
	m = next.(Model)

	next, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)

	if m.Width != 120 || m.Height != 40 {
		t.Errorf("got %dx%d, want the terminal size stored on the router", m.Width, m.Height)
	}
	// Both screens must see it: a modal popping after a resize has to
	// reveal a correctly-sized base screen, not a stale one.
	for i, name := range []string{"base", "modal"} {
		sc := m.stack[i].(stubScreen)
		if len(sc.received) != 1 {
			t.Fatalf("%s screen received %d msgs, want 1", name, len(sc.received))
		}
		if _, ok := sc.received[0].(tea.WindowSizeMsg); !ok {
			t.Errorf("%s screen got %T, want tea.WindowSizeMsg", name, sc.received[0])
		}
	}
}

func TestUnrecognisedMsgForwardsToTopScreen(t *testing.T) {
	m := New(stubScreen{name: "base"}, loadTheme(t), theme.TierASCII, nil)
	next, _ := m.Update(unknownMsg{n: 7})
	m = next.(Model)
	base := m.stack[0].(stubScreen)
	if len(base.received) != 1 {
		t.Fatalf("expected the Msg forwarded to the top screen, got %d received", len(base.received))
	}
	got, ok := base.received[0].(unknownMsg)
	if !ok {
		t.Fatalf("got %T, want unknownMsg forwarded verbatim", base.received[0])
	}
	if got.n != 7 {
		t.Errorf("got %+v, want the Msg forwarded unchanged", got)
	}
}

// TestEmptyStackDefensiveBranches proves top()/Init()/View()/Update() are
// safe on a zero-value stack. New always seeds one screen, so this can
// only happen via direct construction - defensive, but a real path.
func TestBroadcastOnEmptyStackIsSafe(t *testing.T) {
	var m Model
	next, cmd := m.Update(unknownMsg{n: 1})
	if cmd != nil {
		t.Error("expected no Cmd from a broadcast on an empty stack")
	}
	if got := next.(Model).View(); got.Content != "" {
		t.Errorf("got %q, want the empty view", got.Content)
	}
}

func TestEmptyStackDefensiveBranches(t *testing.T) {
	var m Model
	if cmd := m.Init(); cmd != nil {
		t.Error("expected nil Init Cmd on an empty stack")
	}
	if got := m.View(); got.Content != "" {
		t.Errorf("got %q, want empty View() on an empty stack", got.Content)
	}
	next, cmd := m.Update(keyMsg("x"))
	if cmd != nil {
		t.Error("expected no Cmd from Update on an empty stack")
	}
	if got := next.(Model).View(); got.Content != "" {
		t.Errorf("got %q, want Update to be a safe no-op on an empty stack", got.Content)
	}
}

func TestInitDelegatesToTopScreen(t *testing.T) {
	called := false
	base := stubScreen{name: "base", initCmd: func() tea.Msg { called = true; return nil }}
	m := New(base, loadTheme(t), theme.TierASCII, nil)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("expected Init to delegate to the base screen")
	}
	cmd()
	if !called {
		t.Error("expected the base screen's Init Cmd to be returned")
	}
}

// unknownMsg is a Msg type Update has no case for, so it can only reach
// a screen through the fallthrough.
type unknownMsg struct{ n int }

// TestViewFollowsTopScreenFlags pins the handover contract of rule 6.3:
// every cockpit screen holds the alternate screen, but a screen that
// reports ViewFlags.AltScreen=false releases it, because the terminal
// must be able to show the transcript written into native scrollback.
func TestViewFollowsTopScreenFlags(t *testing.T) {
	m := New(stubScreen{name: "base", flags: ViewFlags{AltScreen: true}}, loadTheme(t), theme.TierASCII, nil)
	if !m.View().AltScreen {
		t.Error("a screen that asks for the alternate screen must get it")
	}

	next, _ := m.Update(PushScreenMsg{Screen: stubScreen{name: "handover", flags: ViewFlags{AltScreen: false}}})
	m = next.(Model)
	if m.View().AltScreen {
		t.Error("a screen that reports AltScreen=false must hand the surface back")
	}

	next, _ = m.Update(PopScreenMsg{})
	if !next.(Model).View().AltScreen {
		t.Error("popping back to a holding screen must re-enter the alternate screen")
	}
}

// TestViewMouseDefaultsToNone pins rule 7.1: an agent CLI's output is
// prose, code, diffs and paths - text that exists to be copied. Mouse
// capture removes that, so the default is OFF and the terminal's own
// selection (Cmd-A, click-and-drag, middle-click PRIMARY paste) works
// the moment the cockpit renders. The opt-in is Options{Mouse: true};
// see TestMouseOptionEnablesCapture.
func TestViewMouseDefaultsToNone(t *testing.T) {
	m := New(stubScreen{name: "base", flags: ViewFlags{AltScreen: true}}, loadTheme(t), theme.TierASCII, nil)
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Errorf("got mouse mode %v, want MouseModeNone by default", got)
	}
}

// TestMouseOptionEnablesCapture pins the opt-in path: --mouse opts the
// user into clicks, drags and wheel scrolling at the cost of native
// selection. Cell motion carries clicks, drags and the wheel, which is
// everything the transcript needs; AllMotion adds an event for every
// cursor movement and buys nothing.
func TestMouseOptionEnablesCapture(t *testing.T) {
	m := New(stubScreen{name: "base", flags: ViewFlags{AltScreen: true}}, loadTheme(t), theme.TierASCII, nil).
		WithOptions(Options{Mouse: true})
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Errorf("got mouse mode %v, want CellMotion when Options.Mouse is true", got)
	}
}

// TestMouseOffKeepsNativeSelection pins that setting Options.Mouse to
// false (now also the default) leaves the terminal free to select text
// from the cockpit's own frame. The same assertion holds at the default
// since rule 7.1 makes off the default.
func TestMouseOffKeepsNativeSelection(t *testing.T) {
	m := New(stubScreen{name: "base", flags: ViewFlags{AltScreen: true}}, loadTheme(t), theme.TierASCII, nil).
		WithOptions(Options{Mouse: false})
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Errorf("got mouse mode %v, want none when Options.Mouse is false", got)
	}
}

// TestMouseReleasedWhileHandedOver pins the second half of the capture
// rule: while a screen holds no alternate screen, the mouse must not be
// captured either, or the terminal could not select the transcript that
// was just written into the scrollback.
func TestMouseReleasedWhileHandedOver(t *testing.T) {
	m := New(stubScreen{name: "base", flags: ViewFlags{AltScreen: true}}, loadTheme(t), theme.TierASCII, nil)
	next, _ := m.Update(PushScreenMsg{Screen: stubScreen{name: "handover", flags: ViewFlags{AltScreen: false}}})
	if got := next.(Model).View().MouseMode; got != tea.MouseModeNone {
		t.Errorf("got mouse mode %v, want none while the surface is handed back", got)
	}
}

// TestNonInputMsgsReachTheBaseScreenUnderAModal pins the router rule
// that keeps the conversation alive under a pushed screen: Cmd results
// and stream events are broadcast. Input is not - see
// TestInputGoesToTheTopScreenOnly.
func TestNonInputMsgsReachTheBaseScreenUnderAModal(t *testing.T) {
	m := New(stubScreen{name: "base", flags: ViewFlags{AltScreen: true}}, loadTheme(t), theme.TierASCII, nil)
	next, _ := m.Update(PushScreenMsg{Screen: stubScreen{name: "modal"}})
	m = next.(Model)

	next, _ = m.Update(unknownMsg{n: 3})
	m = next.(Model)
	for i, name := range []string{"base", "modal"} {
		sc := m.stack[i].(stubScreen)
		if len(sc.received) != 1 || sc.received[0].(unknownMsg).n != 3 {
			t.Errorf("%s screen got %v, want the broadcast unknownMsg", name, sc.received)
		}
	}
}

// TestInputGoesToTheTopScreenOnly pins modal key isolation: a keypress
// while a modal is open must never also act on the screen below it.
func TestInputGoesToTheTopScreenOnly(t *testing.T) {
	m := New(stubScreen{name: "base", flags: ViewFlags{AltScreen: true}}, loadTheme(t), theme.TierASCII, nil)
	next, _ := m.Update(PushScreenMsg{Screen: stubScreen{name: "modal"}})
	m = next.(Model)

	next, _ = m.Update(keyMsg("x"))
	m = next.(Model)
	if base := m.stack[0].(stubScreen); len(base.received) != 0 {
		t.Errorf("base screen got %v, want no input while a modal is open", base.received)
	}
	if modal := m.stack[1].(stubScreen); len(modal.received) != 1 {
		t.Errorf("modal screen got %v, want the one keypress", modal.received)
	}
}

// TestFullRepaintClearsScreenOnResize pins the Windows Terminal/ConPTY
// hazard response: in full-repaint mode a resize forces a complete
// redraw, so stale cells cannot survive it.
func TestFullRepaintClearsScreenOnResize(t *testing.T) {
	m := New(stubScreen{name: "base", flags: ViewFlags{AltScreen: true}}, loadTheme(t), theme.TierASCII, nil).
		WithOptions(Options{FullRepaint: true, Mouse: true})
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if cmd == nil {
		t.Fatal("expected a Cmd on resize under full repaint")
	}
	// The batch contains the stub's own nil Cmds plus ClearScreen; run
	// the batch and confirm a clearScreenMsg is among the results.
	want := tea.ClearScreen()
	found := false
	for _, c := range batchCmds(t, cmd) {
		if reflect.DeepEqual(c(), want) {
			found = true
		}
	}
	if !found {
		t.Error("expected tea.ClearScreen in the resize Cmd batch")
	}
}

// TestNoFullRepaintMeansNoClearScreen pins the default: a resize must
// not force a full redraw when the mode is off.
func TestNoFullRepaintMeansNoClearScreen(t *testing.T) {
	m := New(stubScreen{name: "base", flags: ViewFlags{AltScreen: true}}, loadTheme(t), theme.TierASCII, nil)
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	want := tea.ClearScreen()
	for _, c := range batchCmds(t, cmd) {
		if reflect.DeepEqual(c(), want) {
			t.Error("ClearScreen must not run without the full-repaint mode")
		}
	}
}

// TestPushScreenMsgClearsScreen pins the same hazard TestFullRepaintClearsScreenOnResize
// closes for a resize: a pushed screen (theme picker, pager, model
// picker) draws content the screen underneath never did, so a diffing
// renderer that fails to blank a row the new screen doesn't touch
// leaves the old screen's content bleeding through. A screen swap is a
// full-content change, at least as large as a resize, and it is rare
// (a user opening a picker, not every keystroke), so it clears
// unconditionally rather than gating behind --full-repaint the way the
// much higher-frequency resize path does.
func TestPushScreenMsgClearsScreen(t *testing.T) {
	m := New(stubScreen{name: "base", flags: ViewFlags{AltScreen: true}}, loadTheme(t), theme.TierASCII, nil)
	_, cmd := m.Update(PushScreenMsg{Screen: stubScreen{name: "modal", flags: ViewFlags{AltScreen: true}}})
	want := tea.ClearScreen()
	found := false
	for _, c := range batchCmds(t, cmd) {
		if reflect.DeepEqual(c(), want) {
			found = true
		}
	}
	if !found {
		t.Error("expected tea.ClearScreen in the push Cmd batch")
	}
}

// TestPopScreenMsgClearsScreen is the other half: closing a modal
// reveals the screen underneath, which the modal never redrew either.
func TestPopScreenMsgClearsScreen(t *testing.T) {
	m := New(stubScreen{name: "base", flags: ViewFlags{AltScreen: true}}, loadTheme(t), theme.TierASCII, nil)
	next, _ := m.Update(PushScreenMsg{Screen: stubScreen{name: "modal", flags: ViewFlags{AltScreen: true}}})
	m = next.(Model)

	_, cmd := m.Update(PopScreenMsg{})
	want := tea.ClearScreen()
	found := false
	for _, c := range batchCmds(t, cmd) {
		if reflect.DeepEqual(c(), want) {
			found = true
		}
	}
	if !found {
		t.Error("expected tea.ClearScreen in the pop Cmd batch")
	}
}

// batchCmds runs a (possibly batched) Cmd and returns the leaf Cmds it
// contained. tea.Batch runs its leaves concurrently, so the leaves are
// collected from the returned BatchMsg instead of the Cmd itself.
func batchCmds(t *testing.T, cmd tea.Cmd) []tea.Cmd {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		out := make([]tea.Cmd, 0, len(batch))
		for _, c := range batch {
			out = append(out, c)
		}
		return out
	}
	return []tea.Cmd{cmd}
}

func TestAppModelStackImmutability(t *testing.T) {
	th := loadTheme(t)
	themes := []theme.Theme{th}
	base := stubScreen{name: "base"}
	m := New(base, th, theme.TierASCII, themes)

	// Test 1: New clones themes
	themes[0].Name = "MUTATED"
	if m.themes[0].Name == "MUTATED" {
		t.Error("New did not clone themes slice")
	}

	// Test 2: PushScreenMsg then PopScreenMsg then PushScreenMsg preserves old Model stack
	m1, _ := m.Update(PushScreenMsg{Screen: stubScreen{name: "modal1"}})
	model1 := m1.(Model)

	m2, _ := model1.Update(PopScreenMsg{})
	model2 := m2.(Model)

	m3, _ := model2.Update(PushScreenMsg{Screen: stubScreen{name: "modal2"}})
	_ = m3.(Model)

	// Check model1's stack still has modal1 at index 1
	if len(model1.stack) != 2 || model1.stack[1].View() != "modal1" {
		t.Errorf("Push after Pop corrupted previous model stack: got len=%d view=%v", len(model1.stack), model1.stack[1].View())
	}

	// Test 3: deliverTop does not mutate previous stack in place
	m4, _ := model1.Update(keyMsg("a"))
	_ = m4.(Model)
	if baseScreen := model1.stack[1].(stubScreen); len(baseScreen.received) != 0 {
		t.Errorf("deliverTop mutated previous model stack element in place: received=%d", len(baseScreen.received))
	}
}
