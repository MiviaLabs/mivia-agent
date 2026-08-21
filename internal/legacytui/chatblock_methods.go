package legacytui

import (
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"strings"
)

func (m *TUIModel) appendBlock(block cli.ChatBlock) {
	block.Sequence = uint64(len(m.blocks) + 1)
	block.ID = cli.ChatBlockID(block.TurnID, block.Sequence)
	m.blocks = append(m.blocks, block)
	// Block-based truncation: keep at most maxBlocks whole blocks.
	// Dropping whole blocks preserves block identity, kind order,
	// and hit ranges - unlike old line-based truncation which
	// sliced blocks by a line count that bore no relation to blocks.
	const maxBlocks = 1000
	if len(m.blocks) > maxBlocks {
		dropped := len(m.blocks) - maxBlocks
		m.blocks = m.blocks[dropped:]
		// Trimming used to be silent, so the top of the transcript claimed to
		// be the start of the session. The count is chrome (rendered as a
		// note above the first block), not a block - keeping it out of
		// m.blocks preserves block/message accounting exactly.
		m.trimmedBlocks += dropped
		// Re-sequenced dropped blocks start at 1.
		for i := range m.blocks {
			m.blocks[i].Sequence = uint64(i + 1)
			m.blocks[i].ID = cli.ChatBlockID(m.blocks[i].TurnID, m.blocks[i].Sequence)
		}
		if m.msgOffset > 0 && m.session != nil {
			m.msgOffset = cli.Min(m.session.MessagesCount(), m.msgOffset+dropped)
		}
	}
	// Rebuild messages from blocks (single source of truth).
	rendered := m.renderBlocksForView()
	lines := applySelectionChrome(rendered.Lines, rendered.Ranges, m.selectedBlockID, m.focus == cli.FocusScrollback)
	m.messages = lines
	m.chatBlockRanges = rendered.Ranges
	m.clearStaleSelection()
}

func (m *TUIModel) buildViewportContent() string {
	if len(m.blocks) == 0 && len(m.messages) > 0 {
		for _, line := range m.messages {
			m.appendBlock(cli.ChatBlock{Kind: cli.ChatBlockSystem, Text: line, Rendered: line})
		}
	}
	if len(m.blocks) == 0 {
		return ""
	}
	rendered := m.renderBlocksForView()
	m.chatBlockRanges = rendered.Ranges
	m.clearStaleSelection()
	lines := applySelectionChrome(rendered.Lines, rendered.Ranges, m.selectedBlockID, m.focus == cli.FocusScrollback)
	m.messages = lines
	if m.trimmedBlocks > 0 {
		// Chrome, not history: says what the transcript is no longer showing.
		note := fmt.Sprintf("  ─ %d older messages trimmed ─", m.trimmedBlocks)
		return strings.Join(append([]string{TUIDimStyle.Render(note)}, lines...), "\n")
	}
	return strings.Join(lines, "\n")
}

// renderBlocksForView applies work-group collapse (view-layer only).
// Always uses work-group rendering so multi-tool sets get a Work header and
// can be toggled - never silently fall back to a flat list when the collapse
// map is nil (that made Enter/toggle appear broken).
// History rails never animate (Live=false).
func (m *TUIModel) renderBlocksForView() cli.ChatBlockRender {
	w := cli.Max(20, m.chatPaneWidth()-2)
	view := cli.RailView{Frame: m.logoFrame, Live: false}
	if m.workGroupCollapsed == nil {
		m.workGroupCollapsed = map[string]bool{}
	}
	if m.workGroupScroll == nil {
		m.workGroupScroll = map[string]int{}
	}
	return cli.RenderChatBlocksWithWorkGroupsWindow(m.blocks, m.modelName, w, m.thinkingExpandDefault,
		m.workGroupCollapsed, m.workGroupScroll, view)
}
