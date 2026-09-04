// Package app is the root Bubble Tea model: a Screen router (a stack, not
// nullable dialog pointers - build spec section 4.5) plus the global
// keymap. It owns no rendering of its own beyond delegating to the top
// of the stack and the app-wide theme, which every Screen renders with
// but only this package mutates.
package app

import (
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// Screen is one pushable unit of the UI: a full base screen (the
// conversation) or a modal (the theme picker, the transcript pager). Its
// Init/Update/View mirror tea.Model; Screen is not tea.Model itself so a
// Screen can be constructed and tested without a running Program.
type Screen interface {
	Init() tea.Cmd
	Update(tea.Msg) (Screen, tea.Cmd)
	View() string
	// ViewFlags reports the terminal modes this screen needs on the ONE
	// tea.View the router assembles. A Screen cannot return a tea.View,
	// so the modes it depends on - today, whether the alternate screen
	// is held - travel through this method instead.
	ViewFlags() ViewFlags
}

// OwnsQuit is implemented by a pushed screen that manages its own
// ctrl+c double-press quit guard (UX Rule 1.3) instead of the router's
// default "a pushed screen quits on the first ctrl+c" behavior. The
// default fits a quick pick-one-and-go dialog (the theme picker, the
// session picker), where there is nothing to lose. A screen with real
// in-flight state - the settings modal's cursor position, filters, and
// pending saves - should implement this and show its own "press again
// to quit" warning, the same way the base screen's statusRow does, so a
// stray ctrl+c cannot discard that state with no warning.
type OwnsQuit interface {
	OwnsQuit() bool
}

// ViewFlags are the per-screen terminal mode requests the router honors.
type ViewFlags struct {
	// AltScreen reports whether the screen holds the terminal's whole
	// drawing surface. The transcript pager clears it while the
	// conversation is handed back to native scrollback
	// (cockpit-research.md rule 6.3): one key writes the transcript into
	// the scrollback and the terminal must be able to show it.
	AltScreen bool
}

// PushScreenMsg asks the router to push a new modal screen onto the
// stack. Emit it as the Msg a Cmd returns; the router applies it and
// calls the new screen's Init.
type PushScreenMsg struct{ Screen Screen }

// PopScreenMsg asks the router to pop the top screen off the stack. A
// pop on a one-screen stack (the base screen) is a no-op: the base
// screen is never dismissed this way.
type PopScreenMsg struct{}

// ScreenResumedMsg is sent by the router to the newly exposed top screen
// after a pushed modal screen is popped from the stack.
type ScreenResumedMsg struct{}

// MouseCaptureMsg flips the app-wide mouse capture live. The settings
// screen's "mouse capture" row sends it (bridged through the program);
// the router applies it and the next View declares the new MouseMode,
// which the renderer writes as ?1002/?1006 on or off. Turning capture
// off hands the mouse back to the terminal - native selection and
// scroll return; turning it on restores in-app drag-select and wheel.
type MouseCaptureMsg struct{ On bool }

// SettingsNoticeMsg carries one permanent, host-authored notice from the
// settings layer into the conversation transcript (routed to the base
// screen's Notice seam). The full-disk live re-arm sends it: lifting
// confinement is never silent, and a transcript notice is part of the
// conversation record, not transient chrome.
type SettingsNoticeMsg struct{ Text string }

// ThemeSelectedMsg asks the router to adopt a new app-wide theme by
// name and pop the screen that offered the choice (the theme picker).
// Theme identity is app-level state no Screen owns, so the message -
// not a direct field mutation - is how a screen changes it.
type ThemeSelectedMsg struct{ Name string }

// ThemeChangedMsg is sent by the router to every Screen on the stack
// after it adopts a new theme, so each Screen (and the components it
// owns) can update its own copy. Theme/Tier are plain value fields on
// Model, Screen, and every component - there is no shared pointer - so
// nothing re-renders with the new theme without this broadcast.
type ThemeChangedMsg struct {
	Theme theme.Theme
	Tier  theme.Tier
}

// Options are the app-wide terminal modes. They come from flags and
// startup probes, not from any Screen, so they live on the router and
// apply to every frame.
type Options struct {
	// Mouse reports whether the cockpit captures the mouse. Rule 6.5
	// makes capture opt-out: mouse capture is the most common friction
	// point over SSH and inside tmux, because it kills copy-on-select.
	Mouse bool

	// FullRepaint forces a complete redraw on resize. Windows Terminal
	// and ConPTY coalesce positioned writes wrongly and leave stale
	// cells (cockpit-research.md section 4), and a full redraw is the
	// recovery. The effect on a real terminal cannot be tested here;
	// the decision function and the ClearScreen Cmd are.
	FullRepaint bool
}

var _ tea.Model = Model{}

// Model is the root tea.Model: current theme/tier, terminal size, the
// screen stack, and the app-wide Options.
type Model struct {
	Theme  theme.Theme
	Tier   theme.Tier
	themes []theme.Theme
	Opts   Options

	// Width and Height are the live terminal size. Bubble Tea sends a
	// WindowSizeMsg on startup and on every resize, so these are the one
	// source of truth for layout; nothing in the tree may assume 80x24.
	Width  int
	Height int

	stack []Screen

	// drag is the mouse drag-select state (mouse_router.go). It only
	// does anything while Opts.Mouse is true - see updateMouse.
	drag dragState
}

// New returns a router with base as the only (non-poppable) screen.
// themes is the full candidate set ThemeSelectedMsg resolves against;
// pass nil if nothing in this Program ever offers a theme picker.
//
// Options.Mouse starts false here; the launcher sets it from config
// ([tui] mouse, default true) with the MIVIA_MOUSE environment
// override, and MouseCaptureMsg changes it live afterwards.
func New(base Screen, th theme.Theme, tier theme.Tier, themes []theme.Theme) Model {
	return Model{
		Theme: th, Tier: tier, themes: slices.Clone(themes),
		Opts:  Options{Mouse: false},
		stack: []Screen{base},
	}
}

// WithOptions sets the app-wide terminal modes and returns the router.
func (m Model) WithOptions(o Options) Model {
	m.Opts = o
	return m
}

func (m Model) top() (Screen, bool) {
	if len(m.stack) == 0 {
		return nil, false
	}
	return m.stack[len(m.stack)-1], true
}

func (m Model) Init() tea.Cmd {
	top, ok := m.top()
	if !ok {
		return nil
	}
	return top.Init()
}

// isInputMsg reports whether msg is direct user input.
//
// Input belongs to the top of the stack alone: a modal must be able to
// claim a key without the screen below acting on it too. Everything
// else - window resizes, Cmd results, stream events - is broadcast, so
// the base screen keeps running while a modal is open. Before this
// split, a pushed screen silently killed the conversation's event read
// loop: the read-continuation Msg went to the top screen, which ignored
// it, so no further event was ever requested.
func isInputMsg(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.KeyReleaseMsg, tea.MouseMsg,
		tea.PasteMsg, tea.FocusMsg, tea.BlurMsg:
		return true
	}
	return false
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.Opts.Mouse {
		// Drag-select intercepts motion and a drag's release outright;
		// an ordinary click and its release, and any non-mouse Msg,
		// fall through unchanged (consume=false) to the switch below.
		// The router's own state lives on *m so it is never discarded
		// by a fall-through - the defect class that broke the old
		// frame-text selection.
		cmd, consume := m.updateMouse(msg)
		if consume {
			return m, cmd
		}
	}
	switch msg := msg.(type) {
	case PushScreenMsg:
		m.cancelDrag()
		sc := msg.Screen
		var resizeCmd tea.Cmd
		if m.Width > 0 && m.Height > 0 {
			var next Screen
			next, resizeCmd = sc.Update(tea.WindowSizeMsg{Width: m.Width, Height: m.Height})
			sc = next
		}
		m.stack = append(slices.Clone(m.stack), sc)
		return m, tea.Batch(sc.Init(), resizeCmd, tea.ClearScreen)
	case PopScreenMsg:
		m.cancelDrag()
		if len(m.stack) > 1 {
			return m.pop()
		}
		return m, nil
	case MouseCaptureMsg:
		// The settings screen (or a future keybinding) flips capture
		// live. The next View declares the new MouseMode and the
		// renderer writes ?1002/?1006 on/off; turning off also drops any
		// in-flight selection so no stale highlight survives.
		m.Opts.Mouse = msg.On
		if !msg.On {
			m.cancelAllSelections()
		}
		return m, nil
	case SettingsNoticeMsg:
		return m.deliverSettingsNotice(msg)
	case ThemeSelectedMsg:
		return m.applyTheme(msg)
	case tea.KeyPressMsg:
		if msg.String() == "esc" && m.drag.armed {
			// Esc cancels an in-flight drag but keeps its normal meaning
			// for the screen underneath.
			m.cancelDrag()
		}
		if msg.String() == "ctrl+c" {
			// Most modals quit immediately; the base screen and any pushed
			// screen that implements OwnsQuit manage their own turn
			// cancellation and double-press quit guard instead (UX Rule 1.3).
			if len(m.stack) > 1 {
				top, _ := m.top()
				owner, ok := top.(OwnsQuit)
				if !ok || !owner.OwnsQuit() {
					return m, tea.Quit
				}
			}
		}
		// Esc is NOT intercepted here. A screen must be able to give Esc a
		// first meaning - the transcript pager cancels an open search with
		// it - and still ask to close itself by returning PopScreenMsg.
	case tea.WindowSizeMsg:
		m.Width, m.Height = msg.Width, msg.Height
		// A resize invalidates the rects and rows the selection's
		// coordinates were measured against.
		m.cancelAllSelections()
	}

	if isInputMsg(msg) {
		return m.deliverTop(msg)
	}
	return m.broadcast(msg)
}

