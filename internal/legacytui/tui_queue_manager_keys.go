package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// openQueueManager opens the queue manager popup. It refuses when the session
// is not in chat mode, the queue is empty, an edit is already in progress, or
// another modal owns the screen. It closes the composer popups first so the
// suggest/history surfaces never compete with the manager.
func (m *TUIModel) openQueueManager() bool {
	if m.mode != modeChat || m.queueCount() == 0 || m.editingQueued || m.queueMgr.open || m.modalOpen() {
		return false
	}
	m.closeSuggest()
	m.closeHistory()
	m.queueMgr = QueueMgrState{open: true, selected: 0}
	return true
}

func (m *TUIModel) closeQueueManager() {
	m.queueMgr = QueueMgrState{}
}

// handleQueueEditEnter ends the queue-manager edit flow on Enter: the edited
// text re-queues at the tail while the agent is busy, or sends now when idle;
// an emptied draft aborts the edit and restores the original item. The edit
// state is cleared atomically in this Update so the item can never be
// double-sent (the item left the queue when the edit began).
func (m *TUIModel) handleQueueEditEnter() (bool, bool, []tea.Cmd) {
	text := strings.TrimSpace(m.textarea.Value())
	if text == "" {
		m.restoreQueueEdit()
		return true, true, nil
	}
	if m.waiting {
		m.queueAppend(QueuedItem{sent: text, display: text})
		m.editingQueued = false
		m.editMemory = QueuedItem{}
		m.textarea.Reset()
		return true, true, nil
	}
	m.editingQueued = false
	m.editMemory = QueuedItem{}
	m.startAI(text)
	return true, false, []tea.Cmd{m.pollCmd()}
}

// handleQueueManagerKey owns every key while the queue manager is open. The
// manager is a modal: keys it does not act on are consumed anyway, so the
// composer cannot edit a draft behind the popup (INV-TUI-16). When closed it
// declines everything so d/e stay typable in the composer.
func (m *TUIModel) handleQueueManagerKey(key string) (bool, bool, []tea.Cmd) {
	if !m.queueMgr.open {
		return false, false, nil
	}
	switch key {
	case "up", "ctrl+p":
		return m.queueManagerNav(-1)
	case "down", "ctrl+n":
		return m.queueManagerNav(1)
	case "enter":
		return m.queueManagerSteer()
	case "d":
		return m.queueManagerDelete()
	case "e":
		return m.queueManagerEdit()
	case "esc", "q":
		m.closeQueueManager()
	}
	return true, true, nil
}

func (m *TUIModel) queueManagerNav(delta int) (bool, bool, []tea.Cmd) {
	n := m.queueCount()
	if n > 0 {
		m.queueMgr.selected = cli.Min(cli.Max(0, m.queueMgr.selected+delta), n-1)
	}
	return true, true, nil
}

// queueManagerSteer sends the selected queued item right away: it is removed
// from the queue and dispatched through the canonical sendQueuedItem path,
// which cancels/joins the in-flight turn exactly like today's empty-enter
// force-send. The remaining queue keeps auto-draining at the next turn end.
func (m *TUIModel) queueManagerSteer() (bool, bool, []tea.Cmd) {
	if m.queueMgr.selected < 0 || m.queueMgr.selected >= m.queueCount() {
		return true, true, nil
	}
	item := m.queueRemoveAt(m.queueMgr.selected)
	m.closeQueueManager()
	if !m.sendQueuedItem(item) {
		// The item was a slash command handled locally; its terminal command
		// (if any) was staged and must be drained.
		return true, true, m.takeQueuedSlashCmds()
	}
	return true, true, append(m.takeQueuedSlashCmds(), m.pollCmd())
}

func (m *TUIModel) queueManagerDelete() (bool, bool, []tea.Cmd) {
	if m.queueMgr.selected >= 0 && m.queueMgr.selected < m.queueCount() {
		m.queueRemoveAt(m.queueMgr.selected)
	}
	m.clampQueueManager()
	return true, true, nil
}

// queueManagerEdit starts the edit flow for the selected item: the item
// leaves the queue and its text loads into the real composer, with the
// previous draft and cursor snapshotted for esc/cancel restore. Skill entries
// are refused before any mutation (their body is hidden by design).
func (m *TUIModel) queueManagerEdit() (bool, bool, []tea.Cmd) {
	if m.queueMgr.selected < 0 || m.queueMgr.selected >= m.queueCount() {
		return true, true, nil
	}
	item := m.queueItemAt(m.queueMgr.selected)
	if item.skill != nil {
		return true, true, nil
	}
	m.queueRemoveAt(m.queueMgr.selected)
	m.closeQueueManager()
	li := m.textarea.LineInfo()
	item.savedDraft = m.textarea.Value()
	item.savedRow = m.textarea.Line()
	item.savedCursor = li.StartColumn + li.ColumnOffset
	m.editMemory = item
	m.textarea.SetValue(item.sent)
	m.textarea.CursorEnd()
	m.editingQueued = true
	return true, true, nil
}
