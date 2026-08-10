package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *tuiModel) layout() {
	const statusH, hintH = 1, 1
	const borderChrome = 2 // top + bottom border
	inputHeight := min(composerMaxHeight(m.height), max(1, m.textarea.LineCount()))
	composerH := inputHeight + borderChrome + composerPadRows
	// The live "now" panel is a paint-only overlay over the transcript top
	// (renderChatView). It holds no layout band, so the viewport spans the
	// full available height whether the panel is visible or not. Both layout
	// paths subtract the same terms or the viewport is sized differently in
	// each and the frame clips.
	avail := m.height - statusH - composerH - hintH
	if avail < 5 {
		avail = 5
	}

	vpHeight := max(3, avail)

	chatWidth := m.chatPaneWidth()
	if !m.ready {
		// Must go through the constructor: a bare viewport.New here would
		// silently reinstate the default keymap's ctrl+u/ctrl+d scroll
		// aliases that newTranscriptViewport strips.
		m.viewport = newTranscriptViewport(max(1, chatWidth), vpHeight)
		m.textarea.SetWidth(composerInnerWidth(chatWidth))
		m.ready = true
	} else {
		m.viewport.Width = max(1, chatWidth)
		m.viewport.Height = vpHeight
		m.textarea.SetWidth(composerInnerWidth(chatWidth))
	}
}

// updateFromDrain consumes bridge drain data into model state.
// Chat timeline order within a drain: interim speech → tools → stream → thinking.
func (m *tuiModel) updateFromDrain(d bridgeDrain) {
	// Heartbeat stepDetail is fallback when no open-tool verb status is set.
	if d.StepDetail != "" {
		m.stepDetail = d.StepDetail
		if !d.StepDetailAt.IsZero() {
			m.stepDetailAt = d.StepDetailAt
		} else {
			m.stepDetailAt = time.Now()
		}
		// Tool-batch heartbeats ("tools 0/2 done · 12s") are real progress -
		// do not leave the hint line stuck on "⚠ stalled".
		m.stalledWarning = false
		// Prefer live counts from toolRows (authoritative) over raw heartbeat text.
		if len(m.toolRows) > 0 {
			m.refreshLiveToolWaveStatus()
		}
	}
	// Content-then-tools: clear optimistic final stream; speech becomes interim bubble.
	if d.ResetStream {
		m.streamBuf.Reset()
	}
	// Intermediate assistant bubbles - gate noise/ghosts (Phase B).
	committedInterim := false
	if interim := strings.TrimSpace(d.Interim); shouldCommitInterim(interim) {
		m.appendBlock(ChatBlock{Kind: ChatBlockAssistant, Text: interim})
		committedInterim = true
		m.clearAwaitingFirstActivity()
	}
	if d.Thinking != "" {
		m.thinkingBuf.WriteString(d.Thinking)
	}
	if len(d.Tools) > 0 {
		// Status only when this drain has no real interim speech (Phase A).
		m.applyToolEventsOpts(d.Tools, !committedInterim)
	}
	if d.Stream != "" {
		m.streamBuf.WriteString(d.Stream)
		m.clearAwaitingFirstActivity()
	}
	if m.waiting && !d.Done {
		if len(d.Tools) > 0 || committedInterim {
			m.layout()
		}
		if d.Stream != "" || d.Thinking != "" || len(d.Tools) > 0 || d.ResetStream || committedInterim {
			m.renderStreamVP()
		}
	}
	// Stalled: truly quiet after activity (no stream/tools/thinking/heartbeat).
	// Open tools with a recent stepDetail heartbeat are still working - only
	// mark stalled when both the turn and last step are old (was: any turn >5s
	// with open tools falsely showed "⚠ stalled" while tools ran).
	if m.waiting && d.Stream == "" && len(d.Tools) == 0 && d.Thinking == "" && d.StepDetail == "" && !committedInterim && !d.Done {
		hasData := m.streamBuf.Len() > 0 || m.thinkingBuf.Len() > 0 || len(m.toolRows) > 0
		if !hasData {
			return
		}
		last := m.stepDetailAt
		if last.IsZero() {
			last = m.turnStart
		}
		const stallQuiet = 15 * time.Second
		if time.Since(last) > stallQuiet {
			m.stalledWarning = true
		}
	}
}

