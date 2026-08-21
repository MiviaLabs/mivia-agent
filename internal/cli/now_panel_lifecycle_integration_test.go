package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// The "now" panel names what is happening right now. Anything that has
// finished - a tool or an agent - must leave it and live on in the transcript
// (tools) or the ctrl+g fleet detail (agents). These tests drive the real
// publish paths end to end: emitSubagentProgress → bus → UIAdapter → applyEvent
// for agents, and the bridge drain for tools.

// nowPanelModel is a chat model wired to a live bus and adapter, mid-turn.
func nowPanelModel(t *testing.T) (*tuiModel, *events.Bus) {
	t.Helper()
	m := newReadyChatModel(40, 100)
	m.waiting = true
	m.turnStart = time.Now()
	bus := events.New()
	m.eventBus = bus
	m.uiAdapter = NewUIAdapter(bus, m.bridge)
	SetGlobalBus(bus)
	t.Cleanup(func() { SetGlobalBus(nil) })
	return m, bus
}

// pumpAgentEvents publishes agent events through the real path and applies
// every one the adapter delivers.
func pumpAgentEvents(t *testing.T, m *tuiModel, bus *events.Bus, evts ...agent.Event) {
	t.Helper()
	for _, e := range evts {
		emitSubagentProgress(e)
	}
	bus.Flush()
	for range evts {
		msg := m.uiAdapter.PollCmd()()
		ev, ok := msg.(uiEventMsg)
		if !ok {
			t.Fatalf("adapter did not deliver an event (got %T) - the kind is probably missing from the bus subscription", msg)
		}
		m.applyEvent(ev.event)
	}
}

func nowPanelText(m *tuiModel) string {
	return stripANSI(m.renderLivePanel(100, time.Now()))
}

func agentEvent(kind agent.EventKind, task, name, tool string) agent.Event {
	return agent.Event{
		Kind:   kind,
		Name:   tool,
		Origin: agent.EventOrigin{TaskID: task, Agent: name, Depth: 1},
	}
}

func TestIntegrationNowPanelDropsFinishedAgent(t *testing.T) {
	m, bus := nowPanelModel(t)

	pumpAgentEvents(t, m, bus,
		agentEvent(agent.EventSubagentStart, "t1", "audit", "grep"),
		agentEvent(agent.EventSubagentStart, "t2", "fix-tests", "read_file"),
	)
	if got := nowPanelText(m); !strings.Contains(got, "audit") || !strings.Contains(got, "fix-tests") {
		t.Fatalf("both running agents must show in now:\n%s", got)
	}

	// audit closes its tool and then its run; fix-tests keeps working.
	pumpAgentEvents(t, m, bus,
		agentEvent(agent.EventSubagentEnd, "t1", "audit", "grep"),
		agentEvent(agent.EventSubagentDone, "t1", "audit", ""),
	)

	got := nowPanelText(m)
	if strings.Contains(got, "audit") {
		t.Fatalf("finished agent must leave the now panel:\n%s", got)
	}
	if !strings.Contains(got, "fix-tests") {
		t.Fatalf("still-running agent must remain in the now panel:\n%s", got)
	}
	// Turn history keeps it: ctrl+g detail is the record, not the live panel.
	if len(m.subagents.Rows()) != 2 {
		t.Fatalf("finished run must survive in turn history, rows=%d", len(m.subagents.Rows()))
	}
	if n := m.subagents.Active(); n != 1 {
		t.Fatalf("active count=%d want 1 - the header must agree with the rows below it", n)
	}
}

func TestIntegrationNowPanelKeepsAgentIdleBetweenTools(t *testing.T) {
	// The bug this fix replaced inferred "done" from "no open tools", which
	// retired every agent thinking between two tool calls.
	m, bus := nowPanelModel(t)

	pumpAgentEvents(t, m, bus,
		agentEvent(agent.EventSubagentStart, "t1", "audit", "grep"),
		agentEvent(agent.EventSubagentEnd, "t1", "audit", "grep"),
	)

	got := nowPanelText(m)
	if !strings.Contains(got, "audit") {
		t.Fatalf("an agent between two tool calls is still running:\n%s", got)
	}
	if strings.Contains(got, "✓") {
		t.Fatalf("no ✓ before the run-level done event:\n%s", got)
	}
	if n := m.subagents.Active(); n != 1 {
		t.Fatalf("active count=%d want 1", n)
	}
}

func TestIntegrationNowPanelHidesWhenEveryAgentFinished(t *testing.T) {
	m, bus := nowPanelModel(t)

	pumpAgentEvents(t, m, bus,
		agentEvent(agent.EventSubagentStart, "t1", "audit", "grep"),
		agentEvent(agent.EventSubagentDone, "t1", "audit", ""),
	)

	if m.fleetBoxVisible() {
		t.Fatal("fleet box must hide once no agent is running")
	}
	if h := m.fleetBoxHeight(); h != 0 {
		t.Fatalf("fleet box height=%d want 0 - layout and view must agree", h)
	}
	if fleet, _, _, _ := m.livePanelSections(40); fleet != 0 {
		t.Fatalf("now panel still budgets %d fleet rows for finished agents", fleet)
	}
	if strings.Contains(nowPanelText(m), "audit") {
		t.Fatalf("finished agent still painted:\n%s", nowPanelText(m))
	}
}

