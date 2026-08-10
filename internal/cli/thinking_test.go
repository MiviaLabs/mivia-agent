package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/charmbracelet/lipgloss"
)

// Chain of thought: providers already parse ReasoningContent, but nothing
// consumed it - the reasoning was captured and thrown away. It now reaches
// the live panel as a rolling window and history as a summarised block.

func TestThinkingEventReachesBridge(t *testing.T) {
	b := newStreamBridge()
	cb := agentEventBridgeCallback(b)
	cb(agent.Event{Kind: agent.EventThinking, Content: "weighing the budget change"})
	d := b.Drain()
	if !strings.Contains(d.Thinking, "weighing the budget change") {
		t.Fatalf("thinking event never reached the bridge: %q", d.Thinking)
	}
}

func TestLivePanelShowsRollingThinkingWindow(t *testing.T) {
	m := newReadyChatModel(34, 90)
	m.waiting = true
	m.turnStart = time.Now()
	for i := 0; i < 12; i++ {
		m.thinkingBuf.WriteString("reasoning line " + string(rune('a'+i)) + "\n")
	}
	panel := stripANSI(m.renderLivePanel(90, time.Now()))

	// The most recent lines are shown, not just one, and not all twelve.
	shown := 0
	for i := 0; i < 12; i++ {
		if strings.Contains(panel, "reasoning line "+string(rune('a'+i))) {
			shown++
		}
	}
	if shown < 2 || shown > liveMaxThinkingRows {
		t.Fatalf("want a rolling window of up to %d lines, showed %d:\n%s", liveMaxThinkingRows, shown, panel)
	}
	// It is the TAIL that survives - the model's latest thought.
	if !strings.Contains(panel, "reasoning line l") {
		t.Fatalf("newest reasoning line missing:\n%s", panel)
	}
	if strings.Contains(panel, "reasoning line a") {
		t.Fatalf("oldest line should have scrolled out:\n%s", panel)
	}
}

func TestCollapsedThinkingBlockSummarises(t *testing.T) {
	// After the turn, thinking folds away - but the fold should say what it
	// is hiding, not just "thinking".
	text := strings.Repeat("a line of reasoning\n", 20)
	lines := renderThinkingBlock(strings.TrimRight(text, "\n"), true, 0, false)
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "thinking") {
		t.Fatalf("collapsed thinking lost its label: %q", plain)
	}
	if !strings.Contains(plain, "20 lines") {
		t.Fatalf("collapsed thinking should summarise its size: %q", plain)
	}
}

func TestThinkingUsesBrandPhaseColor(t *testing.T) {
	// Thinking is cyan everywhere (the brand's thinking phase); it used to be
	// magenta here and cyan in the status bar for the same state. Asserted on
	// the style, since lipgloss drops color in a non-TTY test process.
	if got := tuiThinkingStyle.GetForeground(); string(got.(lipgloss.Color)) != brandColorThinking {
		t.Fatalf("thinking chrome color = %v, want brand thinking %q", got, brandColorThinking)
	}
	if got := thinkingLiveStyle.GetForeground(); string(got.(lipgloss.Color)) != brandColorThinking {
		t.Fatalf("live thinking color = %v, want brand thinking %q", got, brandColorThinking)
	}
}

func TestNewTUIModelThinkingCollapsedByDefault(t *testing.T) {
	// Committed thinking blocks must collapse by default: the live panel
	// shows the rolling tail during the turn, and history shows the summary.
	m := newReadyChatModel(40, 90)
	if m.thinkingExpandDefault {
		t.Fatal("new TUI model must default committed thinking to collapsed")
	}
}

func TestFlushThinkingCommitsCollapsedSummaryOnly(t *testing.T) {
	// flushThinkingToHistory commits the live thinking as a collapsed block:
	// the render shows only the summary label ('thinking · N lines'), never
	// the body text.
	m := newSmokeModel(t)
	m.mode = modeChat
	m.thinkingBuf.WriteString("reasoning line one\nreasoning line two")
	m.flushThinkingToHistory()
	if len(m.blocks) != 1 {
		t.Fatalf("expected 1 committed thinking block, got %d", len(m.blocks))
	}
	b := m.blocks[0]
	if b.Kind != ChatBlockThinking {
		t.Fatalf("kind=%s, want thinking", b.Kind)
	}
	if !b.Collapsed {
		t.Fatal("committed thinking must be collapsed by default")
	}
	out := stripANSI(strings.Join(RenderChatBlocksWithWorkGroups([]ChatBlock{b}, "m", 90, m.thinkingExpandDefault, nil).Lines, "\n"))
	if !strings.Contains(out, "thinking · 2 lines") {
		t.Fatalf("collapsed summary label missing: %q", out)
	}
	if strings.Contains(out, "reasoning line one") {
		t.Fatalf("collapsed thinking must hide the body: %q", out)
	}
}

func TestFlushThinkingEmptyAppendsNothing(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.flushThinkingToHistory()
	if len(m.blocks) != 0 {
		t.Fatalf("empty thinking buffer must commit nothing, got %d blocks", len(m.blocks))
	}
	// A committed empty-text thinking block still renders a bare summary.
	out := stripANSI(strings.Join(renderThinkingBlock("", false, 0, false), "\n"))
	if !strings.Contains(out, "thinking") {
		t.Fatalf("empty thinking block must render a bare summary: %q", out)
	}
}

func TestCtrlTTogglesGlobalDefaultAndSelectedBlock(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	if m.thinkingExpandDefault {
		t.Fatal("precondition: a new model collapses committed thinking")
	}
	m.blocks = []ChatBlock{{ID: "th-1", Kind: ChatBlockThinking, Text: "reasoning", Collapsed: true}}
	m.selectedBlockID = "th-1"
	m.focus = focusScrollback
	m.renderVP()

	// ctrl+t flips the global default false → true and expands the selected block.
	m.handleChatToggleKey("ctrl+t")
	if !m.thinkingExpandDefault {
		t.Fatal("ctrl+t must flip the global default to true")
	}
	if m.blocks[0].Collapsed {
		t.Fatal("ctrl+t must expand the selected thinking block")
	}

	// A second ctrl+t flips back and collapses the selected block.
	m.handleChatToggleKey("ctrl+t")
	if m.thinkingExpandDefault {
		t.Fatal("second ctrl+t must flip the global default back to false")
	}
	if !m.blocks[0].Collapsed {
		t.Fatal("second ctrl+t must collapse the selected thinking block")
	}
}

func TestEnterAndSpaceToggleCommittedThinkingBlock(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.blocks = []ChatBlock{{ID: "th-1", Kind: ChatBlockThinking, Text: "reasoning", Collapsed: true}}
	m.selectedBlockID = "th-1"
	m.focus = focusScrollback
	m.renderVP()

	// Enter expands a collapsed committed thinking block.
	m.handleChatKey("enter", false)
	if m.blocks[0].Collapsed {
		t.Fatal("enter must expand the selected collapsed thinking block")
	}
	// Space collapses it again.
	m.handleChatKey(" ", false)
	if !m.blocks[0].Collapsed {
		t.Fatal("space must collapse the selected expanded thinking block")
	}
}
