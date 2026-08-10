package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// Stable Stage: the viewport holds immutable history only; everything alive
// renders in a paint-only overlay over the transcript top. The overlay holds
// no layout band, so the transcript never changes height while the agent
// works - that motion was the scroll jumping.

func TestLivePanelBandFormula(t *testing.T) {
	// The "now" panel is a paint-only overlay for the whole turn. Its height
	// is min(livePanelMaxHeight, max(1, termH/3)) with termH floored at 8,
	// the same budget the sections use. Idle is zero. The rendered line count
	// always equals the declared height, even when the overlay is empty.
	for _, h := range []int{8, 11, 12, 20, 24, 30, 34, 40, 44, 60} {
		band := min(livePanelMaxHeight, max(1, max(8, h)/3))

		// Busy content while waiting: every section populated.
		m := newReadyChatModel(h, 90)
		m.waiting = true
		m.turnStart = time.Now()
		m.subagents.Apply(events.Event{Kind: events.KindSubagentStart, Name: "grep"}.
			WithAgentAttribution("t1", "audit", 1), time.Now())
		m.toolRows = []toolRow{{Name: "run_command", Detail: `{"cmd":"go test"}`, Status: "running", Start: time.Now()}}
		m.thinkingBuf.WriteString("weighing the budget change")
		m.streamBuf.WriteString("The timeout was the outer deadline firing early.")
		if got := m.livePanelHeight(); got != band {
			t.Fatalf("height=%d: busy livePanelHeight=%d, want band %d", h, got, band)
		}
		panel := stripANSI(m.renderLivePanel(90, time.Now()))
		if got := strings.Count(panel, "\n") + 1; got != band {
			t.Fatalf("height=%d: busy render is %d lines, declared %d", h, got, band)
		}
		// Top-aligned priority order survives inside the overlay: the fleet row
		// stays visible as soon as the overlay has a content row, and the stream
		// tail once the budget can hold it.
		if band >= 4 && !strings.Contains(panel, "audit") {
			t.Fatalf("height=%d: fleet content must stay visible:\n%s", h, panel)
		}
		if band >= 6 && !strings.Contains(panel, "outer deadline") {
			t.Fatalf("height=%d: stream content must stay visible:\n%s", h, panel)
		}

		// Empty overlay while waiting: still the full height (blank rows), so
		// the panel never disappears mid-turn.
		m2 := newReadyChatModel(h, 90)
		m2.waiting = true
		m2.turnStart = time.Now()
		if got := m2.livePanelHeight(); got != band {
			t.Fatalf("height=%d: empty livePanelHeight=%d, want band %d", h, got, band)
		}
		panel = stripANSI(m2.renderLivePanel(90, time.Now()))
		if got := strings.Count(panel, "\n") + 1; got != band {
			t.Fatalf("height=%d: empty render is %d lines, declared %d", h, got, band)
		}

		// Idle: no band at all.
		m3 := newReadyChatModel(h, 90)
		m3.waiting = false
		if got := m3.livePanelHeight(); got != 0 {
			t.Fatalf("height=%d: idle livePanelHeight=%d, want 0", h, got)
		}
		if got := m3.renderLivePanel(90, time.Now()); got != "" {
			t.Fatalf("height=%d: idle render must be empty", h)
		}
	}
}

func TestLivePanelHeightConstantDuringTurn(t *testing.T) {
	// The core fix: the band appears once at turn start and disappears once
	// at turn end. While waiting, livePanelHeight() and the rendered line
	// count stay the same as sections come and go.
	m := newReadyChatModel(34, 90)
	m.waiting = true
	m.turnStart = time.Now()
	band := min(livePanelMaxHeight, max(1, max(8, m.height)/3))
	if got := m.livePanelHeight(); got != band {
		t.Fatalf("band=%d, want %d", got, band)
	}
	assertBand := func(step string) {
		t.Helper()
		if got := m.livePanelHeight(); got != band {
			t.Fatalf("%s: livePanelHeight=%d changed from %d", step, got, band)
		}
		panel := stripANSI(m.renderLivePanel(90, time.Now()))
		if got := strings.Count(panel, "\n") + 1; got != band {
			t.Fatalf("%s: rendered %d lines, declared %d", step, got, band)
		}
	}
	now := time.Now()

	assertBand("empty")
	m.subagents.Apply(events.Event{Kind: events.KindSubagentStart, Name: "grep"}.
		WithAgentAttribution("t1", "audit", 1), now)
	assertBand("fleet")
	m.toolRows = []toolRow{{Name: "run_command", Detail: `{"cmd":"go test"}`, Status: "running", Start: now}}
	assertBand("tools")
	m.streamBuf.WriteString("partial answer streaming in")
	assertBand("stream")
	m.toolRows = nil
	assertBand("tools finished")
	m.subagents.Apply(events.Event{Kind: events.KindSubagentDone}.
		WithAgentAttribution("t1", "audit", 1), now)
	assertBand("fleet finished")
	m.waiting = false
	if got := m.livePanelHeight(); got != 0 {
		t.Fatalf("idle livePanelHeight=%d, want 0", got)
	}
	if got := m.renderLivePanel(90, time.Now()); got != "" {
		t.Fatal("idle render must be empty")
	}
}

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
	// At height 34 the band is capped at livePanelMaxHeight (6), so the
	// content budget inside the band is band-2 = 4 rows. All four sections
	// cannot fit at once: fleet+tools+stream survive and thinking drops
	// first (sections shrink bottom-up, thinking is the lowest priority).
	m := newReadyChatModel(34, 90)
	m.waiting = true
	m.turnStart = time.Now()
	m.subagents.Apply(events.Event{Kind: events.KindSubagentStart, Name: "grep"}.
		WithAgentAttribution("t1", "audit", 1), time.Now())
	m.toolRows = []toolRow{{Name: "run_command", Detail: `{"cmd":"go test"}`, Status: "running", Start: time.Now()}}
	m.thinkingBuf.WriteString("weighing the budget change")
	m.streamBuf.WriteString("The timeout was the outer deadline firing early.")

	panel := stripANSI(m.renderLivePanel(90, time.Now()))
	for _, want := range []string{"audit", "run_command", "outer deadline"} {
		if !strings.Contains(panel, want) {
			t.Fatalf("live panel missing %q:\n%s", want, panel)
		}
	}
	if strings.Contains(panel, "▾ thinking") {
		t.Fatalf("thinking must drop first under the 4-row content budget:\n%s", panel)
	}
	// Declared height must equal rendered height - both layout paths use it.
	if got := strings.Count(panel, "\n") + 1; got != m.livePanelHeight() {
		t.Fatalf("rendered %d lines, declared %d:\n%s", got, m.livePanelHeight(), panel)
	}
}

