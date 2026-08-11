package cli

import tea "github.com/charmbracelet/bubbletea"

// historyState is the open/selection state of the composer message-history picker.
type historyState struct {
	open     bool
	selected int
}

func (m *tuiModel) appendSentHistory(s string) {
	if s == "" {
		return
	}
	if len(m.sentHistory) > 0 && m.sentHistory[len(m.sentHistory)-1] == s {
		return
	}
	m.sentHistory = append(m.sentHistory, s)
	if len(m.sentHistory) > MaxHistorySize {
		excess := len(m.sentHistory) - MaxHistorySize
		m.sentHistory = m.sentHistory[excess:]
	}
}

func (m *tuiModel) historyEntries() []string {
	entries := make([]string, 0, len(m.sentHistory))
	for i := len(m.sentHistory) - 1; i >= 0; i-- {
		entries = append(entries, m.sentHistory[i])
	}
	return entries
}

func (m *tuiModel) openHistory() {
	m.history.open = true
	m.history.selected = 0
}

func (m *tuiModel) closeHistory() {
	m.history = historyState{}
}

// handleComposerPopupKey routes composer keys through the slash-suggestion
// popup first, then the sent-message history picker. Suggest wins while it is
// open; the history trigger requires suggest closed, so the two never compete.
func (m *tuiModel) handleComposerPopupKey(key string) (bool, bool, []tea.Cmd) {
	if handled, skipViewport, cmds := m.handleSuggestKey(key); handled {
		return true, skipViewport, cmds
	}
	return m.handleHistoryKey(key)
}

func (m *tuiModel) handleHistoryKey(key string) (bool, bool, []tea.Cmd) {
	if m.history.open {
		switch key {
		case "up", "ctrl+p":
			entries := m.historyEntries()
			if len(entries) > 0 && m.history.selected < len(entries)-1 {
				m.history.selected++
			}
			return true, true, nil
		case "down", "ctrl+n":
			if m.history.selected == 0 {
				m.closeHistory()
			} else {
				m.history.selected--
			}
			return true, true, nil
		case "enter", "tab":
			entries := m.historyEntries()
			if m.history.selected < len(entries) {
				m.textarea.SetValue(entries[m.history.selected])
				m.textarea.CursorEnd()
			}
			m.closeHistory()
			return true, true, nil
		case "esc", "shift+tab":
			m.closeHistory()
			return true, true, nil
		default:
			// Typing or any other key dismisses the picker and passes through.
			m.closeHistory()
			return false, false, nil
		}
	}
	if key != "up" && key != "ctrl+p" {
		return false, false, nil
	}
	if m.focus != focusComposer || m.mode != modeChat || m.suggest.open || m.modalOpen() {
		return false, false, nil
	}
	if len(m.historyEntries()) == 0 {
		return false, false, nil
	}
	li := m.textarea.LineInfo()
	if m.textarea.Line() != 0 || li.ColumnOffset != 0 || li.RowOffset != 0 {
		return false, false, nil
	}
	m.openHistory()
	return false, false, nil
}
