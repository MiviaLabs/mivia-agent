package cli

// tuiFocus identifies the pane that owns keyboard navigation in chat mode.
type tuiFocus uint8

const (
	focusComposer tuiFocus = iota
	focusScrollback
	focusSidebar
	focusWorkflowsSidebar
)

func (f tuiFocus) String() string {
	switch f {
	case focusScrollback:
		return "scrollback"
	case focusSidebar:
		return "sidebar"
	case focusWorkflowsSidebar:
		return "workflows"
	default:
		return "composer"
	}
}

func nextTUIFocus(current tuiFocus, reverse bool) tuiFocus {
	panes := []tuiFocus{focusComposer, focusScrollback}
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

func isPrintableKey(key string) bool { return len([]rune(key)) == 1 }

// routeFocusKey returns the new focus and whether the key was consumed.
// Printable input from another pane returns focus to the composer but is not consumed.
func routeFocusKey(current tuiFocus, key string) (tuiFocus, bool) {
	switch key {
	case "tab":
		return nextTUIFocus(current, false), true
	case "shift+tab":
		return nextTUIFocus(current, true), true
	case "esc":
		if current != focusComposer {
			return focusComposer, true
		}
	case "up", "down":
		return current, current != focusComposer
	case "pgup", "pgdown":
		return focusScrollback, true
	case "shift+home", "shift+end":
		// Reach the transcript extremes without disturbing the draft: focus
		// stays where it is, the handler scrolls. Bonus route only - VTE and
		// Konsole bind both to their own scrollback and consume them first.
		return current, true
	case "home", "end":
		// Line editing first. These are the composer's only line-start and
		// line-end keys, so they are promoted to the transcript only when it
		// already owns focus; handleChatControlKey resolves the meaning.
		return current, current == focusScrollback
	default:
		if current != focusComposer && isPrintableKey(key) {
			return focusComposer, false
		}
	}
	return current, false
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
