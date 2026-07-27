package cli

// applyFollowScroll sticks to bottom when following; preserves YOffset otherwise.
func (m *tuiModel) applyFollowScroll(wasAtBottom bool, savedOffset int) {
	m.followOutput = shouldFollowOutput(m.followOutput, wasAtBottom, false)
	if m.followOutput {
		m.viewport.GotoBottom()
		return
	}
	m.viewport.YOffset = min(savedOffset, m.viewport.TotalLineCount()-m.viewport.Height)
	if m.viewport.YOffset < 0 {
		m.viewport.YOffset = 0
	}
}

// jumpToLatest re-enables follow mode and scrolls to the bottom.
func (m *tuiModel) jumpToLatest() {
	m.followOutput = true
	m.viewport.GotoBottom()
}

// noteUserScrolledUp marks the user as reading history (no auto-jump).
func (m *tuiModel) noteUserScrolledUp() {
	m.followOutput = false
}

func (m *tuiModel) clearAwaitingFirstActivity() {
	m.awaitingFirstActivity = false
}