func TestLivePanelFitsThinkingWhenStreamAbsent(t *testing.T) {
	// Without stream content the four content rows hold fleet+tools+thinking
	// (1+1+2) exactly, so thinking renders after tools instead of dropping.
	m := newReadyChatModel(34, 90)
	m.waiting = true
	m.turnStart = time.Now()
	m.subagents.Apply(events.Event{Kind: events.KindSubagentStart, Name: "grep"}.
		WithAgentAttribution("t1", "audit", 1), time.Now())
	m.toolRows = []toolRow{{Name: "run_command", Detail: `{"cmd":"go test"}`, Status: "running", Start: time.Now()}}
	m.thinkingBuf.WriteString("weighing the budget change")

	panel := stripANSI(m.renderLivePanel(90, time.Now()))
	pos := func(marker string) int {
		t.Helper()
		idx := strings.Index(panel, marker)
		if idx < 0 {
			t.Fatalf("marker %q missing from the overlay:\n%s", marker, panel)
		}
		return idx
	}
	fleet, tools, thinking := pos("audit"), pos("run_command"), pos("▾ thinking")
	if !(fleet < tools && tools < thinking) {
		t.Fatalf("order must be fleet < tools < thinking, got %d < %d < %d:\n%s",
			fleet, tools, thinking, panel)
	}
	if got := strings.Count(panel, "\n") + 1; got != m.livePanelHeight() {
		t.Fatalf("rendered %d lines, declared %d", got, m.livePanelHeight())
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

func TestLivePanelPriorityOrderAtCapSix(t *testing.T) {
	// At termH=24 the band formula reaches the cap boundary exactly
	// (24/3 == 8 >= livePanelMaxHeight == 6). With every section populated
	// the overlay renders its full 6 rows and the priority order holds from
	// top to bottom: fleet < tools < stream, with thinking dropped first
	// (5 rows of content do not fit the band-2 = 4 content budget).
	m := newReadyChatModel(24, 90)
	m.waiting = true
	m.turnStart = time.Now()
	m.subagents.Apply(events.Event{Kind: events.KindSubagentStart, Name: "grep"}.
		WithAgentAttribution("t1", "audit", 1), time.Now())
	m.toolRows = []toolRow{{Name: "run_command", Detail: `{"cmd":"go test"}`, Status: "running", Start: time.Now()}}
	m.thinkingBuf.WriteString("weighing the budget change")
	m.streamBuf.WriteString("The timeout was the outer deadline firing early.")

	if got := m.livePanelHeight(); got != livePanelMaxHeight {
		t.Fatalf("livePanelHeight=%d, want cap %d at termH=24", got, livePanelMaxHeight)
	}
	panel := stripANSI(m.renderLivePanel(90, time.Now()))
	if got := strings.Count(panel, "\n") + 1; got != livePanelMaxHeight {
		t.Fatalf("rendered %d lines, want %d", got, livePanelMaxHeight)
	}
	if strings.Contains(panel, "▾ thinking") {
		t.Fatalf("thinking must drop first under the 4-row content budget:\n%s", panel)
	}
	pos := func(marker string) int {
		t.Helper()
		idx := strings.Index(panel, marker)
		if idx < 0 {
			t.Fatalf("marker %q missing from the overlay:\n%s", marker, panel)
		}
		return idx
	}
	fleet, tools, stream := pos("audit"), pos("run_command"), pos("outer deadline")
	if !(fleet < tools && tools < stream) {
		t.Fatalf("surviving order must be fleet < tools < stream, got %d < %d < %d:\n%s",
			fleet, tools, stream, panel)
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