// deliverTop hands a Msg to the top screen only.
func (m Model) deliverTop(msg tea.Msg) (tea.Model, tea.Cmd) {
	top, ok := m.top()
	if !ok {
		return m, nil
	}
	next, cmd := top.Update(msg)
	m.stack = slices.Clone(m.stack)
	m.stack[len(m.stack)-1] = next
	return m, cmd
}

// deliverSettingsNotice routes a permanent, host-authored notice from the
// settings layer to the screen that owns the conversation record, found
// structurally (the Notice seam) so the app package needs no import of the
// concrete conversation screen (which imports app for its own messages).
// While Settings is pushed it stays on the stack UNDER the modal, so the
// notice reaches the transcript and is waiting when the operator pops back.
func (m Model) deliverSettingsNotice(msg SettingsNoticeMsg) (tea.Model, tea.Cmd) {
	// Broadcast hands the message to every screen's Update; the concrete
	// conversation screen owns the transcript and folds the notice into its
	// immutable state, and the returned screens replace the stack.
	return m.broadcast(msg)
}

// broadcast hands a Msg to every screen on the stack and batches the
// Cmds they return.
func (m Model) broadcast(msg tea.Msg) (tea.Model, tea.Cmd) {
	if len(m.stack) == 0 {
		return m, nil
	}
	m.stack = slices.Clone(m.stack)
	cmds := make([]tea.Cmd, 0, len(m.stack))
	for i, sc := range m.stack {
		var cmd tea.Cmd
		m.stack[i], cmd = sc.Update(msg)
		cmds = append(cmds, cmd)
	}
	if m.Opts.FullRepaint {
		if _, ok := msg.(tea.WindowSizeMsg); ok {
			// A resize under full-repaint redraws every cell, so stale
			// cells a coalescing terminal left behind cannot survive.
			cmds = append(cmds, tea.ClearScreen)
		}
	}
	return m, tea.Batch(cmds...)
}

