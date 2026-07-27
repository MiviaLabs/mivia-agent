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

	toolH := 0
	if m.waiting && len(m.toolRows) > 0 {
		// Windowed tool strip: header(+hint) + at most toolMaxVisibleRows + expand.
		want := m.calcToolPanelLines()
		cap := max(3, avail/3)
		toolH = min(cap, want)
	}
	// Leave room for optional ↓ indicator line.
	extra := toolH
	vpHeight := max(3, avail-extra)
	if toolH+vpHeight > avail {
		vpHeight = max(3, avail-toolH)
	}

	if !m.ready {
		m.viewport = viewport.New(max(1, m.width), vpHeight)
		m.textarea.SetWidth(composerInnerWidth(m.width))
		m.ready = true
	} else {
		m.viewport.Width = max(1, m.width)
		m.viewport.Height = vpHeight
	}
}

// calcToolPanelLines estimates rendered lines for the windowed tool panel.
func (m *tuiModel) calcToolPanelLines() int {
	if len(m.toolRows) == 0 {
		return 0
	}
	// header + optional hint + up to toolMaxVisibleRows collapsed rows
	lines := 1
	if m.toolPanel.Focused || len(m.toolRows) > toolMaxVisibleRows {
		lines++ // hint
	}
	nVis := min(toolMaxVisibleRows, len(m.toolRows))
	lines += nVis
	// Expand only the selected row when Expanded.
	sel := m.toolPanel.Selected
	if sel >= 0 && sel < len(m.toolRows) && m.toolRows[sel].Expanded {
		r := m.toolRows[sel]
		maxPreview := 6
		if isEditTool(r.Name) {
			maxPreview = 10
		}
		if r.Detail != "" {
			lines++ // input header
			lines += min(maxPreview+1, 1+strings.Count(r.Detail, "\n"))
		}
		if r.Result != "" {
			lines++ // output header
			lines += min(maxPreview+1, 1+strings.Count(r.Result, "\n"))
		}
	}
	return lines
}

func (m *tuiModel) applyToolEvents(evts []bridgeToolEvt) {
	for _, e := range evts {
		if e.Start {
			m.applyToolStartEvent(e)
			continue
		}
		m.applyToolEndEvent(e)
	}
	if len(m.toolRows) > 0 {
		m.toolPanel.ordered = orderToolIndices(m.toolRows)
		m.toolPanel.Scroll = clampToolScroll(
			m.toolPanel.Scroll, m.toolPanel.Selected, m.toolPanel.ordered, toolMaxVisibleRows,
		)
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

func (m *tuiModel) applyToolEndEvent(e bridgeToolEvt) {
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
			m.toolRows[i].Failed = body == "failed"
		} else {
			m.toolRows[i].Result = body
			m.toolRows[i].Status = "completed"
			m.toolRows[i].Failed = failed
			if failed {
				m.toolRows[i].Status = "failed"
			}
		}
		return
	}
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

func (m *tuiModel) finishStream(err error) []tea.Cmd {
	m.waiting = false
	raw := m.streamBuf.String()
	m.streamBuf.Reset()

	if strings.TrimSpace(raw) != "" {
		m.appendBlock(ChatBlock{Kind: ChatBlockAssistant, Text: raw})
	}
	if thinking := strings.TrimSpace(m.thinkingBuf.String()); thinking != "" {
		m.appendBlock(ChatBlock{
			Kind:         ChatBlockThinking,
			Text:         thinking,
			ScrollOffset: m.liveThinkingScroll,
		})
	}

	if len(m.toolRows) > 0 {
		var toolLines []string
		for _, r := range m.toolRows {
			item := newToolRenderItem(r.Name, r.Detail, r.Result, r.Done, r.Failed)
			opts := terminalToolRenderOptions()
			toolLines = append(toolLines, formatToolLine(item, m.width, opts))
		}
		m.appendBlock(ChatBlock{
			Kind:      ChatBlockTool,
			ToolName:  "tools",
			Text:      strings.Join(toolLines, "\n"),
			Collapsed: true,
		})
	}

	total := time.Since(m.turnStart)
	if err != nil && err != context.Canceled {
		text := "error: " + SafeChatBlockText(err.Error(), 240)
		m.appendBlock(ChatBlock{
			Kind:     ChatBlockDivider,
			Text:     text,
			Rendered: tuiErrorStyle.Render(text),
		})
	} else if err == context.Canceled {
		text := fmt.Sprintf("(cancelled · %s)", formatDuration(total))
		m.appendBlock(ChatBlock{
			Kind:     ChatBlockDivider,
			Text:     text,
			Rendered: tuiDimStyle.Render(text),
		})
	} else {
		text := fmt.Sprintf("  ─ done · %s ─", formatDuration(total))
		m.appendBlock(ChatBlock{
			Kind:     ChatBlockDivider,
			Text:     text,
			Rendered: tuiDimStyle.Render(text),
		})
	}

	m.toolRows = nil
	m.toolPanel = toolPanelState{Selected: -1}
	m.thinkingBuf.Reset()
	m.liveThinkingScroll = 0
	m.layout()
	m.renderVP()
	// Do not textarea.Reset() here: user may have typed a draft while waiting.
	// startAI / sendNextQueued still Reset after capturing the sent text.
	m.mu.Lock()
	m.cancel = nil
	m.mu.Unlock()

	// Auto-send next queued message if any.
	if len(m.pendingQueue) > 0 {
		m.sendNextQueued()
		if m.waiting {
			return []tea.Cmd{m.pollCmd()}
		}
	}
	return nil
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
	if m.streamBuf.Len() > 0 {
		if content != "" {
			content += "\n"
		}
		content += tuiDimStyle.Render("▌ ") + m.streamBuf.String()
	}
	if m.thinkingBuf.Len() > 0 {
		thinkingStr := renderThinkingBlockView(
			"thinking-live",
			m.thinkingBuf.String(),
			false, // never collapsed during live stream
			m.liveThinkingScroll,
			m.modelName,
			m.width,
			true, // always show expanded during live stream
		)
		if thinkingStr != "" {
			if content != "" {
				content += "\n"
			}
			content += thinkingStr
		}
	}
	// Show elapsed thinking time when waiting with no visible activity yet.
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
