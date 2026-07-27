package cli

import (
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// applyEvent applies a single EventBus event directly to the TUI model state,
// bypassing the streamBridge. This is the primary event path when the UIAdapter
// is active (Phase 3+). The legacy bridge path (tuiTickMsg + updateFromDrain)
// remains as a fallback when no adapter is available.
func (m *tuiModel) applyEvent(ev events.Event) {
	if m.mode != modeChat {
		return
	}

	switch ev.Kind {
	case events.KindToolStart:
		m.applyToolEventFromBus(ev)

	case events.KindToolEnd:
		m.applyToolEventFromBus(ev)

	case events.KindSubagentStart:
		m.applyToolEventFromBus(ev)

	case events.KindSubagentEnd:
		m.applyToolEventFromBus(ev)

	case events.KindAssistant:
		if ev.Content != "" {
			m.thinkingBuf.WriteString(ev.Content)
			m.renderStreamVP()
		}

	case events.KindStep:
		m.stepDetail = ev.Detail
		m.stepDetailAt = time.Now()

	case events.KindPrune:
		m.toolRows = append(m.toolRows, toolRow{
			Name:   "prune",
			Detail: ev.Detail,
			Start:  ev.Timestamp,
			Done:   true,
			End:    ev.Timestamp,
			Status: "completed",
		})
		m.toolPanel.ordered = orderToolIndices(m.toolRows)

	case events.KindToolParallel:
		m.toolRows = append(m.toolRows, toolRow{
			Name:   "parallel",
			Detail: ev.Detail,
			Start:  ev.Timestamp,
			Done:   true,
			End:    ev.Timestamp,
			Status: "completed",
		})
		m.toolPanel.ordered = orderToolIndices(m.toolRows)

	case events.KindSubagentHeartbeat:
		m.stepDetail = ev.Detail
		m.stepDetailAt = time.Now()

	case events.KindTurnEnd:
		if m.waiting {
			m.finishStream(nil)
		}

	case events.KindError:
		m.stepDetail = "error: " + ev.Detail
		m.stalledWarning = true
	}
}

// applyToolEventFromBus handles tool lifecycle events from the EventBus.
// It directly manipulates m.toolRows, matching the behaviour of
// applyToolStartEvent / applyToolEndEvent from the bridge path.
func (m *tuiModel) applyToolEventFromBus(ev events.Event) {
	switch ev.Kind {
	case events.KindToolStart, events.KindSubagentStart:
		if ev.ToolCallID != "" {
			for i := range m.toolRows {
				if !m.toolRows[i].Done && m.toolRows[i].ToolCallID == ev.ToolCallID {
					if ev.Name != "" {
						m.toolRows[i].Name = ev.Name
					}
					if isLifecycleStatus(ev.Detail) {
						m.toolRows[i].Status = ev.Detail
					} else if ev.Detail != "" {
						m.toolRows[i].Detail = ev.Detail
					}
					return
				}
			}
		}
		status, detail := "queued", ev.Detail
		if isLifecycleStatus(ev.Detail) {
			status, detail = ev.Detail, ""
		}
		m.toolRows = append(m.toolRows, toolRow{
			ToolCallID: ev.ToolCallID,
			Name:       ev.Name,
			Detail:     detail,
			Status:     status,
			Start:      ev.Timestamp,
		})
		if !m.toolPanel.Focused {
			m.toolPanel.Selected = len(m.toolRows) - 1
		}
		m.toolPanel.ordered = orderToolIndices(m.toolRows)
		m.toolPanel.Scroll = clampToolScroll(
			m.toolPanel.Scroll, m.toolPanel.Selected, m.toolPanel.ordered, toolMaxVisibleRows,
		)
		if m.waiting && !m.stalledWarning {
			m.layout()
			m.renderStreamVP()
		}

	case events.KindToolEnd, events.KindSubagentEnd:
		for i := len(m.toolRows) - 1; i >= 0; i-- {
			match := !m.toolRows[i].Done &&
				((ev.ToolCallID != "" && m.toolRows[i].ToolCallID == ev.ToolCallID) ||
					(ev.ToolCallID == "" && m.toolRows[i].Name == ev.Name))
			if !match {
				continue
			}
			m.toolRows[i].Done = true
			m.toolRows[i].End = ev.Timestamp
			body := ev.Output
			if body == "" {
				body = ev.Detail
			}
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
}

// agentEventBridgeCallback returns an OnEvent handler that forwards
// agent loop events to the TUI bridge for rendering.
func agentEventBridgeCallback(bridge *streamBridge) func(agent.Event) {
	return func(e agent.Event) {
		switch e.Kind {
		case agent.EventToolStart:
			bridge.PushToolWithID(true, e.ToolCallID, e.Name, eventPreview(e.Input, e.Detail))
		case agent.EventToolEnd:
			bridge.PushToolWithID(false, e.ToolCallID, e.Name, eventPreview(e.Output, e.Detail))
		case agent.EventToolParallel:
			bridge.PushTool(true, "parallel", e.Detail)
		case agent.EventPrune:
			bridge.PushTool(false, "prune", e.Detail)
		case agent.EventAssistant:
			if e.Content != "" {
				bridge.PushThinking(e.Content)
			}
		case agent.EventStep:
			bridge.PushStep(e.Detail)
		case agent.EventSubagentStart:
			bridge.PushToolWithID(true, e.ToolCallID, e.Name, eventPreview(e.Input, e.Detail))
		case agent.EventSubagentEnd:
			bridge.PushToolWithID(false, e.ToolCallID, e.Name, eventPreview(e.Output, e.Detail))
		case agent.EventSubagentHeartbeat:
			bridge.PushStep(e.Detail)
		}
	}
}

func eventPreview(preview, fallback string) string {
	if preview != "" {
		return preview
	}
	return fallback
}
