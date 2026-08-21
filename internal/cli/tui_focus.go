package cli

// TuiFocus identifies the pane that owns keyboard navigation in chat mode.
type TuiFocus uint8

const (
	FocusComposer TuiFocus = iota
	FocusScrollback
	FocusSidebar
	FocusWorkflowsSidebar
)

func (f TuiFocus) String() string {
	switch f {
	case FocusScrollback:
		return "scrollback"
	case FocusSidebar:
		return "sidebar"
	case FocusWorkflowsSidebar:
		return "workflows"
	default:
		return "composer"
	}
}

func nextTUIFocus(current TuiFocus, reverse bool) TuiFocus {
	panes := []TuiFocus{FocusComposer, FocusScrollback}
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
	return FocusComposer
}

func isPrintableKey(key string) bool { return len([]rune(key)) == 1 }

// RouteFocusKey returns the new focus and whether the key was consumed.
// Printable input from another pane returns focus to the composer but is not consumed.
func RouteFocusKey(current TuiFocus, key string) (TuiFocus, bool) {
	switch key {
	case "tab":
		return nextTUIFocus(current, false), true
	case "shift+tab":
		return nextTUIFocus(current, true), true
	case "esc":
		if current != FocusComposer {
			return FocusComposer, true
		}
	case "up", "down":
		return current, current != FocusComposer
	case "pgup", "pgdown":
		return FocusScrollback, true
	case "shift+home", "shift+end":
		// Reach the transcript extremes without disturbing the draft: focus
		// stays where it is, the handler scrolls. Bonus route only - VTE and
		// Konsole bind both to their own scrollback and consume them first.
		return current, true
	case "home", "end":
		// Line editing first. These are the composer's only line-start and
		// line-end keys, so they are promoted to the transcript only when it
		// already owns focus; handleChatControlKey resolves the meaning.
		return current, current == FocusScrollback
	default:
		if current != FocusComposer && isPrintableKey(key) {
			return FocusComposer, false
		}
	}
	return current, false
}
