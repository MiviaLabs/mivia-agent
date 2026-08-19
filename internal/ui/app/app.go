// Package app is the root Bubble Tea model: a Screen router (a stack, not
// nullable dialog pointers - build spec section 4.5) plus the global
// keymap. It owns no rendering of its own beyond delegating to the top
// of the stack and the app-wide theme, which every Screen renders with
// but only this package mutates.
package app

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// Screen is one pushable unit of the UI: a full base screen (the
// conversation) or a modal (the theme picker). Init/Update/View mirror
// tea.Model; Screen is not tea.Model itself so a Screen can be
// constructed and tested without a running Program.
type Screen interface {
	Init() tea.Cmd
	Update(tea.Msg) (Screen, tea.Cmd)
	View() string
}

// PushScreenMsg asks the router to push a new modal screen onto the
// stack. Emit it as the Msg a Cmd returns; the router applies it and
// calls the new screen's Init.
type PushScreenMsg struct{ Screen Screen }

// PopScreenMsg asks the router to pop the top screen off the stack. A
// pop on a one-screen stack (the base screen) is a no-op: the base
// screen is never dismissed this way.
type PopScreenMsg struct{}

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

var _ tea.Model = Model{}

// Model is the root tea.Model: current theme/tier, terminal size, and the
// screen stack.
type Model struct {
	Theme  theme.Theme
	Tier   theme.Tier
	themes []theme.Theme

	// Width and Height are the live terminal size. Bubble Tea sends a
	// WindowSizeMsg on startup and on every resize, so these are the one
	// source of truth for layout; nothing in the tree may assume 80x24.
	Width  int
	Height int

	stack []Screen
}

// New returns a router with base as the only (non-poppable) screen.
// themes is the full candidate set ThemeSelectedMsg resolves against;
// pass nil if nothing in this Program ever offers a theme picker.
func New(base Screen, th theme.Theme, tier theme.Tier, themes []theme.Theme) Model {
	return Model{Theme: th, Tier: tier, themes: themes, stack: []Screen{base}}
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

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Broadcast to every screen, not just the top one: a modal that
		// pops after a resize must reveal a correctly-sized base screen
		// underneath, not one still laid out for the old width.
		m.Width, m.Height = msg.Width, msg.Height
		var cmds []tea.Cmd
		for i, sc := range m.stack {
			var cmd tea.Cmd
			m.stack[i], cmd = sc.Update(msg)
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			// Modals quit immediately; the base screen manages its own turn
			// cancellation and double-press quit guard (UX Rule 1.3).
			if len(m.stack) > 1 {
				return m, tea.Quit
			}
		case "esc":
			// A modal on top of the base screen: the router owns Esc as
			// "dismiss the modal" globally, so it never reaches the
			// modal's own Update (picker.Model separately handles "esc"
			// for standalone use outside a Screen, but here the router
			// is the single owner).
			if len(m.stack) > 1 {
				return m.pop()
			}
		}
	case PushScreenMsg:
		sc := msg.Screen
		var resizeCmd tea.Cmd
		if m.Width > 0 && m.Height > 0 {
			var next Screen
			next, resizeCmd = sc.Update(tea.WindowSizeMsg{Width: m.Width, Height: m.Height})
			sc = next
		}
		m.stack = append(m.stack, sc)
		return m, tea.Batch(sc.Init(), resizeCmd)
	case PopScreenMsg:
		if len(m.stack) > 1 {
			return m.pop()
		}
		return m, nil
	case ThemeSelectedMsg:
		var cmds []tea.Cmd
		if th, ok := m.themeByName(msg.Name); ok {
			m.Theme = th
			// Broadcast to every screen on the stack, not just the top
			// (the picker itself): the base screen underneath is the one
			// that actually needs to repaint with the new theme.
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

	top, ok := m.top()
	if !ok {
		return m, nil
	}
	next, cmd := top.Update(msg)
	m.stack[len(m.stack)-1] = next
	return m, cmd
}

// pop removes the top screen.
func (m Model) pop() (tea.Model, tea.Cmd) {
	m.stack = m.stack[:len(m.stack)-1]
	return m, nil
}

// View renders the top of the stack on the alternate screen, always.
//
// The cockpit is the only interactive renderer. v2 declares this on the
// View rather than as a Program option, so the mode is part of the frame
// and cannot drift from what was drawn.
//
// MouseModeCellMotion, not AllMotion: cell motion reports clicks, drags
// and the wheel, which is everything the transcript needs. AllMotion adds
// an event for every cursor movement over the surface, and that traffic
// buys nothing here.
func (m Model) View() tea.View {
	top, ok := m.top()
	if !ok {
		return tea.NewView("")
	}
	v := tea.NewView(top.View())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
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
