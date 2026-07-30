package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// The detail overlay is the doorway every truncation leads to: a full-screen
// scrollable pager over a block's complete content. No 50-line dead ends.

func overlayTestBlock(lines int) ChatBlock {
	var b strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, "line-%03d\n", i)
	}
	return ChatBlock{
		ID: "turn-1-block-2", Kind: ChatBlockTool, ToolName: "run_command",
		AgentName: "audit", Text: strings.TrimRight(b.String(), "\n"),
		Elapsed: 2 * time.Second,
	}
}

func TestOverlayShowsFullContentBeyondInlineCap(t *testing.T) {
	o := newBlockOverlay(overlayTestBlock(120))
	if got := len(o.lines); got < 120 {
		t.Fatalf("overlay truncated content: %d lines", got)
	}
	view := stripANSI(o.View(80, 24))
	if !strings.Contains(view, "run_command") {
		t.Fatalf("overlay header missing tool name:\n%s", view)
	}
	if !strings.Contains(view, "audit") {
		t.Fatalf("overlay header missing agent:\n%s", view)
	}
	if !strings.Contains(view, "line-000") {
		t.Fatalf("overlay must start at the top:\n%s", view)
	}
}

func TestOverlayScrollAndClamp(t *testing.T) {
	o := newBlockOverlay(overlayTestBlock(100))
	h := 20
	o.scroll(5, h)
	v := stripANSI(o.View(80, h))
	if strings.Contains(v, "line-000") {
		t.Fatalf("scroll did not move content:\n%s", v)
	}
	o.scroll(-1000, h)
	if v := stripANSI(o.View(80, h)); !strings.Contains(v, "line-000") {
		t.Fatalf("scroll up must clamp to top:\n%s", v)
	}
	o.scroll(100000, h)
	if v := stripANSI(o.View(80, h)); !strings.Contains(v, "line-099") {
		t.Fatalf("scroll down must clamp to bottom:\n%s", v)
	}
}

func TestOverlayRedactsSecrets(t *testing.T) {
	// Same privacy rule as inline expansion: the overlay routes content
	// through the configured redaction policy (INV-TUI-7: policy-driven,
	// never compiled-in patterns).
	policy, err := redact.Compile([]string{`sk-live-[A-Za-z0-9]+`}, nil, "[redacted]")
	if err != nil {
		t.Fatal(err)
	}
	previous := redact.Current()
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(previous) })

	block := ChatBlock{Kind: ChatBlockTool, ToolName: "run_command",
		Text: `export API_KEY=sk-live-abc123def456`}
	o := newBlockOverlay(block)
	view := stripANSI(o.View(100, 20))
	if strings.Contains(view, "sk-live-abc123def456") {
		t.Fatalf("overlay leaked a secret:\n%s", view)
	}
	if !strings.Contains(view, "[redacted]") {
		t.Fatalf("overlay did not apply the policy:\n%s", view)
	}
}

func TestOverlayKeyFlow(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.blocks = []ChatBlock{overlayTestBlock(80)}
	m.renderVP()
	m.View()
	m.focus = focusScrollback
	m.selectedBlockID = "turn-1-block-2"

	// 'o' on a selected block opens the overlay.
	skipTA, _, _ := m.handleChatKey("o", false)
	if m.overlay == nil {
		t.Fatal("'o' must open the detail overlay")
	}
	if !skipTA {
		t.Fatal("'o' must not leak into the composer")
	}
	// View renders the overlay, not the transcript chrome.
	view := stripANSI(m.View())
	if !strings.Contains(view, "run_command") || !strings.Contains(view, "line-000") {
		t.Fatalf("overlay not rendered:\n%s", view)
	}

	// j scrolls; esc closes.
	m.handleChatKey("j", false)
	if m.overlay.yOffset == 0 {
		t.Fatal("j must scroll the overlay")
	}
	m.handleChatKey("esc", false)
	if m.overlay != nil {
		t.Fatal("esc must close the overlay")
	}

	// 'o' with composer focus stays typable (no hijacking while writing).
	m.focus = focusComposer
	skipTA, _, _ = m.handleChatKey("o", false)
	if skipTA || m.overlay != nil {
		t.Fatal("'o' while composing must reach the textarea")
	}
}
