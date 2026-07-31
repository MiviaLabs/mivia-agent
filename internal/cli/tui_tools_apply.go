package cli

import (
	"strings"
	"time"
)

// applyToolEvents applies tool start/end events with empty-speech status enabled.
func (m *tuiModel) applyToolEvents(evts []bridgeToolEvt) {
	m.applyToolEventsOpts(evts, true)
}

// applyToolEventsOpts applies tool events. emptySpeechStatus emits one Phase A
// status system line when the wave has no interim assistant speech.
func (m *tuiModel) applyToolEventsOpts(evts []bridgeToolEvt, emptySpeechStatus bool) {
	openBefore := 0
	for _, r := range m.toolRows {
		if !r.Done {
			openBefore++
		}
	}
	var finished []int
	statusEmitted := false
	for _, e := range evts {
		if e.Start {
			if openBefore == 0 {
				m.flushThinkingToHistory()
				if emptySpeechStatus && !statusEmitted {
					m.appendEmptySpeechToolStatus(evts)
					statusEmitted = true
				}
				openBefore = 1
			}
			m.applyToolStartEvent(e)
			if e.Name != "" && !isLifecycleStatus(e.Detail) {
				m.stepDetail = toolStatusLine(e.Name, e.Detail)
				m.stepDetailAt = time.Now()
			} else if e.Name != "" && m.stepDetail == "" {
				m.stepDetail = toolStatusLine(e.Name, e.Detail)
				m.stepDetailAt = time.Now()
			}
			m.clearAwaitingFirstActivity()
			continue
		}
		if idx := m.applyToolEndEvent(e); idx >= 0 {
			finished = append(finished, idx)
		}
	}
	m.commitToolIndicesToHistory(finished)
	if len(m.toolRows) > 0 {
		m.toolPanel.reindex(m.toolRows)
		// Live k/n on work-status + composer footer (Phase S.3).
		m.refreshLiveToolWaveStatus()
	} else {
		m.toolPanel = toolPanelState{Selected: -1}
	}
}

// appendEmptySpeechToolStatus commits one dim system status line for a tool wave
// with no interim assistant speech (never ChatBlockAssistant).
// Multi-tool waves store per-tool detail under the summary and start collapsed
// so Tab-focus + Enter/Space expands the list (and live tool panel when open).
func (m *tuiModel) appendEmptySpeechToolStatus(evts []bridgeToolEvt) {
	detail := toolBatchStatusDetail(evts)
	if detail == "" {
		return
	}
	// Seed live k/n counters for this wave (completed tools leave toolRows).
	if n := len(realToolStarts(evts)); n > 0 {
		m.toolWaveTotal = n
		m.toolWaveDone = 0
	}
	summary := detail
	if i := strings.IndexByte(detail, '\n'); i >= 0 {
		summary = detail[:i]
	}
	// Inject initial 0/n into multi-tool summary when we know the wave size.
	if m.toolWaveTotal >= 2 {
		open, done, total := m.toolWaveTotal, 0, m.toolWaveTotal
		elapsed := time.Duration(0)
		if !m.turnStart.IsZero() {
			elapsed = time.Since(m.turnStart)
		}
		summary = formatLiveToolWaveSummary(open, done, total, elapsed)
		// Rebuild detail body with live summary as first line.
		if i := strings.IndexByte(detail, '\n'); i >= 0 {
			detail = summary + detail[i:]
		} else {
			detail = summary
		}
	}
	collapsed := strings.Contains(detail, "\n")
	m.appendBlock(ChatBlock{
		Kind:      ChatBlockSystem,
		Text:      "→ " + detail,
		Rendered:  tuiDimStyle.Render("  → " + summary),
		Collapsed: collapsed,
	})
	m.clearAwaitingFirstActivity()
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
		ToolCallID: e.ToolCallID, Name: e.Name, Agent: e.Agent, Detail: detail, Status: status, Start: e.At,
	})
	if !m.toolPanel.Focused {
		m.toolPanel.Selected = len(m.toolRows) - 1
	}
	m.toolPanel.reindex(m.toolRows)
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
		failed := toolResultFailed(body) ||
			body == "failed" || strings.HasPrefix(body, "failed")
		if isLifecycleStatus(body) {
			// Lifecycle-only end (should be rare): keep status, do not wipe Result.
			m.toolRows[i].Status = body
			m.toolRows[i].Failed = lifecycleStatusFailed(body)
			if m.toolRows[i].Failed && m.toolRows[i].Result == "" {
				m.toolRows[i].Result = body
			}
		} else {
			m.toolRows[i].Result = body
			m.toolRows[i].Failed = failed
			if failed {
				m.toolRows[i].Status = "failed"
			} else {
				m.toolRows[i].Status = "completed"
			}
		}
		if m.toolWaveTotal > 0 && !isBannerTool(m.toolRows[i].Name) {
			m.toolWaveDone++
		}
		return i
	}
	return -1
}

func toolResultFailed(body string) bool {
	if body == "" {
		return false
	}
	low := strings.ToLower(strings.TrimSpace(body))
	if strings.HasPrefix(low, "error") || low == "failed" || strings.HasPrefix(low, "failed ") {
		return true
	}
	// Any non-zero exit= token (exit=1, exit=127, exit=timeout, exit=error, …).
	if i := strings.Index(low, "exit="); i >= 0 {
		rest := low[i+len("exit="):]
		if strings.HasPrefix(rest, "0") && (len(rest) == 1 || rest[1] < '0' || rest[1] > '9') {
			return false
		}
		return true
	}
	return false
}

// commitToolIndicesToHistory moves the given toolRows indices into ChatBlockTool
// history (highest index first so removals stay stable).
func (m *tuiModel) commitToolIndicesToHistory(idxs []int) {
	if len(idxs) == 0 {
		return
	}
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
	m.forceCommitRemainingToolsStatus("completed")
}

// forceCommitRemainingToolsStatus commits leftover tools with the given open status.
func (m *tuiModel) forceCommitRemainingToolsStatus(openStatus string) {
	var idxs []int
	for i := range m.toolRows {
		if !m.toolRows[i].Done {
			m.toolRows[i].Done = true
			m.toolRows[i].End = time.Now()
			if m.toolRows[i].Status == "" || m.toolRows[i].Status == "queued" || m.toolRows[i].Status == "running" {
				m.toolRows[i].Status = openStatus
			}
			if openStatus == "cancelled" {
				m.toolRows[i].Failed = true
			}
		}
		idxs = append(idxs, i)
	}
	m.commitToolIndicesToHistory(idxs)
}

func (m *tuiModel) appendOneToolBlock(r toolRow) {
	item := newToolRenderItem(r.Name, r.Detail, r.Result, r.Done, r.Failed)
	line := formatToolLine(item, m.width, terminalToolRenderOptions())
	if r.Agent != "" {
		line = agentBadgeStyle.Render("◆ "+r.Agent) + " " + line
	}
	if !r.Start.IsZero() {
		line += " " + toolTimeStyle.Render("· "+formatDuration(r.elapsed(time.Now())))
	}
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
		AgentName:  r.Agent,
		Text:       strings.TrimRight(rawContent, "\n"),
		Rendered:   line,
		Collapsed:  true,
		Failed:     r.Failed,
		Elapsed:    r.elapsed(time.Now()),
	})
}

// appendToolBlocks commits all current tool rows (legacy batch path / tests).
func (m *tuiModel) appendToolBlocks() {
	for _, r := range m.toolRows {
		m.appendOneToolBlock(r)
	}
}
