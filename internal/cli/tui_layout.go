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

func (m *tuiModel) applyToolEvents(evts []bridgeToolEvt) {
	openBefore := 0
	for _, r := range m.toolRows {
		if !r.Done {
			openBefore++
		}
	}
	// Indices finished in this batch only (do not re-commit pre-existing Done rows).
	var finished []int
	for _, e := range evts {
		if e.Start {
			// Chat timeline: stick thinking before tool work begins.
			if openBefore == 0 {
				m.flushThinkingToHistory()
				openBefore = 1 // only flush once per batch
			}
			m.applyToolStartEvent(e)
			continue
		}
		if idx := m.applyToolEndEvent(e); idx >= 0 {
			finished = append(finished, idx)
		}
	}
	// Progressive commit: tools that completed in this batch become chat blocks.
	m.commitToolIndicesToHistory(finished)
	if len(m.toolRows) > 0 {
		m.toolPanel.ordered = orderToolIndices(m.toolRows)
		m.toolPanel.Scroll = clampToolScroll(
			m.toolPanel.Scroll, m.toolPanel.Selected, m.toolPanel.ordered, toolMaxVisibleRows,
		)
	} else {
		m.toolPanel = toolPanelState{Selected: -1}
	}
}

func (m *tuiModel) applyToolStartEvent(e bridgeToolEvt) {
	// Same ToolCallID: lifecycle Status only — never clobber args Detail.
	if e.ToolCallID != "" {
		for i := range m.toolRows {
			if m.toolRows[i].Done || m.toolRows[i].ToolCallID != e.ToolCallID {
				continue
			}
			if e.Name != "" {
				m.toolRows[i].Name = e.Name
			}
			if isLifecycleStatus(e.Detail) {
				m.toolRows[i].Status = e.Detail
			} else if e.Detail != "" {
				m.toolRows[i].Detail = e.Detail
			}
			return
		}
	}
	status, detail := "queued", e.Detail
	if isLifecycleStatus(e.Detail) {
		status, detail = e.Detail, ""
	}
	m.toolRows = append(m.toolRows, toolRow{
		ToolCallID: e.ToolCallID, Name: e.Name, Detail: detail, Status: status, Start: e.At,
	})
	if !m.toolPanel.Focused {
		m.toolPanel.Selected = len(m.toolRows) - 1
	}
	m.toolPanel.ordered = orderToolIndices(m.toolRows)
	m.toolPanel.Scroll = clampToolScroll(
		m.toolPanel.Scroll, m.toolPanel.Selected, m.toolPanel.ordered, toolMaxVisibleRows,
	)
}

// applyToolEndEvent marks a matching open tool done. Returns toolRows index or -1.
func (m *tuiModel) applyToolEndEvent(e bridgeToolEvt) int {
	for i := len(m.toolRows) - 1; i >= 0; i-- {
		match := !m.toolRows[i].Done && ((e.ToolCallID != "" && m.toolRows[i].ToolCallID == e.ToolCallID) ||
			(e.ToolCallID == "" && m.toolRows[i].Name == e.Name))
		if !match {
			continue
		}
		m.toolRows[i].Done = true
		m.toolRows[i].End = e.At
		body := e.Detail
		failed := toolResultFailed(body)
		if isLifecycleStatus(body) {
			m.toolRows[i].Status = body
			m.toolRows[i].Failed = body == "failed" || strings.HasPrefix(body, "failed")
		} else {
			m.toolRows[i].Result = body
			m.toolRows[i].Status = "completed"
			m.toolRows[i].Failed = failed
			if failed {
				m.toolRows[i].Status = "failed"
			}
		}
		return i
	}
	return -1
}

func toolResultFailed(body string) bool {
	low := strings.ToLower(body)
	return strings.HasPrefix(low, "error") ||
		strings.Contains(body, "exit=1") ||
		strings.Contains(body, "exit=error") ||
		strings.Contains(body, "exit=timeout") ||
		strings.Contains(body, "exit=canceled") ||
		body == "failed"
}

