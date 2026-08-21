package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
)

// nextTUIFocus returns the focus cycle for the panes that are visible now.
func (m *TUIModel) nextTUIFocus(current cli.TuiFocus, reverse bool) cli.TuiFocus {
	panes := []cli.TuiFocus{cli.FocusComposer, cli.FocusScrollback}
	if m.sidebarVisible() {
		panes = append(panes, cli.FocusSidebar)
	}
	if m.workflowsSidebarVisible() {
		panes = append(panes, cli.FocusWorkflowsSidebar)
	}
	for i, pane := range panes {
		if pane != current {
			continue
		}
		step := 1
		if reverse {
			step = -1
		}
		return panes[(i+step+len(panes))%len(panes)]
	}
	return cli.FocusComposer
}

func (m *TUIModel) setFocus(focus cli.TuiFocus) {
	if focus == cli.FocusSidebar && !m.sidebarVisible() {
		focus = cli.FocusComposer
	}
	if focus == cli.FocusWorkflowsSidebar && !m.workflowsSidebarVisible() {
		focus = cli.FocusComposer
	}
	m.focus = focus
	if focus == cli.FocusComposer {
		m.textarea.Focus()
	} else {
		m.closeSuggest()
		m.closeHistory()
		m.textarea.Blur()
	}
}

func (m *TUIModel) sidebarVisible() bool {
	return m.sessionsSidebar != nil && NewChatPaneLayout(m.width, m.sessionsSidebar != nil, m.workflowsSidebar != nil).sidebarVisible
}

// workflowsSidebarVisible reports whether the workflows sidebar is open and
// the terminal is wide enough to draw it.
func (m *TUIModel) workflowsSidebarVisible() bool {
	return m.workflowsSidebar != nil && NewChatPaneLayout(m.width, m.sessionsSidebar != nil, m.workflowsSidebar != nil).rightSidebarVisible
}
