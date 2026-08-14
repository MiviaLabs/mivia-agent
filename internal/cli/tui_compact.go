package cli

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

// compactionDoneMsg carries the result of one asynchronous /compact run from
// the tea command goroutine back to the update goroutine.
type compactionDoneMsg struct{ err error }

// handleTuiCompactSlash starts the manual compact off the update goroutine.
// The indicator shows at once: the transcript gets the notice, the status bar
// reads busy, and the work rides a drained tea command. A manual compact may
// reach an LLM summary call bounded by a ten-second timeout, so running it
// inline froze the UI with no visible cause for the whole call.
func (m *tuiModel) handleTuiCompactSlash() bool {
	switch {
	case m.waiting:
		m.appendInfo("(finish the current turn before /compact)")
	case m.compacting:
		m.appendInfo("(already compacting)")
	default:
		m.compacting = true
		m.turnStart = time.Now()
		m.appendInfo("compacting context…")
		m.pendingAsyncCmds = append(m.pendingAsyncCmds, m.compactCmd())
	}
	return true
}

// compactCmd runs the compact and reports through compactionDoneMsg. The
// session serializes compaction against turn publication itself; the command
// only moves the blocking call off the update goroutine.
func (m *tuiModel) compactCmd() tea.Cmd {
	return func() tea.Msg {
		return compactionDoneMsg{err: m.session.Compact(context.Background())}
	}
}

// applyCompactionDone clears the busy state and reports the outcome in the
// transcript with the same words the inline handler used.
func (m *tuiModel) applyCompactionDone(err error) {
	m.compacting = false
	if err != nil {
		m.appendInfo("context compaction failed: " + err.Error())
		m.renderVP()
		return
	}
	usage := m.session.ContextUsage()
	m.appendInfo(fmt.Sprintf("context compacted (%d%% used, %s/%s prompt)", usage.Percent, chat.FormatTokenK(usage.UsedTokens), chat.FormatTokenK(usage.BudgetTokens)))
	m.renderVP()
}