// updateFromDrain consumes bridge drain data into model state.
// Chat timeline order within a drain: interim speech → tools → stream → thinking.
func (m *tuiModel) updateFromDrain(d bridgeDrain) {
	m.stepDetail = d.StepDetail
	if !d.StepDetailAt.IsZero() {
		m.stepDetailAt = d.StepDetailAt
	}
	// Content-then-tools: clear optimistic final stream; speech becomes interim bubble.
	if d.ResetStream {
		m.streamBuf.Reset()
	}
	// Intermediate assistant bubbles ("I'll search…") — durable chat blocks.
	if interim := strings.TrimSpace(d.Interim); interim != "" {
		m.appendBlock(ChatBlock{Kind: ChatBlockAssistant, Text: interim})
	}
	if d.Thinking != "" {
		m.thinkingBuf.WriteString(d.Thinking)
	}
	if len(d.Tools) > 0 {
		m.applyToolEvents(d.Tools)
	}
	if d.Stream != "" {
		m.streamBuf.WriteString(d.Stream)
	}
	if m.waiting && !d.Done {
		if len(d.Tools) > 0 || d.Interim != "" {
			m.layout()
		}
		if d.Stream != "" || d.Thinking != "" || len(d.Tools) > 0 || d.ResetStream || d.Interim != "" {
			m.renderStreamVP()
		}
	}
	// Stalled: quiet after activity.
	if m.waiting && d.Stream == "" && len(d.Tools) == 0 && d.Thinking == "" && d.Interim == "" && !d.Done {
		hasData := m.streamBuf.Len() > 0 || m.thinkingBuf.Len() > 0 || len(m.toolRows) > 0
		if hasData {
			elapsed := time.Since(m.turnStart)
			if elapsed > 5*time.Second && !m.stalledWarning {
				m.stalledWarning = true
			}
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

	// Chat timeline order: thinking → tools → assistant answer → done.
	m.flushThinkingToHistory()
	m.forceCommitRemainingTools()

	raw := m.streamBuf.String()
	m.streamBuf.Reset()
	if strings.TrimSpace(raw) != "" {
		m.appendBlock(ChatBlock{Kind: ChatBlockAssistant, Text: raw})
	}

	m.appendTurnFooter(err, time.Since(m.turnStart))

	m.toolRows = nil
	m.toolPanel = toolPanelState{Selected: -1}
	m.thinkingBuf.Reset()
	m.liveThinkingScroll = 0
	m.stepDetail = ""
	m.stepDetailAt = time.Time{}
	m.stalledWarning = false
	m.layout()
	m.renderVP()
	// Do not textarea.Reset() here: user may have typed a draft while waiting.
	m.mu.Lock()
	m.cancel = nil
	m.mu.Unlock()

	if len(m.pendingQueue) > 0 {
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

// commitToolIndicesToHistory moves the given toolRows indices into ChatBlockTool
// history (highest index first so removals stay stable).
func (m *tuiModel) commitToolIndicesToHistory(idxs []int) {
	if len(idxs) == 0 {
		return
	}
	// Dedup and sort descending.
	seen := map[int]bool{}
	var uniq []int
	for _, i := range idxs {
		if i < 0 || i >= len(m.toolRows) || seen[i] {
			continue
		}
		seen[i] = true
		uniq = append(uniq, i)
	}
	for i := 0; i < len(uniq); i++ {
		for j := i + 1; j < len(uniq); j++ {
			if uniq[j] > uniq[i] {
				uniq[i], uniq[j] = uniq[j], uniq[i]
			}
		}
	}
	for _, i := range uniq {
		if i < 0 || i >= len(m.toolRows) {
			continue
		}
		m.appendOneToolBlock(m.toolRows[i])
		m.toolRows = append(m.toolRows[:i], m.toolRows[i+1:]...)
		if m.toolPanel.Selected == i {
			m.toolPanel.Selected = -1
		} else if m.toolPanel.Selected > i {
			m.toolPanel.Selected--
		}
	}
	if m.toolPanel.Selected >= len(m.toolRows) {
		m.toolPanel.Selected = len(m.toolRows) - 1
	}
}

// forceCommitRemainingTools commits any leftover tools at turn end.
func (m *tuiModel) forceCommitRemainingTools() {
	var idxs []int
	for i := range m.toolRows {
		if !m.toolRows[i].Done {
			m.toolRows[i].Done = true
			m.toolRows[i].End = time.Now()
			if m.toolRows[i].Status == "" || m.toolRows[i].Status == "queued" || m.toolRows[i].Status == "running" {
				m.toolRows[i].Status = "completed"
			}
		}
		idxs = append(idxs, i)
	}
	m.commitToolIndicesToHistory(idxs)
}

func (m *tuiModel) appendOneToolBlock(r toolRow) {
	item := newToolRenderItem(r.Name, r.Detail, r.Result, r.Done, r.Failed)
	line := formatToolLine(item, m.width, terminalToolRenderOptions())
	rawContent := r.Detail
	if r.Result != "" {
		if rawContent != "" {
			rawContent += "\n"
		}
		rawContent += r.Result
	}
	m.appendBlock(ChatBlock{
		Kind:       ChatBlockTool,
		ToolName:   r.Name,
		ToolCallID: r.ToolCallID,
		Text:       strings.TrimRight(rawContent, "\n"),
		Rendered:   line,
		Collapsed:  true,
	})
}

// appendToolBlocks commits all current tool rows (legacy batch path / tests).
func (m *tuiModel) appendToolBlocks() {
	for _, r := range m.toolRows {
		m.appendOneToolBlock(r)
	}
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
	wasAtBottom := m.viewport.AtBottom()
	savedOffset := m.viewport.YOffset
	m.viewport.SetContent(content)
	if wasAtBottom {
		m.viewport.GotoBottom()
	} else {
		m.viewport.YOffset = min(savedOffset, m.viewport.TotalLineCount()-m.viewport.Height)
		if m.viewport.YOffset < 0 {
			m.viewport.YOffset = 0
		}
	}
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
	// Elapsed wait when nothing visible yet.
	if m.waiting && m.streamBuf.Len() == 0 && m.thinkingBuf.Len() == 0 && len(m.toolRows) == 0 {
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
	wasAtBottom := m.viewport.AtBottom()
	savedOffset := m.viewport.YOffset
	m.viewport.SetContent(content)
	if wasAtBottom {
		m.viewport.GotoBottom()
	} else {
		m.viewport.YOffset = min(savedOffset, m.viewport.TotalLineCount()-m.viewport.Height)
		if m.viewport.YOffset < 0 {
			m.viewport.YOffset = 0
		}
	}
}

// loadMoreMessages loads older messages from session history into the viewport.
// It prepends them to m.messages and adjusts the scroll offset so the user's
// current viewport position remains stable (showing the same content).
// Batch size: 50 messages at a time.
