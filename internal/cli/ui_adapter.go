package cli

import (
	"context"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	tea "github.com/charmbracelet/bubbletea"
)

// uiEventMsg carries a single event from the EventBus to the TUI.
type uiEventMsg struct {
	event events.Event
}

// uiTickMsg is a periodic heartbeat when no events arrive.
type uiTickMsg struct{}

// UIAdapter bridges the EventBus to the Bubble Tea TUI.
// It subscribes to all event kinds and forwards them to the TUI
// via a buffered channel consumed by PollCmd.
//
// With the async event bus (per-subscriber bounded queues with drop-oldest
// overflow), bus.Publish never blocks on this handler. The UIAdapter's
// internal channel provides the TUI poll loop its own buffered delivery
// semantics independent of the bus delivery mechanism.
type UIAdapter struct {
	bus     *events.Bus
	evChan  chan events.Event
	pollDur time.Duration
}

// NewUIAdapter creates a UIAdapter, subscribes it to agent and system event
// kinds handled by the bus path, and returns it. The bridge parameter is
// reserved for Phase 3 backward compat (may be nil).
func NewUIAdapter(bus *events.Bus, bridge *StreamBridge) *UIAdapter {
	a := &UIAdapter{
		bus:     bus,
		evChan:  make(chan events.Event, 512),
		pollDur: 80 * time.Millisecond,
	}
	// Subscribe to all agent and system events.
	allKinds := []events.Kind{
		events.KindAssistant, events.KindToolStart, events.KindToolEnd,
		events.KindStep, events.KindHeartbeat, events.KindPrune, events.KindToolParallel,
		events.KindSubagentStart, events.KindSubagentEnd, events.KindSubagentHeartbeat,
		events.KindSubagentDone,
		events.KindTurnStart, events.KindTurnEnd,
		events.KindWorkflowRunStarted, events.KindWorkflowStepStarted, events.KindWorkflowStepHeartbeat,
		events.KindWorkflowStepCompleted, events.KindWorkflowGateResult, events.KindWorkflowApprovalRequested,
		events.KindWorkflowRunFinished, events.KindWorkflowDeliveryStage,
		events.KindInvocationStarted, events.KindInvocationCompleted, events.KindInvocationRetrying,
		events.KindUIResize, events.KindUserInput, events.KindError,
	}
	bus.SubscribeMany(allKinds, a)
	_ = bridge // reserved for backward compat during migration
	return a
}

// HandleEvent implements events.Handler. It forwards events to the TUI via
// a buffered channel. Since the async bus guarantees Publish never blocks
// on this handler, we can use a simple non-blocking send for all events.
// The bus's per-subscriber queue (256) handles backpressure with drop-oldest
// before this handler is even called. If the TUI's own poll loop is slow
// and evChan fills, the non-blocking send drops here instead of blocking
// the bus delivery goroutine.
func (a *UIAdapter) HandleEvent(ctx context.Context, ev events.Event) {
	select {
	case a.evChan <- ev:
	default:
		// TUI poll loop not keeping up; drop event to avoid blocking
		// the bus delivery goroutine. This is acceptable for all event
		// kinds because:
		// - Non-critical events (ToolStart, Step, etc.) are transient UI updates
		// - TurnEnd: the bridge owns the primary finish path; this is backup
		// - Error: errors are also streamed through the bridge
	}
}

// PollCmd returns a self-perpetuating tea.Cmd that either:
// - Returns the next event from the channel as uiEventMsg, or
// - Returns uiTickMsg after pollDur timeout (heartbeat)
func (a *UIAdapter) PollCmd() tea.Cmd {
	return func() tea.Msg {
		select {
		case ev := <-a.evChan:
			return uiEventMsg{event: ev}
		case <-time.After(a.pollDur):
			return uiTickMsg{}
		}
	}
}