func TestIntegrationFleetOverlayKeepsFinishedRunsLabelled(t *testing.T) {
	// The live panel drops finished agents; ctrl+g is where the turn's record
	// lives, so it has to say which of them are still running.
	m, bus := nowPanelModel(t)
	pumpAgentEvents(t, m, bus,
		agentEvent(agent.EventSubagentStart, "t1", "audit", "grep"),
		agentEvent(agent.EventSubagentStart, "t2", "fix-tests", "read_file"),
		agentEvent(agent.EventSubagentDone, "t1", "audit", ""),
	)

	if !m.openFleetOverlay() {
		t.Fatal("overlay did not open")
	}
	got := stripANSI(strings.Join(m.overlay.lines, "\n"))
	if !strings.Contains(got, "audit") || !strings.Contains(got, "done") {
		t.Fatalf("finished run missing or unlabelled in the overlay:\n%s", got)
	}
	if !strings.Contains(got, "fix-tests") || !strings.Contains(got, "running") {
		t.Fatalf("running run missing or unlabelled in the overlay:\n%s", got)
	}
}

func TestTrackerIgnoresEventsOutsideTheRunLifecycle(t *testing.T) {
	tr := newSubagentTracker()
	ev := events.Event{Kind: events.KindStep, Detail: "thinking"}.WithAgentAttribution("t1", "audit", 1)

	if tr.Apply(ev, time.Now()) {
		t.Fatal("an unmodelled kind must not register as a state change")
	}
	if len(tr.Rows()) != 0 {
		t.Fatalf("it opened a row that no done event would ever close: %+v", tr.Rows())
	}
}

func TestIntegrationNowPanelDropsFinishedTool(t *testing.T) {
	// The tool half of the same invariant, through the bridge drain.
	m := newReadyChatModel(40, 100)
	m.waiting = true
	m.turnStart = time.Now()
	now := time.Now()

	m.updateFromDrain(BridgeDrain{Tools: []bridgeToolEvt{
		{Start: true, ToolCallID: "c1", Name: "grep", Detail: `{"q":"x"}`, At: now},
		{Start: true, ToolCallID: "c2", Name: "read_file", Detail: `{"path":"a"}`, At: now},
	}})
	if got := nowPanelText(m); !strings.Contains(got, "grep") || !strings.Contains(got, "read_file") {
		t.Fatalf("both open tools must show in now:\n%s", got)
	}

	m.updateFromDrain(BridgeDrain{Tools: []bridgeToolEvt{
		{ToolCallID: "c1", Name: "grep", Detail: "3 matches", At: now},
	}})

	got := nowPanelText(m)
	if strings.Contains(got, "grep") {
		t.Fatalf("finished tool must leave the now panel:\n%s", got)
	}
	if !strings.Contains(got, "read_file") {
		t.Fatalf("still-running tool must remain in the now panel:\n%s", got)
	}
	if len(m.toolRows) != 1 {
		t.Fatalf("finished tool must leave toolRows, got %d", len(m.toolRows))
	}
	// It moved to history rather than vanishing.
	var committed bool
	for _, b := range m.blocks {
		if b.Kind == ChatBlockTool && b.ToolName == "grep" {
			committed = true
		}
	}
	if !committed {
		t.Fatal("finished tool must commit to the transcript, not disappear")
	}
}

func TestIntegrationLivePanelDoesNotShrinkViewport(t *testing.T) {
	// The "now" panel is a paint-only overlay: it must not reserve a layout
	// band, so the transcript viewport keeps its full height while the panel
	// is visible. layout() (Update path) and View() must agree, idle or not.
	m := newReadyChatModel(40, 100)
	m.messages = []string{"one", "two", "three"}
	m.renderVP()
	m.layout()
	idleLayout := m.viewport.Height
	m.View()
	idleView := m.viewport.Height
	if idleLayout != idleView {
		t.Fatalf("idle: layout()=%d View()=%d - the two paths disagree", idleLayout, idleView)
	}
	if got := m.renderLivePanel(100, time.Now()); got != "" {
		t.Fatal("idle: renderLivePanel must be empty")
	}

	m.waiting = true
	m.turnStart = time.Now()
	m.toolRows = []toolRow{{Name: "run_command", Detail: `{"cmd":"go test"}`, Status: "running", Start: time.Now()}}
	m.streamBuf.WriteString("streaming answer")
	m.subagents.Apply(events.Event{Kind: events.KindSubagentStart, Name: "grep"}.
		WithAgentAttribution("t1", "audit", 1), time.Now())
	if m.livePanelHeight() == 0 {
		t.Fatal("precondition: the panel must be visible while waiting")
	}
	m.layout()
	waitLayout := m.viewport.Height
	m.View()
	waitView := m.viewport.Height
	if waitLayout != idleLayout {
		t.Fatalf("layout() viewport shrank while the panel is visible: idle=%d waiting=%d", idleLayout, waitLayout)
	}
	if waitView != idleView {
		t.Fatalf("View() viewport shrank while the panel is visible: idle=%d waiting=%d", idleView, waitView)
	}

	// The waiting frame paints the overlay top-aligned on the row below the
	// one-line status header, while the transcript viewport keeps its full
	// height.
	plain := strings.Split(stripANSI(m.View()), "\n")
	if !strings.Contains(plain[1], " now · ") {
		t.Fatalf("overlay header missing on the row below the status header:\n%s", strings.Join(plain[:4], "\n"))
	}
}
