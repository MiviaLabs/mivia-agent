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

var _ tea.Model = Model{}

// Model is the root tea.Model: current theme/tier plus the screen stack.
type Model struct {
	Theme  theme.Theme
	Tier   theme.Tier
	themes []theme.Theme

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
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			// A modal on top of the base screen: the router owns Esc as
			// "dismiss the modal" globally, so it never reaches the
			// modal's own Update (picker.Model separately handles "esc"
			// for standalone use outside a Screen, but here the router
			// is the single owner).
			if len(m.stack) > 1 {
				m.stack = m.stack[:len(m.stack)-1]
				return m, nil
			}
		}
	case PushScreenMsg:
		m.stack = append(m.stack, msg.Screen)
		return m, msg.Screen.Init()
	case PopScreenMsg:
		if len(m.stack) > 1 {
			m.stack = m.stack[:len(m.stack)-1]
		}
		return m, nil
	case ThemeSelectedMsg:
		if th, ok := m.themeByName(msg.Name); ok {
			m.Theme = th
		}
		if len(m.stack) > 1 {
			m.stack = m.stack[:len(m.stack)-1]
		}
		return m, nil
	}

	top, ok := m.top()
	if !ok {
		return m, nil
	}
	next, cmd := top.Update(msg)
	m.stack[len(m.stack)-1] = next
	return m, cmd
}

func (m Model) View() tea.View {
	top, ok := m.top()
	if !ok {
		return tea.NewView("")
	}
	return tea.NewView(top.View())
}

func (m Model) themeByName(name string) (theme.Theme, bool) {
	for _, th := range m.themes {
		if th.Name == name {
			return th, true
		}
	}
	return theme.Theme{}, false
}
