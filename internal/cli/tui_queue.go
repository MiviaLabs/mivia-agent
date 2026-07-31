package cli

// The pending-message queue: what happens to text typed while the agent is
// working. Queued input is held, then sent when the turn ends (or force-sent
// on an empty Enter). Slash commands run locally as they come off the queue.

import "strings"

func (m *tuiModel) forceSendQueued() {
	if len(m.pendingQueue) == 0 {
		return
	}
	// Cancel current turn.
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	m.sendNextQueued()
}

// sendNextQueued pops and sends the next queued message, handling /commands locally.
func (m *tuiModel) sendNextQueued() {
	for len(m.pendingQueue) > 0 {
		next := m.pendingQueue[0]
		m.pendingQueue = m.pendingQueue[1:]
		var skill *skillSlashSpec
		if len(m.pendingSkillTurns) > 0 {
			skill = m.pendingSkillTurns[0]
			m.pendingSkillTurns = m.pendingSkillTurns[1:]
		}
		display := next
		if len(m.pendingQueueLabels) > 0 {
			display = m.pendingQueueLabels[0]
			m.pendingQueueLabels = m.pendingQueueLabels[1:]
		}
		if strings.HasPrefix(next, "/") && m.handleSlash(next) {
			m.renderVP()
			m.textarea.Reset()
			// A queued slash command can still need a terminal command
			// (/select). Dropping it left the mode and the terminal
			// disagreeing until some later command drained the stale value.
			m.queuedSlashCmds = append(m.queuedSlashCmds, m.takePendingSlashCmds()...)
			continue
		}
		if skill != nil {
			m.startSkillAI(*skill)
		} else {
			m.startAIWithDisplay(next, display)
		}
		return
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
