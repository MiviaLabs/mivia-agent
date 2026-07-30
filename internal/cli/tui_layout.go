package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

func (m *tuiModel) layout() {
	const statusH, hintH = 1, 1
	const borderChrome = 2 // top + bottom border
	inputHeight := min(composerMaxHeight(m.height), max(3, m.textarea.LineCount()+1))
	composerH := inputHeight + borderChrome
	avail := m.height - statusH - composerH - hintH
	if avail < 5 {
		avail = 5
	}

	vpHeight := max(3, avail)

	if !m.ready {
		m.viewport = viewport.New(max(1, m.width), vpHeight)
		m.textarea.SetWidth(composerInnerWidth(m.width))
		m.ready = true
	} else {
		m.viewport.Width = max(1, m.width)
		m.viewport.Height = vpHeight
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
		// Tool-batch heartbeats ("tools 0/2 done · 12s") are real progress —
		// do not leave the composer footer stuck on "⚠ stalled".
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
	// Intermediate assistant bubbles — gate noise/ghosts (Phase B).
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
	// Open tools with a recent stepDetail heartbeat are still working — only
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

	m.toolRows = nil
	m.toolWaveTotal = 0
	m.toolWaveDone = 0
	m.toolPanel = toolPanelState{Selected: -1}
	m.thinkingBuf.Reset()
	m.liveThinkingScroll = 0
	m.stepDetail = ""
	m.stepDetailAt = time.Time{}
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
		if m.waiting {
			return []tea.Cmd{m.pollCmd()}
		}
	}
	return nil
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
	text := fmt.Sprintf("  ─ done · %s ─", formatDuration(total))
	m.appendBlock(ChatBlock{
		Kind:     ChatBlockDivider,
		Text:     text,
		Rendered: tuiDimStyle.Render(text),
	})
}

func (m *tuiModel) appendMsg(s string) {
	m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: s, Rendered: s})
	if len(m.messages) == 0 {
		return
	}
	return
}

func (m *tuiModel) appendInfo(s string) {
	m.appendMsg(tuiInfoStyle.Render("  " + s))
}

func (m *tuiModel) renderVP() {
	m.hitMap.invalidate()
	content := m.buildViewportContent()
	m.viewport.SetContent(content)
	// Capture scroll state AFTER SetContent so content-length-dependent
	// AtBottom() / YOffset are correct — not stale from before content rebuild.
	wasAtBottom := m.viewport.AtBottom()
	m.applyFollowScroll(wasAtBottom, m.viewport.YOffset)
	// Check if scrolled to top and there's more history to load.
	if !m.waiting && m.msgOffset > 0 && m.viewport.YOffset <= 0 && m.viewport.TotalLineCount() > m.viewport.Height {
		m.loadMoreMessages()
	}
}

func (m *tuiModel) renderStreamVP() {
	m.hitMap.invalidate()
	content := m.buildViewportContent()
	// Live chrome order matches chat timeline:
	// history blocks → thinking → open tools → streaming answer.
	if m.thinkingBuf.Len() > 0 {
		thinkingStr := renderThinkingBlockView(
			"thinking-live",
			m.thinkingBuf.String(),
			false,
			m.liveThinkingScroll,
			m.modelName,
			m.width,
			true,
			m.logoFrame,
			true, // live overlay: cyan pulse
		)
		if thinkingStr != "" {
			if content != "" {
				content += "\n"
			}
			content += thinkingStr
		}
	}
	if len(m.toolRows) > 0 {
		_, doneTools, _ := countTools(m.toolRows)
		openTools := len(m.toolRows) - doneTools
		toolContent, _, _ := renderToolPanelWindow(
			m.toolRows, m.width, time.Now(), m.toolPanel,
			m.logoFrame,
			deriveBrandPhase(m.waiting, openTools, m.streamBuf.Len(), len(m.pendingQueue), false, time.Since(m.turnStart)),
			toolMaxVisibleRows,
			visualLineCount(m.messages),
			time.Since(m.turnStart),
		)
		if toolContent != "" {
			if content != "" {
				content += "\n"
			}
			content += toolContent
		}
	}
	if m.streamBuf.Len() > 0 {
		if content != "" {
			content += "\n"
		}
		content += tuiDimStyle.Render("▌ ") + m.streamBuf.String()
	}
	// Phase C: awaiting first activity — dim planning affordance (not blank).
	if m.waiting && m.awaitingFirstActivity &&
		m.streamBuf.Len() == 0 && m.thinkingBuf.Len() == 0 && len(m.toolRows) == 0 {
		elapsed := time.Since(m.turnStart)
		if elapsed > 300*time.Millisecond {
			glyph := brandGlyph(m.logoFrame, brandColorThinking)
			indicator := fmt.Sprintf("  %s … planning · %s", glyph, formatDuration(elapsed))
			if content != "" {
				content += "\n"
			}
			content += tuiDimStyle.Render(indicator)
		}
	} else if m.waiting && m.streamBuf.Len() == 0 && m.thinkingBuf.Len() == 0 && len(m.toolRows) == 0 {
		// Quiet mid-turn (no tools/stream yet after activity cleared).
		elapsed := time.Since(m.turnStart)
		if elapsed > 2*time.Second {
			glyph := brandGlyph(m.logoFrame, brandColorThinking)
			indicator := fmt.Sprintf("  %s thinking · %s", glyph, formatDuration(elapsed))
			if content != "" {
				content += "\n"
			}
			content += tuiDimStyle.Render(indicator)
		}
	}
	m.viewport.SetContent(content)
	// Capture scroll state AFTER SetContent so content-length-dependent
	// AtBottom() / YOffset are correct — not stale from before content rebuild.
	wasAtBottom := m.viewport.AtBottom()
	m.applyFollowScroll(wasAtBottom, m.viewport.YOffset)
}

// loadMoreMessages loads older messages from session history into the viewport.
// It prepends them to m.messages and adjusts the scroll offset so the user's
// current viewport position remains stable (showing the same content).
// Batch size: 50 messages at a time.
// Implementation: tui.go loadMoreMessages.
