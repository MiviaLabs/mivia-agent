package cli

// The queue manager: a modal popup above the composer that shows the pending
// message queue (pendingQueue / pendingQueueLabels / pendingSkillTurns) in
// send order and lets the user steer (send now), delete, or edit any queued
// item. It is a modal with popup placement: while open it consumes every key
// and swallows paste/mouse (INV-TUI-29), ctrl+c/ctrl+q close-then-act
// (INV-TUI-26), and the composer stays visible below it.

// queuedItem is one entry of the pending queue: a single-item view over the
// three index-aligned slices. It doubles as the edit memory for the queue
// manager's edit flow, carrying the composer draft/cursor to restore on
// esc/cancel.
type queuedItem struct {
	index       int    // original queue index (esc-restore target)
	sent        string // text actually sent to the model ("" for skill turns)
	display     string // transcript/label text (skill labels differ from sent)
	skill       *skillSlashSpec
	savedDraft  string // composer draft before edit began (restore target)
	savedCursor int    // caret column within the saved draft's original line
	savedRow    int    // caret line of the saved draft (multi-line restore)
}

// queueMgrState is the open/selection state of the queue manager popup.
type queueMgrState struct {
	open     bool
	selected int
}

func (m *tuiModel) queueCount() int {
	return len(m.pendingQueue)
}

// queueItemAt returns the item at index i. Out-of-range and short-slice
// accesses return zero values so callers that construct the queue with only
// pendingQueue set (some tests) never panic.
func (m *tuiModel) queueItemAt(i int) queuedItem {
	if i < 0 || i >= len(m.pendingQueue) {
		return queuedItem{}
	}
	item := queuedItem{index: i, sent: m.pendingQueue[i], display: m.pendingQueue[i]}
	if i < len(m.pendingQueueLabels) {
		item.display = m.pendingQueueLabels[i]
	}
	if i < len(m.pendingSkillTurns) {
		item.skill = m.pendingSkillTurns[i]
	}
	return item
}

// queueRemoveAt removes and returns the item at index i, splicing all three
// slices so their alignment is preserved.
func (m *tuiModel) queueRemoveAt(i int) queuedItem {
	item := m.queueItemAt(i)
	if i >= 0 && i < len(m.pendingQueue) {
		m.pendingQueue = append(m.pendingQueue[:i], m.pendingQueue[i+1:]...)
	}
	if i >= 0 && i < len(m.pendingQueueLabels) {
		m.pendingQueueLabels = append(m.pendingQueueLabels[:i], m.pendingQueueLabels[i+1:]...)
	}
	if i >= 0 && i < len(m.pendingSkillTurns) {
		m.pendingSkillTurns = append(m.pendingSkillTurns[:i], m.pendingSkillTurns[i+1:]...)
	}
	return item
}

// queueInsertAt inserts an item at index i (clamped to the queue length),
// splicing all three slices and extending short ones with zero values so the
// alignment invariant holds.
func (m *tuiModel) queueInsertAt(i int, item queuedItem) {
	if i < 0 {
		i = 0
	}
	if i > len(m.pendingQueue) {
		i = len(m.pendingQueue)
	}
	m.pendingQueue = append(m.pendingQueue, "")
	copy(m.pendingQueue[i+1:], m.pendingQueue[i:])
	m.pendingQueue[i] = item.sent
	for len(m.pendingQueueLabels) < len(m.pendingQueue) {
		m.pendingQueueLabels = append(m.pendingQueueLabels, "")
	}
	for len(m.pendingSkillTurns) < len(m.pendingQueue) {
		m.pendingSkillTurns = append(m.pendingSkillTurns, nil)
	}
	copy(m.pendingQueueLabels[i+1:], m.pendingQueueLabels[i:])
	m.pendingQueueLabels[i] = item.display
	copy(m.pendingSkillTurns[i+1:], m.pendingSkillTurns[i:])
	m.pendingSkillTurns[i] = item.skill
}

// queueAppend appends an item, preserving its skill shape (unlike queueTurn,
// which always appends a nil skill). It is the edit flow's re-queue path.
func (m *tuiModel) queueAppend(item queuedItem) {
	m.pendingQueue = append(m.pendingQueue, item.sent)
	m.pendingQueueLabels = append(m.pendingQueueLabels, item.display)
	m.pendingSkillTurns = append(m.pendingSkillTurns, item.skill)
}

// resetQueueState clears the queue and every piece of queue-manager state. It
// is the single lifecycle reset for /clear, new sessions, and session loads.
func (m *tuiModel) resetQueueState() {
	m.pendingQueue = nil
	m.pendingQueueLabels = nil
	m.pendingSkillTurns = nil
	m.queueMgr = queueMgrState{}
	m.editingQueued = false
	m.editMemory = queuedItem{}
}

// clampQueueManager clamps the manager selection to the queue and auto-closes
// the manager when the queue empties. It runs after every drain/delete, not
// only in the key handler, so an invisible modal can never consume keys
// (INV-TUI-16).
func (m *tuiModel) clampQueueManager() {
	if !m.queueMgr.open {
		return
	}
	if m.queueCount() == 0 {
		m.queueMgr = queueMgrState{}
		return
	}
	if m.queueMgr.selected >= m.queueCount() {
		m.queueMgr.selected = m.queueCount() - 1
	}
	if m.queueMgr.selected < 0 {
		m.queueMgr.selected = 0
	}
}

// restoreQueueEdit restores the item being edited back into the queue at its
// original position (clamped) and puts the composer draft and cursor back the
// way they were before the edit began. It is the esc / cancel / empty-enter
// path of the edit flow. It is a no-op when no edit is in progress, so an
// unrelated esc or ctrl+c can never resurrect an item.
func (m *tuiModel) restoreQueueEdit() {
	if !m.editingQueued {
		return
	}
	item := m.editMemory
	m.queueInsertAt(item.index, item)
	m.textarea.SetValue(item.savedDraft)
	// bubbles SetValue parks the caret on the last line and SetCursor only
	// moves the column within the current line, so restore the original row
	// first (clamped) and then the column.
	for m.textarea.Line() > item.savedRow {
		m.textarea.CursorUp()
	}
	m.textarea.SetCursor(item.savedCursor)
	m.editingQueued = false
	m.editMemory = queuedItem{}
}
