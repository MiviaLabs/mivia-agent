package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
)

// applyFollowScroll sticks to bottom when following; preserves YOffset otherwise.
func (m *TUIModel) applyFollowScroll(wasAtBottom bool, savedOffset int) {
	m.followOutput = cli.ShouldFollowOutput(m.followOutput, wasAtBottom, false)
	if m.followOutput {
		m.viewport.GotoBottom()
		return
	}
	m.viewport.YOffset = cli.Min(savedOffset, m.viewport.TotalLineCount()-m.viewport.Height)
	if m.viewport.YOffset < 0 {
		m.viewport.YOffset = 0
	}
}

// jumpToLatest re-enables follow mode and scrolls to the bottom.
func (m *TUIModel) jumpToLatest() {
	m.followOutput = true
	m.viewport.GotoBottom()
}

// noteUserScrolledUp marks the user as reading history (no auto-jump).
func (m *TUIModel) noteUserScrolledUp() {
	m.followOutput = false
}

func (m *TUIModel) clearAwaitingFirstActivity() {
	m.awaitingFirstActivity = false
}
