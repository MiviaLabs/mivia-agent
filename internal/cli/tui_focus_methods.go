package cli

// nextTUIFocus returns the focus cycle for the panes that are visible now.
func (m *tuiModel) nextTUIFocus(current tuiFocus, reverse bool) tuiFocus {
	panes := []tuiFocus{focusComposer, focusScrollback}
	if m.sidebarVisible() {
		panes = append(panes, focusSidebar)
	}
	if m.workflowsSidebarVisible() {
		panes = append(panes, focusWorkflowsSidebar)
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
	return focusComposer
}

func (m *tuiModel) setFocus(focus tuiFocus) {
	if focus == focusSidebar && !m.sidebarVisible() {
		focus = focusComposer
	}
	if focus == focusWorkflowsSidebar && !m.workflowsSidebarVisible() {
		focus = focusComposer
	}
	m.focus = focus
	if focus == focusComposer {
		m.textarea.Focus()
	} else {
		m.closeSuggest()
		m.closeHistory()
		m.textarea.Blur()
	}
}

func (m *tuiModel) sidebarVisible() bool {
	return m.sessionsSidebar != nil && newChatPaneLayout(m.width, m.sessionsSidebar != nil, m.workflowsSidebar != nil).sidebarVisible
}

// workflowsSidebarVisible reports whether the workflows sidebar is open and
// the terminal is wide enough to draw it.
func (m *tuiModel) workflowsSidebarVisible() bool {
	return m.workflowsSidebar != nil && newChatPaneLayout(m.width, m.sessionsSidebar != nil, m.workflowsSidebar != nil).rightSidebarVisible
}
