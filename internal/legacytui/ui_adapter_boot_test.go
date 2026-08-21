package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	tea "github.com/charmbracelet/bubbletea"
)

// The UIAdapter poll chain is self-perpetuating: every uiEventMsg/uiTickMsg
// re-issues PollCmd. Nothing re-issues the FIRST one, so Init must start it
// - without that, the bus side channel is dead in production and everything
// fed by it (subagent tracker → fleet box) silently never appears.

func collectCmdMsgs(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	// tea.Batch returns a BatchMsg carrying the child commands.
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	out := make([]tea.Msg, 0, len(batch))
	done := make(chan tea.Msg, len(batch))
	for _, c := range batch {
		go func(c tea.Cmd) {
			if c == nil {
				done <- nil
				return
			}
			done <- c()
		}(c)
	}
	deadline := time.After(2 * time.Second)
	for i := 0; i < len(batch); i++ {
		select {
		case m := <-done:
			out = append(out, m)
		case <-deadline:
			return out
		}
	}
	return out
}

func TestInitStartsUIAdapterPollChain(t *testing.T) {
	m := newTUIModel(makeTestSession(), nil, true)
	bus := events.New()
	m.eventBus = bus
	m.uiAdapter = NewUIAdapter(bus, m.bridge)

	// Publish before Init so the adapter's channel already holds an event:
	// a started poll chain must deliver it as uiEventMsg.
	bus.Publish(events.Event{Kind: events.KindSubagentStart, Name: "grep"}.
		WithAgentAttribution("t1", "audit", 1))
	bus.Flush()

	msgs := collectCmdMsgs(t, m.Init())
	for _, msg := range msgs {
		if ev, ok := msg.(uiEventMsg); ok {
			if ev.event.AgentName != "audit" {
				t.Fatalf("wrong event delivered: %+v", ev.event)
			}
			return
		}
	}
	t.Fatalf("Init did not start the UIAdapter poll chain: bus events never reach applyEvent (got %d msgs)", len(msgs))
}

func TestSubagentEventsReachTrackerThroughBus(t *testing.T) {
	// End-to-end through the real publish path: cli.EmitSubagentProgress →
	// bus → adapter channel → uiEventMsg → applyEvent → tracker.
	m := newReadyChatModel(30, 80)
	m.waiting = true
	bus := events.New()
	m.eventBus = bus
	m.uiAdapter = NewUIAdapter(bus, m.bridge)
	cli.SetGlobalBus(bus)
	t.Cleanup(func() { cli.SetGlobalBus(nil) })

	cli.EmitSubagentProgress(agent.Event{
		Kind:       agent.EventSubagentStart,
		Name:       "grep",
		ToolCallID: "c1",
		Origin:     agent.EventOrigin{TaskID: "t1", Agent: "audit", Depth: 1},
	})
	bus.Flush()

	// Drain one event through the adapter and apply it like Update would.
	msg := m.uiAdapter.PollCmd()()
	ev, ok := msg.(uiEventMsg)
	if !ok {
		t.Fatalf("expected uiEventMsg, got %T", msg)
	}
	if ev.event.AgentName != "audit" {
		t.Fatalf("attribution lost on the bus: %+v", ev.event)
	}
	m.applyEvent(ev.event)
	if m.subagents.Active() != 1 {
		t.Fatal("subagent tracker not fed through the bus path")
	}
	if !m.fleetBoxVisible() {
		t.Fatal("fleet box still hidden after a real subagent event")
	}
}

func TestUIAdapterPollChainSurvivesNonChatMode(t *testing.T) {
	// The chain must re-queue in every mode, or a welcome-screen event
	// silently kills it for the whole session (INV-TUI-2 sibling).
	m := newTUIModel(makeTestSession(), nil, true)
	m.mode = modeWelcome
	bus := events.New()
	m.eventBus = bus
	m.uiAdapter = NewUIAdapter(bus, m.bridge)

	_, cmd := m.Update(uiEventMsg{event: events.Event{Kind: events.KindStep}})
	if cmd == nil {
		t.Fatal("uiEventMsg in welcome mode must still re-queue the poll chain")
	}
	_, cmd = m.Update(uiTickMsg{})
	if cmd == nil {
		t.Fatal("uiTickMsg must re-queue the poll chain")
	}
}