// applyTheme adopts the named theme, broadcasts the change, and pops
// the screen that offered the choice. The pop happens even when the
// name does not resolve: a picker that offered a bad name is dismissed,
// not left blocking the app.
func (m Model) applyTheme(msg ThemeSelectedMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if th, ok := m.themeByName(msg.Name); ok {
		m.Theme = th
		m.stack = slices.Clone(m.stack)
		// Broadcast to every screen on the stack, not just the top (the
		// picker itself): the base screen underneath is the one that
		// actually needs to repaint with the new theme.
		for i, sc := range m.stack {
			var cmd tea.Cmd
			m.stack[i], cmd = sc.Update(ThemeChangedMsg{Theme: th, Tier: m.Tier})
			cmds = append(cmds, cmd)
		}
	}
	if len(m.stack) > 1 {
		next, popCmd := m.pop()
		m = next.(Model)
		cmds = append(cmds, popCmd)
	}
	return m, tea.Batch(cmds...)
}

// pop removes the top screen and clears the terminal: the screen
// beneath drew content the popped screen never touched, and a diffing
// renderer that fails to blank a row neither frame wrote to leaves the
// popped screen's content bleeding through (the same hazard the
// full-repaint resize path guards against, but unconditional here - a
// screen swap is rare, not per-keystroke, so the cost is negligible).
func (m Model) pop() (tea.Model, tea.Cmd) {
	m.stack = slices.Clone(m.stack[:len(m.stack)-1])
	var resumeCmd tea.Cmd
	if top, ok := m.top(); ok {
		var next Screen
		next, resumeCmd = top.Update(ScreenResumedMsg{})
		m.stack[len(m.stack)-1] = next
	}
	return m, tea.Batch(resumeCmd, tea.ClearScreen)
}

// View renders the top of the stack.
//
// The cockpit holds the alternate screen, which is the only interactive
// renderer. v2 declares this on the View rather than as a Program
// option, so the mode is part of the frame and cannot drift from what
// was drawn. The top screen can hand the surface back (rule 6.3) by
// reporting ViewFlags.AltScreen = false.
//
// MouseModeCellMotion, not AllMotion: cell motion reports clicks, drags
// and the wheel, which is everything the transcript needs. AllMotion
// adds an event for every cursor movement over the surface, and that
// traffic buys nothing here. Capture is off entirely while the surface
// is handed back - the terminal's own selection must reach the
// transcript in scrollback - and when Options.Mouse is off (rule 7.1).
// The live-drag highlight is painted by each selectable component in
// its own View (internal/ui/select), so the router adds no overlay.
func (m Model) View() tea.View {
	top, ok := m.top()
	if !ok {
		return tea.NewView("")
	}
	content := top.View()
	v := tea.NewView(content)
	flags := top.ViewFlags()
	v.AltScreen = flags.AltScreen
	if flags.AltScreen && m.Opts.Mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

func (m Model) themeByName(name string) (theme.Theme, bool) {
	for _, th := range m.themes {
		if th.Name == name {
			return th, true
		}
	}
	return theme.Theme{}, false
}
