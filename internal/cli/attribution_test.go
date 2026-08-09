package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
)

// Attribution chain: agent.Event.Origin (stamped by the subagent handler)
// must survive forwarding, reach the event bus, land on tool rows, and feed
// the subagent tracker - so the UI can tell which agent did what.

func TestOnEventForMultiStepPreservesOrigin(t *testing.T) {
	origin := agent.EventOrigin{TaskID: "task-9", Agent: "audit", Depth: 1}
	var got []agent.Event
	fwd := OnEventForMultiStep(func(e agent.Event) { got = append(got, e) })

	fwd(agent.Event{Kind: agent.EventToolStart, Name: "grep", Origin: origin})
	fwd(agent.Event{Kind: agent.EventToolEnd, Name: "grep", Origin: origin})
	fwd(agent.Event{Kind: agent.EventStep, Detail: "step 2", Origin: origin})

	if len(got) != 3 {
		t.Fatalf("forwarded %d events, want 3", len(got))
	}
	for i, e := range got {
		if e.Origin != origin {
			t.Fatalf("event %d lost origin: %+v", i, e.Origin)
		}
	}
	if got[0].Kind != agent.EventSubagentStart || got[1].Kind != agent.EventSubagentEnd {
		t.Fatalf("kinds not converted: %v %v", got[0].Kind, got[1].Kind)
	}
}

func TestOnEventForMultiStepFeedsStepHeartbeatRegistry(t *testing.T) {
	controller.ResetStepHeartbeats()
	t.Cleanup(controller.ResetStepHeartbeats)

	fwd := OnEventForMultiStep(func(agent.Event) {})
	fwd(agent.Event{Kind: agent.EventSubagentHeartbeat, Origin: agent.EventOrigin{TaskID: "wft-abc"}})

	got, ok := controller.LastStepHeartbeat("wft-abc")
	if !ok {
		t.Fatalf("no step heartbeat recorded for wft-abc")
	}
	if time.Since(got) > time.Minute {
		t.Fatalf("step heartbeat not recent: %v", got)
	}

	fwd(agent.Event{Kind: agent.EventSubagentHeartbeat, Origin: agent.EventOrigin{}})
	if _, ok := controller.LastStepHeartbeat(""); ok {
		t.Fatal("empty task id must not record a step heartbeat")
	}
}

func TestEventWithAgentAttribution(t *testing.T) {
	ev := events.NewEventFromAgentParts(events.KindSubagentStart, "id", "grep", "d", "", "", "").
		WithAgentAttribution("task-1", "audit", 1)
	if ev.AgentTask != "task-1" || ev.AgentName != "audit" || ev.AgentDepth != 1 {
		t.Fatalf("attribution not applied: %+v", ev)
	}
}

func TestBridgeSubagentToolCarriesAgent(t *testing.T) {
	b := newStreamBridge()
	b.PushSubagentTool(true, "call-1", "audit", "grep", `{"pattern":"x"}`)
	d := b.Drain()
	if len(d.Tools) != 1 {
		t.Fatalf("tools drained: %d", len(d.Tools))
	}
	if d.Tools[0].Agent != "audit" {
		t.Fatalf("agent lost in bridge: %+v", d.Tools[0])
	}
	// Plain pushes stay unattributed.
	b.PushToolWithID(true, "call-2", "read_file", "{}")
	d = b.Drain()
	if d.Tools[0].Agent != "" {
		t.Fatalf("plain tool gained agent: %+v", d.Tools[0])
	}
}

func TestToolRowsCarryAgentFromDrain(t *testing.T) {
	m := newTUIModel(makeTestSession(), nil, true)
	m.mode = modeChat
	m.ready = true
	m.width, m.height = 80, 30
	m.waiting = true
	m.turnStart = time.Now()

	m.applyToolEventsOpts([]bridgeToolEvt{
		{Start: true, ToolCallID: "c1", Name: "grep", Detail: "{}", Agent: "audit", At: time.Now()},
		{Start: true, ToolCallID: "c2", Name: "read_file", Detail: "{}", At: time.Now()},
	}, false)

	if len(m.toolRows) != 2 {
		t.Fatalf("rows: %d", len(m.toolRows))
	}
	if m.toolRows[0].Agent != "audit" {
		t.Fatalf("row 0 lost agent: %+v", m.toolRows[0])
	}
	if m.toolRows[1].Agent != "" {
		t.Fatalf("row 1 gained agent: %+v", m.toolRows[1])
	}
}

