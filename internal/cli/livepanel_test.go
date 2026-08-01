package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// Stable Stage: the viewport holds immutable history only; everything alive
// renders in a fixed panel above the composer. The transcript must not
// change height while the agent works - that motion was the scroll jumping.

func TestLivePanelHiddenWhenIdle(t *testing.T) {
	m := newReadyChatModel(30, 80)
	m.waiting = false
	if m.livePanelHeight() != 0 {
		t.Fatalf("idle live panel height=%d want 0", m.livePanelHeight())
	}
	if m.renderLivePanel(80, time.Now()) != "" {
		t.Fatal("idle live panel must render nothing")
	}
}

func TestLivePanelShowsAgentsToolsThinkingAndStream(t *testing.T) {
	m := newReadyChatModel(34, 90)
	m.waiting = true
	m.turnStart = time.Now()
	m.subagents.Apply(events.Event{Kind: events.KindSubagentStart, Name: "grep"}.
		WithAgentAttribution("t1", "audit", 1), time.Now())
	m.toolRows = []toolRow{{Name: "run_command", Detail: `{"cmd":"go test"}`, Status: "running", Start: time.Now()}}
	m.thinkingBuf.WriteString("weighing the budget change")
	m.streamBuf.WriteString("The timeout was the outer deadline firing early.")

	panel := stripANSI(m.renderLivePanel(90, time.Now()))
	for _, want := range []string{"audit", "run_command", "thinking", "outer deadline"} {
		if !strings.Contains(panel, want) {
			t.Fatalf("live panel missing %q:\n%s", want, panel)
		}
	}
	// Declared height must equal rendered height - both layout paths use it.
	if got := strings.Count(panel, "\n") + 1; got != m.livePanelHeight() {
		t.Fatalf("rendered %d lines, declared %d:\n%s", got, m.livePanelHeight(), panel)
	}
}

func TestLivePanelHeightBounded(t *testing.T) {
	m := newReadyChatModel(40, 90)
	m.waiting = true
	m.turnStart = time.Now()
	for i := 0; i < 12; i++ {
		m.subagents.Apply(events.Event{Kind: events.KindSubagentStart, Name: "grep"}.
			WithAgentAttribution(string(rune('a'+i)), "agent", 1), time.Now())
		m.toolRows = append(m.toolRows, toolRow{Name: "read_file", Start: time.Now()})
	}
	for i := 0; i < 200; i++ {
		m.streamBuf.WriteString("a long streaming line of assistant output\n")
	}
	if got := m.livePanelHeight(); got > livePanelMaxHeight {
		t.Fatalf("panel height %d exceeds cap %d", got, livePanelMaxHeight)
	}
	panel := stripANSI(m.renderLivePanel(90, time.Now()))
	if got := strings.Count(panel, "\n") + 1; got != m.livePanelHeight() {
		t.Fatalf("rendered %d lines, declared %d", got, m.livePanelHeight())
	}
}

func TestTranscriptHeightStableDuringTurn(t *testing.T) {
	// The core fix: viewport content must not grow as live state churns.
	m := newReadyChatModel(30, 80)
	m.blocks = []ChatBlock{
		{ID: "b1", Kind: ChatBlockUser, Text: "do the thing"},
	}
	m.waiting = true
	m.turnStart = time.Now()
	m.renderVP()
	before := m.viewport.TotalLineCount()

	// Simulate a busy turn: thinking, tools, streaming, agents.
	m.thinkingBuf.WriteString("thinking hard\nabout it")
	m.toolRows = []toolRow{{Name: "grep", Start: time.Now()}}
	m.streamBuf.WriteString("partial answer streaming in")
	m.subagents.Apply(events.Event{Kind: events.KindSubagentStart, Name: "grep"}.
		WithAgentAttribution("t1", "audit", 1), time.Now())
	m.renderVP()

	if got := m.viewport.TotalLineCount(); got != before {
		t.Fatalf("transcript grew during the turn (%d → %d): live state leaked into the viewport", before, got)
	}
}

func TestLayoutAndViewAgreeWithLivePanel(t *testing.T) {
	m := newReadyChatModel(34, 80)
	m.waiting = true
	m.turnStart = time.Now()
	m.toolRows = []toolRow{{Name: "grep", Start: time.Now()}}
	m.streamBuf.WriteString("streaming")
	m.messages = []string{"one"}
	m.layout()
	fromLayout := m.viewport.Height
	m.View()
	if fromView := m.viewport.Height; fromLayout != fromView {
		t.Fatalf("layout()=%d View()=%d with live panel visible", fromLayout, fromView)
	}
}

func TestViewFitsTerminalWithLivePanel(t *testing.T) {
	for _, h := range []int{12, 20, 30, 44} {
		m := newReadyChatModel(h, 80)
		m.waiting = true
		m.turnStart = time.Now()
		for i := 0; i < 6; i++ {
			m.toolRows = append(m.toolRows, toolRow{Name: "read_file", Start: time.Now()})
			m.subagents.Apply(events.Event{Kind: events.KindSubagentStart, Name: "x"}.
				WithAgentAttribution(string(rune('a'+i)), "agent", 1), time.Now())
		}
		m.streamBuf.WriteString(strings.Repeat("stream line\n", 40))
		var msgs []string
		for i := 0; i < 60; i++ {
			msgs = append(msgs, "history line")
		}
		m.messages = msgs
		m.renderVP()
		view := stripANSI(m.View())
		if got := strings.Count(view, "\n") + 1; got > max(8, h) {
			t.Fatalf("height=%d: view is %d lines\n%s", h, got, view)
		}
	}
}
