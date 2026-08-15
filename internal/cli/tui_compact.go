package cli

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

// compactionDoneMsg carries the result of one asynchronous /compact run from
// the tea command goroutine back to the update goroutine. notice is the
// rendered typed compaction record (before/after tokens, elision counts), or
// empty when the run emitted none.
type compactionDoneMsg struct {
	err    error
	notice string
}

// handleTuiCompactSlash starts the manual compact off the update goroutine.
// The indicator shows at once: the transcript gets the notice, the status bar
// reads busy, and the work rides a drained tea command. A manual compact may
// reach an LLM summary call bounded by a ten-second timeout, so running it
// inline froze the UI with no visible cause for the whole call.
func (m *tuiModel) handleTuiCompactSlash(focus string) bool {
	switch {
	case m.waiting:
		m.appendInfo("(finish the current turn before /compact)")
	case m.compacting:
		m.appendInfo("(already compacting)")
	default:
		m.compacting = true
		m.turnStart = time.Now()
		m.appendInfo("compacting context…")
		m.pendingAsyncCmds = append(m.pendingAsyncCmds, m.compactCmd(focus))
	}
	return true
}

// compactCmd runs the compact and reports through compactionDoneMsg. The
// session serializes compaction against turn publication itself; the command
// only moves the blocking call off the update goroutine.
//
// The agent-event sink is attached for the duration of the run, the same way
// the --json leg does it (handleSlashCompactJSON): the bridge callback is
// installed per turn inside SendUser*, so a compact started from a slash
// command runs with no sink and the session's typed compaction record - the
// only carrier of the before/after token delta and the elision counts -
// reached nobody. The swap is mutex-guarded because the compaction reads the
// field from this goroutine.
func (m *tuiModel) compactCmd(focus string) tea.Cmd {
	return func() tea.Msg {
		var notice string
		previous := m.session.SwapOnAgentEvent(func(ev agent.Event) {
			if ev.Kind == agent.EventCompaction && ev.Compaction != nil {
				notice = renderCompactionNotice(*ev.Compaction)
			}
		})
		err := m.session.Compact(context.Background(), focus)
		m.session.SwapOnAgentEvent(previous)
		return compactionDoneMsg{err: err, notice: notice}
	}
}

// applyCompactionDone clears the busy state and reports the outcome in the
// transcript with the same words the inline handler used. A typed notice, when
// the run produced one, precedes the usage line: it carries the detail the
// percentage cannot (how much was dropped, and how much of that was elided
// tool output).
func (m *tuiModel) applyCompactionDone(msg compactionDoneMsg) {
	m.compacting = false
	if msg.err != nil {
		m.appendInfo("context compaction failed: " + msg.err.Error())
		m.renderVP()
		return
	}
	// These are independent: a structural-only compaction still emits a typed
	// record with its before/after tokens, so the notice being present says
	// nothing about whether a summary was produced. The gate reason is what
	// distinguishes "compacted and summarized" from "compacted, and the
	// summary was never attempted". The usage line stays last so the
	// transcript ends on the result.
	if msg.notice != "" {
		m.appendInfo(msg.notice)
	}
	if reason := summaryDisabledReason(m.session, m.config); reason != "" {
		m.appendInfo(compactStructuralOnlyNotice(reason))
	}
	usage := m.session.ContextUsage()
	m.appendInfo(fmt.Sprintf("context compacted (%d%% used, %s/%s prompt)", usage.Percent, chat.FormatTokenK(usage.UsedTokens), chat.FormatTokenK(usage.BudgetTokens)))
	m.renderVP()
}
