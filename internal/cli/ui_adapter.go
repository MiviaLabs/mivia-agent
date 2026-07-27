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
type UIAdapter struct {
	bus     *events.Bus
	evChan  chan events.Event
	pollDur time.Duration
}

// NewUIAdapter creates a UIAdapter, subscribes it to all agent and system
// event kinds, and returns it. The bridge parameter is reserved for Phase 3
// backward compat (may be nil).
func NewUIAdapter(bus *events.Bus, bridge *streamBridge) *UIAdapter {
	a := &UIAdapter{
		bus:     bus,
		evChan:  make(chan events.Event, 512),
		pollDur: 80 * time.Millisecond,
	}
	// Subscribe to all agent and system events.
	allKinds := []events.Kind{
		events.KindAssistant, events.KindToolStart, events.KindToolEnd,
		events.KindStep, events.KindPrune, events.KindToolParallel,
		events.KindSubagentStart, events.KindSubagentEnd, events.KindSubagentHeartbeat,
		events.KindTurnStart, events.KindTurnEnd,
		events.KindUIResize, events.KindUserInput, events.KindError,
	}
	bus.SubscribeMany(allKinds, a)
	_ = bridge // reserved for backward compat during migration
	return a
}

// HandleEvent implements events.Handler. It forwards events to the TUI via
// a buffered channel. If the channel is full, the event is dropped (backpressure).
func (a *UIAdapter) HandleEvent(ctx context.Context, ev events.Event) {
	select {
	case a.evChan <- ev:
	default:
		// Backpressure: drop if channel full.
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