func TestToolPanelRowShowsAgentBadge(t *testing.T) {
	rows := []toolRow{
		{ToolCallID: "c1", Name: "grep", Detail: "{}", Agent: "audit", Start: time.Now()},
	}
	st := toolPanelState{Selected: -1, ordered: orderToolIndices(rows)}
	out, _, _ := renderToolPanelWindow(rows, 100, time.Now(), st, 0, phaseTools, 8, 0, time.Second)
	plain := stripANSI(out)
	if !strings.Contains(plain, "audit") {
		t.Fatalf("panel row missing agent badge:\n%s", plain)
	}
	if !strings.Contains(plain, "◆") {
		t.Fatalf("panel row missing agent diamond:\n%s", plain)
	}
}

func TestSubagentTrackerAggregates(t *testing.T) {
	tr := newSubagentTracker()
	now := time.Now()

	start := events.Event{Kind: events.KindSubagentStart, Name: "grep"}.
		WithAgentAttribution("t1", "audit", 1)
	end := events.Event{Kind: events.KindSubagentEnd, Name: "grep"}.
		WithAgentAttribution("t1", "audit", 1)
	other := events.Event{Kind: events.KindSubagentStart, Name: "go_test"}.
		WithAgentAttribution("t2", "fix-tests", 1)

	if !tr.Apply(start, now) || !tr.Apply(other, now) || !tr.Apply(end, now.Add(time.Second)) {
		t.Fatal("attributed events must register")
	}
	rows := tr.Rows()
	if len(rows) != 2 {
		t.Fatalf("rows: %d", len(rows))
	}
	// Stable first-seen order.
	if rows[0].Name != "audit" || rows[1].Name != "fix-tests" {
		t.Fatalf("order: %+v", rows)
	}
	if rows[0].ToolsDone != 1 || rows[0].ToolsOpen != 0 {
		t.Fatalf("audit counts: %+v", rows[0])
	}
	if rows[1].ToolsOpen != 1 {
		t.Fatalf("fix-tests counts: %+v", rows[1])
	}
	if rows[0].LastTool != "grep" {
		t.Fatalf("last tool: %+v", rows[0])
	}

	// Unattributed events are ignored, not misfiled.
	if tr.Apply(events.Event{Kind: events.KindSubagentStart, Name: "x"}, now) {
		t.Fatal("unattributed event must not register")
	}

	tr.Reset()
	if len(tr.Rows()) != 0 {
		t.Fatal("reset must clear rows")
	}

	// Nil receiver is safe: models built without a tracker must not panic.
	var nilTr *subagentTracker
	if nilTr.Apply(start, now) || nilTr.Rows() != nil || nilTr.Active() != 0 {
		t.Fatal("nil tracker must be inert")
	}
	nilTr.Reset()
}

func TestApplyEventFeedsTrackerAndPrefixesDetail(t *testing.T) {
	m := newTUIModel(makeTestSession(), nil, true)
	m.mode = modeChat
	m.ready = true
	m.waiting = true

	ev := events.Event{Kind: events.KindSubagentStart, Name: "grep", Detail: "{}"}.
		WithAgentAttribution("t1", "audit", 1)
	m.applyEvent(ev)

	if got := m.subagents.Active(); got != 1 {
		t.Fatalf("tracker not fed: active=%d", got)
	}

	hb := events.Event{Kind: events.KindSubagentHeartbeat, Detail: "elapsed=30s steps=4"}.
		WithAgentAttribution("t1", "audit", 1)
	m.applyEvent(hb)
	if !strings.Contains(m.stepDetail, "audit") {
		t.Fatalf("heartbeat detail not attributed: %q", m.stepDetail)
	}
}

func TestToolBlockHistoryKeepsAgent(t *testing.T) {
	// When a nested tool row commits to history, the block keeps the agent
	// name so the transcript can render the ◆ badge.
	block := ChatBlock{Kind: ChatBlockTool, ToolName: "grep", Text: "3 hits", AgentName: "audit", Collapsed: true}
	lines := renderOneChatBlock(block, "m", 80, false)
	joined := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "audit") {
		t.Fatalf("collapsed tool line missing agent badge: %q", joined)
	}
}
