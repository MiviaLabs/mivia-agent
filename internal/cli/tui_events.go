package cli

import (
	"context"
	"errors"
	"fmt"
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
	var cmds []tea.Cmd
	m.trackSubagentEvent(ev)

	switch ev.Kind {
	case events.KindSubagentStart, events.KindSubagentEnd, events.KindSubagentDone:
		// Tool rows are owned by the bridge path; the tracker was already fed
		// by trackSubagentEvent above. Done retires the run from the live
		// agent view there - nothing further to do here.

	case events.KindStep, events.KindSubagentHeartbeat:
		m.applyStepEvent(ev)

	case events.KindWorkflowRunStarted, events.KindWorkflowStepStarted, events.KindWorkflowStepHeartbeat,
		events.KindWorkflowStepCompleted, events.KindWorkflowGateResult, events.KindWorkflowApprovalRequested,
		events.KindWorkflowRunFinished:
		cmds = append(cmds, m.applyWorkflowProgressEvent()...)

	case events.KindError:
		m.stepDetail = "error: " + ev.Detail
		m.stalledWarning = true

	// KindAssistant / tools / prune / parallel / TurnEnd: bridge owns UI.
	// TurnEnd is intentionally ignored for finish - bridge.Finish drives it.
	// (Idempotent finishStream would also tolerate dual finish.)
	case events.KindTurnEnd:
		return m.applyTurnEndEvent(ev)

	default:
		// Ignore KindAssistant/Tool*/Prune/Parallel - applied via bridge drain.
	}
	return cmds
}

// trackSubagentEvent feeds the subagent tracker for attributed events. It
// runs regardless of the phase gates in applyEvent: attributed events arrive
// before the first drain sets m.waiting, and a dropped start leaves the fleet
// box empty for the rest of the turn.
func (m *tuiModel) trackSubagentEvent(ev events.Event) {
	if ev.AgentTask != "" {
		m.subagents.Apply(ev, time.Now())
	}
}

// applyStepEvent records the latest step/heartbeat detail for the status bar.
func (m *tuiModel) applyStepEvent(ev events.Event) {
	detail := ev.Detail
	if ev.AgentName != "" {
		detail = "◆ " + ev.AgentName + " · " + detail
	}
	m.stepDetail = detail
	m.stepDetailAt = time.Now()
	m.stalledWarning = false
}

// applyWorkflowProgressEvent refreshes the /workflows sidebar and the open
// run-detail dialog on workflow progress events. Each refresh returns a
// tea.Cmd so the ledger read runs off the update goroutine, and both throttle
// to at most one read per interval; a closed sidebar or dialog is a no-op.
// Events for runs started in other terminals reach the sidebar through the
// uiTickMsg path instead, because they do not pass the updateMessageImpl gate.
func (m *tuiModel) applyWorkflowProgressEvent() []tea.Cmd {
	var cmds []tea.Cmd
	if cmd := m.refreshWorkflowsSidebar(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if cmd := m.refreshWorkflowRunDialog(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return cmds
}

// applyTurnEndEvent is the backup finish path for a KindTurnEnd that arrives
// without the bridge drain ever observing done (should be rare).
func (m *tuiModel) applyTurnEndEvent(ev events.Event) []tea.Cmd {
	if !m.waiting {
		return nil
	}
	if ev.TurnID != "" && m.activeTurnID != "" && ev.TurnID != m.activeTurnID {
		return nil
	}
	// Only finish here when the bridge is fully drained. bridge.Finish
	// (tui_start.go) strictly precedes publishTurnEnd, so this event can
	// arrive while the bridge still holds the final stream chunk and an
	// unconsumed Done. Finishing now would commit only the drained prefix
	// and lose the tail: finishStream flips waiting=false, and the later
	// drain writes the tail into streamBuf with no one to commit it.
	// Skip instead - the self-perpetuating pollCmd tick (80ms) drains the
	// bridge and finishes via d.Done, so the tail lands in m.blocks.
	// An empty/nil bridge counts as drained, keeping this as the rescue
	// path for a lost bridge notify / bus-only TurnEnd.
	m.mu.Lock()
	bridge := m.bridge
	m.mu.Unlock()
	if bridge != nil && bridge.Pending() {
		return nil
	}
	// Still finish so UI cannot stick waiting if bridge notify was lost.
	return m.finishStream(turnEndError(ev))
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
		Identity:  sessionIdentity(m.session, m.agentState, m.session.CurrentModelGeneration()),
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
			// Banner only - must not leave an open tool row. A Start without End
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
		case agent.EventHook:
			// Banner, never a tool row: a hook has no start/end pair, and an
			// unmatched Start is what leaves activeTools permanently inflated.
			bridge.PushCompletedBanner(hookBannerLabel(e), hookBannerBody(e))
		case agent.EventStep, agent.EventHeartbeat:
			bridge.PushStep(e.Detail)
		case agent.EventSubagentStart:
			bridge.PushSubagentTool(true, e.ToolCallID, e.Origin.Agent, e.Name, eventPreview(e.Input, e.Detail))
		case agent.EventSubagentEnd:
			bridge.PushSubagentTool(false, e.ToolCallID, e.Origin.Agent, e.Name, eventPreview(e.Output, e.Detail))
		case agent.EventSubagentHeartbeat:
			bridge.PushStep(e.Detail)
		case agent.EventCompaction:
			if e.Compaction != nil {
				bridge.PushCompletedBanner("context", renderCompactionNotice(*e.Compaction))
			}
		}
	}
}

func renderCompactionNotice(event events.CompactionEvent) string {
	if err := event.Validate(); err != nil {
		return "context compacted"
	}
	notice := fmt.Sprintf("context compacted: %d -> %d tokens", event.BeforeTokens, event.AfterTokens)
	if event.ElidedMessages > 0 {
		unit := "tool results"
		if event.ElidedMessages == 1 {
			unit = "tool result"
		}
		notice = fmt.Sprintf("%s (%d %s elided, %d bytes)", notice, event.ElidedMessages, unit, event.ElidedBytes)
	}
	return notice
}

// hookBannerLabel names the row. The event is in the label rather than buried
// in the body so a screen full of PostToolUse chatter can be told apart at a
// glance from the one PreToolUse row that stopped a call.
func hookBannerLabel(e agent.Event) string {
	if e.Name == "" {
		return "hook"
	}
	return "hook " + e.Name
}

// hookBannerBody keeps the summary even when the hook said nothing, because
// "ran, no output" is the answer to the question the row exists to answer.
func hookBannerBody(e agent.Event) string {
	if e.Output == "" {
		return e.Detail
	}
	return e.Detail + ": " + e.Output
}

func eventPreview(preview, fallback string) string {
	if preview != "" {
		return preview
	}
	return fallback
}
