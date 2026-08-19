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
}

func (s stubScreen) Init() tea.Cmd        { return s.initCmd }
func (s stubScreen) ViewFlags() ViewFlags { return s.flags }
func (s stubScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	s.received = append(s.received, msg)
	return s, s.cmd
}
func (s stubScreen) View() string { return s.name }

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
	cmd()
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
	if len(base.received) != 1 {
		t.Fatalf("expected the base screen to receive ThemeChangedMsg, got %d received msgs", len(base.received))
	}
	got, ok := base.received[0].(ThemeChangedMsg)
	if !ok || got.Theme.Name != light.Name {
		t.Errorf("got %+v, want ThemeChangedMsg{Theme: %s}", base.received[0], light.Name)
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

// TestViewRequestsCellMotionMouse pins the mode. Cell motion carries
// clicks, drags and the wheel, which is everything the transcript needs.
// All-motion adds an event for every cursor movement and buys nothing.
func TestViewRequestsCellMotionMouse(t *testing.T) {
	m := New(stubScreen{name: "base", flags: ViewFlags{AltScreen: true}}, loadTheme(t), theme.TierASCII, nil)
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Errorf("got mouse mode %v, want tea.MouseModeCellMotion", got)
	}
}

// TestNoMouseOptionReleasesCapture pins rule 6.5: --no-mouse keeps the
// cockpit and drops mouse capture, so the terminal's own
// copy-on-select works over SSH and inside tmux.
func TestNoMouseOptionReleasesCapture(t *testing.T) {
	m := New(stubScreen{name: "base", flags: ViewFlags{AltScreen: true}}, loadTheme(t), theme.TierASCII, nil).
		WithOptions(Options{Mouse: false})
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Errorf("got mouse mode %v, want none under --no-mouse", got)
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
