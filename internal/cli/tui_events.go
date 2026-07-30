package cli

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	tea "github.com/charmbracelet/bubbletea"
)

// applyEvent applies EventBus events that are safe alongside the bridge path.
// Content, tools, and turn finish are owned by streamBridge drain (tuiTickMsg).
// Bus handlers must not double-apply those or the transcript duplicates/races.
func (m *tuiModel) applyEvent(ev events.Event) []tea.Cmd {
	if m.mode != modeChat {
		return nil
	}
	// Attributed subagent events feed the tracker regardless of the phase
	// gates below: they arrive before the first drain sets m.waiting, and a
	// dropped start leaves the fleet box empty for the rest of the turn.
	if ev.AgentTask != "" {
		m.subagents.Apply(ev, time.Now())
	}

	switch ev.Kind {
	case events.KindSubagentStart, events.KindSubagentEnd:
		// Tool rows are owned by the bridge path; the tracker was already
		// fed above (fleet box / ledger data spine).

	case events.KindStep, events.KindSubagentHeartbeat:
		detail := ev.Detail
		if ev.AgentName != "" {
			detail = "◆ " + ev.AgentName + " · " + detail
		}
		m.stepDetail = detail
		m.stepDetailAt = time.Now()
		m.stalledWarning = false

	case events.KindError:
		m.stepDetail = "error: " + ev.Detail
		m.stalledWarning = true

	// KindAssistant / tools / prune / parallel / TurnEnd: bridge owns UI.
	// TurnEnd is intentionally ignored for finish — bridge.Finish drives it.
	// (Idempotent finishStream would also tolerate dual finish.)
	case events.KindTurnEnd:
		// Backup finish only if bridge drain never saw done (should be rare).
		if !m.waiting {
			return nil
		}
		if ev.TurnID != "" && m.activeTurnID != "" && ev.TurnID != m.activeTurnID {
			return nil
		}
		// Prefer bridge-owned finish; only use bus if stream/tools already
		// drained and waiting is still true after a grace path.
		// Still finish so UI cannot stick waiting if bridge notify was lost.
		return m.finishStream(turnEndError(ev))

	default:
		// Ignore KindAssistant/Tool*/Prune/Parallel — applied via bridge drain.
	}
	return nil
}

// publishTurnEnd emits KindTurnEnd on the session bus (if any). Turn finish
// for the TUI is driven by bridge.Finish → drain, not by this event alone.
func (m *tuiModel) publishTurnEnd(turnID string, err error) {
	if m.eventBus == nil {
		return
	}
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	m.eventBus.Publish(events.Event{
		Kind:      events.KindTurnEnd,
		Timestamp: time.Now(),
		TurnID:    turnID,
		Detail:    detail,
		Err:       err,
	})
}

// turnEndError maps a KindTurnEnd event to the error finishStream expects.
// Preserves context.Canceled identity for the cancel footer.
func turnEndError(ev events.Event) error {
	if ev.Err != nil {
		if errors.Is(ev.Err, context.Canceled) {
			return context.Canceled
		}
		return ev.Err
	}
	if ev.Detail == "" {
		return nil
	}
	if ev.Detail == context.Canceled.Error() {
		return context.Canceled
	}
	return errors.New(ev.Detail)
}

// applyToolEventFromBus handles tool lifecycle events from the EventBus.
// Retained for tests and optional Program.Send wiring; production TUI uses
// bridge drain instead to avoid double-applying with OnEvent→bridge.
func (m *tuiModel) applyToolEventFromBus(ev events.Event) {
	switch ev.Kind {
	case events.KindToolStart, events.KindSubagentStart:
		m.applyToolStartFromBus(ev)
	case events.KindToolEnd, events.KindSubagentEnd:
		m.applyToolEndFromBus(ev)
	}
}

func (m *tuiModel) applyToolStartFromBus(ev events.Event) {
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
				} else if ev.Input != "" && m.toolRows[i].Detail == "" {
					m.toolRows[i].Detail = ev.Input
				}
				m.stalledWarning = false
				m.refreshToolPanelIfWaiting()
				return
			}
		}
	}
	status, detail := "queued", eventPreview(ev.Input, ev.Detail)
	if isLifecycleStatus(ev.Detail) {
		status = ev.Detail
		detail = eventPreview(ev.Input, "")
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
	m.stalledWarning = false
	m.refreshToolPanelIfWaiting()
}

func (m *tuiModel) applyToolEndFromBus(ev events.Event) {
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
		failed := toolResultFailed(body) ||
			body == "failed" || strings.HasPrefix(body, "failed")
		if isLifecycleStatus(body) {
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
		m.toolPanel.ordered = orderToolIndices(m.toolRows)
		m.toolPanel.Scroll = clampToolScroll(
			m.toolPanel.Scroll, m.toolPanel.Selected, m.toolPanel.ordered, toolMaxVisibleRows,
		)
		m.stalledWarning = false
		m.refreshToolPanelIfWaiting()
		return
	}
}

func (m *tuiModel) refreshToolPanelIfWaiting() {
	if m.waiting {
		m.layout()
		m.renderStreamVP()
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
			// Banner only — must not leave an open tool row. A Start without End
			// permanently inflated activeTools and kept the row yellow forever
			// (status "queued", spinning glyph). Complete immediately.
			bridge.PushCompletedBanner("parallel", e.Detail)
		case agent.EventPrune:
			// Same as parallel: visibility banner, not an in-flight tool.
			bridge.PushCompletedBanner("prune", e.Detail)
		case agent.EventAssistant:
			// Intermediate multi-bubble speech only (Detail=interim). Final answer
			// streams via FinalWriter → streamBuf; do not PushInterim finals or
			// we would duplicate the assistant block at turn end.
			if e.Content != "" && e.Detail == "interim" {
				bridge.PushInterim(e.Content)
			}
		case agent.EventThinking:
			// Chain of thought: dim chrome, never a speech bubble.
			if e.Content != "" {
				bridge.PushThinking(e.Content)
			}
		case agent.EventStep:
			bridge.PushStep(e.Detail)
		case agent.EventSubagentStart:
			bridge.PushSubagentTool(true, e.ToolCallID, e.Origin.Agent, e.Name, eventPreview(e.Input, e.Detail))
		case agent.EventSubagentEnd:
			bridge.PushSubagentTool(false, e.ToolCallID, e.Origin.Agent, e.Name, eventPreview(e.Output, e.Detail))
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
