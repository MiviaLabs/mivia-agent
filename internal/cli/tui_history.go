package cli

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func (m *tuiModel) loadMoreMessages() {
	m.hitMap.invalidate()
	// Allow while waiting - user can still browse older history mid-turn.
	if m.msgOffset <= 0 {
		return
	}
	const batchSize = 50
	newOffset := m.msgOffset - batchSize
	if newOffset < 0 {
		newOffset = 0
	}
	msgs := m.session.MessagesCopy()
	var newBlocks []ChatBlock
	maxIdx := len(msgs) - 1
	for i := m.msgOffset - 1; i >= newOffset && i <= maxIdx; i-- {
		msg := msgs[i]
		hydrated := HydrateChatBlocksForView([]provider.Message{msg})
		if len(hydrated) == 0 {
			continue
		}
		newBlocks = append(hydrated, newBlocks...)
	}
	if len(newBlocks) == 0 {
		m.msgOffset = 0 // nothing left to load
		return
	}
	// Visual lines (not slot count): multi-line content shifts YOffset by more than 1.
	addedVisual := visualLineCount(RenderChatBlocksWithWorkGroups(newBlocks, m.modelName, max(20, m.width-2), m.thinkingExpandDefault, m.workGroupCollapsed).Lines)
	oldYOffset := m.viewport.YOffset
	// Prepend to messages.
	m.blocks = append(newBlocks, m.blocks...)
	m.messages = m.renderBlocksForView().Lines
	m.msgOffset = newOffset
	// Always preserve visual position on prepend. Do NOT use AtBottom()/GotoBottom:
	// when content fits the viewport, AtBottom∧AtTop are both true and GotoBottom
	// would jump the user away from the top (history load looks broken).
	content := m.buildViewportContent()
	m.viewport.SetContent(content)
	maxOff := m.viewport.TotalLineCount() - m.viewport.Height
	if maxOff < 0 {
		maxOff = 0
	}
	newOff := addedVisual + oldYOffset
	if newOff > maxOff {
		newOff = maxOff
	}
	m.viewport.YOffset = newOff
	// Remove the "showing last N" notice once everything is loaded. The notice
	// block opens the transcript, so after the prepend it sits behind the freshly
	// loaded batch; scan for it instead of assuming position 0.
	if m.msgOffset <= 0 {
		for i := range m.blocks {
			if m.blocks[i].Kind == ChatBlockSystem && strings.Contains(m.blocks[i].Text, "showing last") {
				noticeVisual := visualLineCount(RenderChatBlocksWithWorkGroups([]ChatBlock{m.blocks[i]}, m.modelName, max(20, m.width-2), m.thinkingExpandDefault, m.workGroupCollapsed).Lines)
				m.blocks = append(m.blocks[:i], m.blocks[i+1:]...)
				m.messages = m.renderBlocksForView().Lines
				m.viewport.SetContent(m.buildViewportContent())
				m.viewport.YOffset = max(0, m.viewport.YOffset-noticeVisual)
				break
			}
		}
	}
}

// visualLineCount returns how many viewport lines the given content slots occupy.
// Each string may itself contain newlines after markdown/wrap.
func visualLineCount(lines []string) int {
	n := 0
	for _, line := range lines {
		n += strings.Count(line, "\n") + 1
	}
	return n
}
