package cli

// tuiFocus identifies the pane that owns keyboard navigation in chat mode.
type tuiFocus uint8

const (
	focusComposer tuiFocus = iota
	focusScrollback
	focusTools
)

func (f tuiFocus) String() string {
	switch f {
	case focusScrollback:
		return "scrollback"
	case focusTools:
		return "tools"
	default:
		return "composer"
	}
}

func nextTUIFocus(current tuiFocus, toolsAvailable, reverse bool) tuiFocus {
	panes := []tuiFocus{focusComposer, focusScrollback}
	if toolsAvailable {
		panes = append(panes, focusTools)
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
func routeFocusKey(current tuiFocus, key string, toolsAvailable bool) (tuiFocus, bool) {
	switch key {
	case "tab":
		return nextTUIFocus(current, toolsAvailable, false), true
	case "shift+tab":
		return nextTUIFocus(current, toolsAvailable, true), true
	case "esc":
		if current != focusComposer {
			return focusComposer, true
		}
	case "up", "down":
		return current, current != focusComposer
	case "pgup", "pgdown", "home", "end":
		return focusScrollback, true
	default:
		if current != focusComposer && isPrintableKey(key) {
			return focusComposer, false
		}
	}
	return current, false
}

func (m *tuiModel) setFocus(focus tuiFocus) {
	m.focus = focus
	if focus == focusComposer {
		m.textarea.Focus()
	} else {
		m.textarea.Blur()
	}
	m.toolPanel.Focused = focus == focusTools
}
