package cli

// The pending-message queue: what happens to text typed while the agent is
// working. Queued input is held, then sent when the turn ends (or force-sent
// on an empty Enter). Slash commands run locally as they come off the queue.
//
// The queue is three index-aligned slices on tuiModel
// (pendingQueue / pendingQueueLabels / pendingSkillTurns). Every mutation
// goes through queueRemoveAt/queueInsertAt/queueAppend (tui_queue_manager.go)
// so the alignment cannot drift, and sendQueuedItem owns the canonical
// dispatch shared by the turn-end drain and the queue manager's steer.

import "strings"

// sendQueuedItem dispatches one queued item. It returns true when a turn was
// started (the caller stops draining) and false when the item was a slash
// command handled locally and the caller should continue with the next item.
func (m *tuiModel) sendQueuedItem(item queuedItem) bool {
	if strings.HasPrefix(item.sent, "/") && m.handleSlash(item.sent) {
		m.renderVP()
		m.textarea.Reset()
		// A queued slash command can still need a terminal command
		// (/select). Dropping it left the mode and the terminal
		// disagreeing until some later command drained the stale value.
		m.queuedSlashCmds = append(m.queuedSlashCmds, m.takePendingSlashCmds()...)
		return false
	}
	if item.skill != nil {
		m.startSkillAI(*item.skill)
	} else {
		m.startAIWithDisplay(item.sent, item.display)
	}
	return true
}

// sendNextQueued pops and sends queued messages, handling /commands locally.
func (m *tuiModel) sendNextQueued() {
	for m.queueCount() > 0 {
		item := m.queueRemoveAt(0)
		if m.sendQueuedItem(item) {
			return
		}
	}
}

func (m *tuiModel) queueTurn(sent, display string) {
	m.pendingQueue = append(m.pendingQueue, sent)
	m.pendingQueueLabels = append(m.pendingQueueLabels, display)
	m.pendingSkillTurns = append(m.pendingSkillTurns, nil)
}

func (m *tuiModel) queueSkillTurn(spec skillSlashSpec) {
	m.pendingQueue = append(m.pendingQueue, "")
	m.pendingQueueLabels = append(m.pendingQueueLabels, spec.display)
	m.pendingSkillTurns = append(m.pendingSkillTurns, &spec)
}
