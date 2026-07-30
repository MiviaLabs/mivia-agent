package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/charmbracelet/lipgloss"
)

// Chain of thought: providers already parse ReasoningContent, but nothing
// consumed it — the reasoning was captured and thrown away. It now reaches
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
	// It is the TAIL that survives — the model's latest thought.
	if !strings.Contains(panel, "reasoning line l") {
		t.Fatalf("newest reasoning line missing:\n%s", panel)
	}
	if strings.Contains(panel, "reasoning line a") {
		t.Fatalf("oldest line should have scrolled out:\n%s", panel)
	}
}

func TestCollapsedThinkingBlockSummarises(t *testing.T) {
	// After the turn, thinking folds away — but the fold should say what it
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