func (m *tuiModel) finishStream(err error) []tea.Cmd {
	// Idempotent: a second finish (stale TurnEnd after bridge done, or dual path)
	// must not re-append assistant/done blocks.
	if !m.waiting {
		return nil
	}
	m.waiting = false
	m.awaitingFirstActivity = false

	// Chat timeline order: thinking → tools → assistant answer → done.
	m.flushThinkingToHistory()
	if err == context.Canceled {
		m.forceCommitRemainingToolsStatus("cancelled")
	} else {
		m.forceCommitRemainingTools()
	}

	raw := m.streamBuf.String()
	m.streamBuf.Reset()
	if strings.TrimSpace(raw) != "" {
		m.appendBlock(ChatBlock{Kind: ChatBlockAssistant, Text: raw})
	}

	m.appendTurnFooter(err, time.Since(m.turnStart))
	// The turn boundary is where a staged tool load either publishes or
	// defers; say so on the deferral path (plan tools/05 D7).
	m.appendAdmissionNotes()

	m.toolRows = nil
	m.toolWaveTotal = 0
	m.toolWaveDone = 0
	m.toolPanel = toolPanelState{Selected: -1}
	m.thinkingBuf.Reset()
	m.liveThinkingScroll = 0
	m.stepDetail = ""
	m.stepDetailAt = time.Time{}
	m.cachedCtxPercent = 0
	m.cachedCtxPercentAt = time.Time{}
	m.stalledWarning = false
	m.layout()
	m.renderVP()
	// Do not textarea.Reset() here: user may have typed a draft while waiting.
	// Keep m.cancel alive so runTUI cleanup can still call it (harmless no-op
	// after context is already cancelled). This prevents the second Ctrl+C from
	// finding m.cancel=nil and racing away before the agent goroutine finishes.

	// Cancel keeps the queue but does not auto-send the next item (stop = stop).
	if err != context.Canceled && len(m.pendingQueue) > 0 {
		m.sendNextQueued()
		cmds := m.takeQueuedSlashCmds()
		if m.waiting {
			return append(cmds, m.pollCmd())
		}
		return cmds
	}
	return m.takeQueuedSlashCmds()
}

// flushThinkingToHistory commits live thinking as a durable chat block so it
// does not disappear when the live overlay clears.
func (m *tuiModel) flushThinkingToHistory() {
	text := strings.TrimSpace(m.thinkingBuf.String())
	if text == "" {
		return
	}
	m.appendBlock(ChatBlock{
		Kind:         ChatBlockThinking,
		Text:         text,
		Collapsed:    !m.thinkingExpandDefault,
		ScrollOffset: m.liveThinkingScroll,
	})
	m.thinkingBuf.Reset()
	m.liveThinkingScroll = 0
}

func (m *tuiModel) appendTurnFooter(err error, total time.Duration) {
	if err != nil && err != context.Canceled {
		text := "error: " + SafeChatBlockText(err.Error(), 240)
		m.appendBlock(ChatBlock{
			Kind:     ChatBlockDivider,
			Text:     text,
			Rendered: tuiErrorStyle.Render(text),
		})
		return
	}
	if err == context.Canceled {
		text := fmt.Sprintf("(cancelled · %s)", formatDuration(total))
		m.appendBlock(ChatBlock{
			Kind:     ChatBlockDivider,
			Text:     text,
			Rendered: tuiDimStyle.Render(text),
		})
		return
	}
	// Turn footer carries what the turn cost: duration plus the typed action
	// tally, so scrolling history reads as a record instead of a rule.
	text := fmt.Sprintf("  ─ turn %d · %s", m.session.UserTurns(), formatDuration(total))
	tools, agents, failed := m.turnActionTally()
	if tools > 0 {
		text += fmt.Sprintf(" · %d ⚙", tools)
	}
	if agents > 0 {
		text += fmt.Sprintf(" · %d ◆", agents)
	}
	if failed > 0 {
		text += fmt.Sprintf(" · %d ✗", failed)
	}
	text += " ─"
	m.appendBlock(ChatBlock{
		Kind:     ChatBlockDivider,
		Text:     text,
		Rendered: tuiDimStyle.Render(text),
	})
}

func (m *tuiModel) appendMsg(s string) {
	m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: s, Rendered: s})
}

func (m *tuiModel) appendInfo(s string) {
	m.appendMsg(tuiInfoStyle.Render("  " + s))
}

func (m *tuiModel) renderVP() {
	m.hitMap.invalidate()
	content := m.buildViewportContent()
	m.viewport.SetContent(content)
	// Capture scroll state AFTER SetContent so content-length-dependent
	// AtBottom() / YOffset are correct - not stale from before content rebuild.
	wasAtBottom := m.viewport.AtBottom()
	m.applyFollowScroll(wasAtBottom, m.viewport.YOffset)
	// Check if scrolled to top and there's more history to load.
	if !m.waiting && m.msgOffset > 0 && m.viewport.YOffset <= 0 && m.viewport.TotalLineCount() > m.viewport.Height {
		m.loadMoreMessages()
	}
}

// renderStreamVP is retained as an alias for renderVP.
//
// Live content (thinking, tools, stream tail, planning indicator) used to be
// concatenated into the viewport here, which made the transcript's height
// change on every tick and the scroll anchor chase it - the chat visibly
// jumped. That content now renders in a paint-only live panel overlay
// (livepanel.go); the viewport holds committed history only.
func (m *tuiModel) renderStreamVP() {
	m.renderVP()
}

// loadMoreMessages loads older messages from session history into the viewport.
// It prepends them to m.messages and adjusts the scroll offset so the user's
// current viewport position remains stable (showing the same content).
// Batch size: 50 messages at a time.
// Implementation: tui.go loadMoreMessages.

// turnActionTally counts the actions committed since the last user block -
// the material for the turn footer.
func (m *tuiModel) turnActionTally() (tools, agents, failed int) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		b := m.blocks[i]
		if b.Kind == ChatBlockUser {
			break
		}
		if b.Kind != ChatBlockTool {
			continue
		}
		if actionKindForTool(b.ToolName) == actionAgent {
			agents++
		} else {
			tools++
		}
		if b.Failed {
			failed++
		}
	}
	return tools, agents, failed
}
