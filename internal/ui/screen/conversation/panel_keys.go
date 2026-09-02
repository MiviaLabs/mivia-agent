// panel_keys.go holds the files-panel's own key routing: the list
// (handlePanelListKey) and its content dialog (panelDialogKey). Split out
// of keys.go for the same reason cancel_tool_call.go and
// cancel_subagent_task.go were (INV: files stay under the ~500 LOC soft
// cap / 800 hard cap) - keys.go was at the hard cap before this slice
// added keymap.IDCancelSubagentTask's routing.
package conversation

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/picker"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
)

func (s Screen) handlePanelListKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd, bool) {
	if id, ok := s.keys.Match(keymap.ContextFiles, msg.String()); ok {
		switch id {
		case keymap.IDCancel:
			s.panelFocus(false)
			return s, nil, true
		case keymap.IDCancelSubagentTask:
			next, cmd := s.cancelSelectedSubagentTask()
			return next, cmd, true
		case keymap.IDPagerRowUp:
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case keymap.IDPagerRowDown:
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		}
	}
	// Sidebar navigation: only arrow/nav keys and Enter act on the list (no search filter)
	switch msg.Code {
	case tea.KeyUp, tea.KeyDown, tea.KeyHome, tea.KeyEnd, tea.KeyPgUp, tea.KeyPgDown, tea.KeyEnter:
		// allowed nav keys
	default:
		if msg.String() == "j" {
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		} else if msg.String() == "k" {
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		} else {
			return s, nil, true
		}
	}
	next, cmd := s.panel.list.Update(msg)
	s.panel.list = next
	s.panel.offset = 0 // a moved selection restarts the content at its top
	if cmd != nil {
		if _, ok := cmd().(picker.SelectMsg); ok && s.panelDialogFits() {
			// Enter on a subagent row opens its thread when one
			// resolves (openThread builds or reuses the embedded
			// screen); either way the dialog is named for the agent.
			// A file row keeps the diff/source dialog.
			if a, isAgent := s.panel.selectedAgent(); isAgent {
				s.panel.dialogAgent = a.ID
				_, openCmd := s.openThread(a.ID)
				s.panel.dialog, s.panel.offset = true, 0
				return s, openCmd, true
			} else {
				s.panel.dialogAgent = ""
			}
			s.panel.dialog, s.panel.offset = true, 0
		}
	}
	return s, nil, true
}

// panelDialogKey applies the content dialog's one rule: any key closes
// it back to the list, except the view toggle, the half-page scrolls,
// and the emergency exit (which closes it and runs the ordinary quit
// flow, so the second-press warning lands on a visible status row).
func (s Screen) panelDialogKey(msg tea.KeyPressMsg) app.Screen {
	if msg.String() == "ctrl+c" {
		s.panel.dialog = false
		next, _, _ := s.quit()
		return next
	}
	switch msg.String() {
	case "up", "k":
		s.scrollPanel(-1)
		return s
	case "down", "j":
		s.scrollPanel(1)
		return s
	case "pgup":
		s.scrollPanel(-max(1, s.panelBodyRows()/2))
		return s
	case "pgdown":
		s.scrollPanel(max(1, s.panelBodyRows()/2))
		return s
	case "home":
		s.panel.offset = 0
		return s
	case "end":
		s.panel.offset = 100000
		s.scrollPanel(0)
		return s
	}
	if id, ok := s.keys.Match(keymap.ContextFiles, msg.String()); ok {
		switch id {
		case keymap.IDFileToggleView:
			s.panel.sourceView = !s.panel.sourceView
			s.panel.offset = 0
			return s
		case keymap.IDPagerHalfUp:
			s.scrollPanel(-1)
			return s
		case keymap.IDPagerHalfDown:
			s.scrollPanel(1)
			return s
		case keymap.IDCancel:
			s.panel.dialog, s.panel.dialogAgent = false, ""
			s.panel.offset = 0
			s.closeThread()
			return s
		}
	}
	s.panel.dialog, s.panel.dialogAgent = false, ""
	s.panel.offset = 0
	s.closeThread()
	return s
}
