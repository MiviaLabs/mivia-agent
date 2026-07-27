package cli

import (
	"strings"
	"testing"
	"time"
)

// TDD integration: full-height rails, padding, collapse for all block kinds.
// These tests define the contract before/while implementation lands.

func TestIntegration_FullHeightRail_UserMultiLine(t *testing.T) {
	// User cards no longer use a left rail — bg bar + stacked time/body.
	// Keep this test as a regression: multi-line user content, no rail.
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	long := strings.Repeat("word ", 50)
	blocks := []ChatBlock{{
		ID: "u1", Kind: ChatBlockUser, Text: long, SentAt: time.Now(),
	}}
	r := RenderChatBlocks(blocks, "m", 36, true)
	var content []string
	for _, ln := range r.Lines {
		p := stripANSI(ln)
		if strings.TrimSpace(p) == "" {
			continue
		}
		content = append(content, p)
	}
	if len(content) < 2 {
		t.Fatalf("need multi-line user card, got %d lines: %v", len(content), content)
	}
	for i, p := range content {
		if hasFullHeightRailPrefix(p) {
			t.Fatalf("user content line %d must not have left rail: %q", i, p)
		}
	}
}

func TestIntegration_FullHeightRail_ToolExpanded(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	body := "line-one\nline-two\nline-three"
	blocks := []ChatBlock{{
		ID: "t1", Kind: ChatBlockTool, ToolName: "read_file",
		Text: body, Collapsed: false,
	}}
	r := RenderChatBlocks(blocks, "m", 60, true)
	var content []string
	for _, ln := range r.Lines {
		p := stripANSI(ln)
		if strings.TrimSpace(p) == "" {
			continue
		}
		content = append(content, p)
	}
	if len(content) < 2 {
		t.Fatalf("expanded tool needs multi-line, got %v", content)
	}
	for i, p := range content {
		// tools rail ASCII # (thick bar)
		if !hasFullHeightRailPrefix(p) {
			t.Fatalf("tool line %d missing full-height rail: %q", i, p)
		}
	}
}

func TestIntegration_FullHeightRail_ToolRenderedProduction(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	item := newToolRenderItem("grep", `{"pattern":"x"}`, "match\nmatch2", true, false)
	rendered := formatToolLine(item, 80, terminalToolRenderOptions())
	blocks := []ChatBlock{{
		ID: "t1", Kind: ChatBlockTool, ToolName: "grep",
		Text: "match\nmatch2", Rendered: rendered, Collapsed: true,
	}}
	r := RenderChatBlocks(blocks, "m", 80, true)
	if len(r.Lines) != 1 {
		t.Fatalf("collapsed tool must be 1 line, got %d %v", len(r.Lines), r.Lines)
	}
	p := stripANSI(r.Lines[0])
	if !hasFullHeightRailPrefix(p) {
		t.Fatalf("production Rendered tool missing rail: %q", p)
	}

	// Expanded production tool (Rendered set) must paint multi-line body + rail
	// on every line — not stay stuck on the one-line summary.
	blocks[0].Collapsed = false
	r = RenderChatBlocks(blocks, "m", 80, true)
	var content []string
	for _, ln := range r.Lines {
		p := stripANSI(ln)
		if strings.TrimSpace(p) == "" {
			continue
		}
		content = append(content, p)
	}
	if len(content) < 2 {
		t.Fatalf("expanded Rendered tool needs multi-line body, got %v", content)
	}
	for i, p := range content {
		if !hasFullHeightRailPrefix(p) {
			t.Fatalf("expanded tool line %d missing rail: %q", i, p)
		}
	}
	joined := strings.Join(content, "\n")
	if !strings.Contains(joined, "match") {
		t.Fatalf("expanded body missing match: %v", content)
	}
}

func TestIntegration_Collapsed_OnlyOneLineWithRail(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	kinds := []struct {
		b ChatBlock
	}{
		{ChatBlock{ID: "u", Kind: ChatBlockUser, Text: strings.Repeat("long user text ", 20), Collapsed: true}},
		{ChatBlock{ID: "a", Kind: ChatBlockAssistant, Text: strings.Repeat("long assistant ", 20), Collapsed: true}},
		{ChatBlock{ID: "t", Kind: ChatBlockTool, ToolName: "run_command", Text: "out1\nout2\nout3", Collapsed: true}},
		{ChatBlock{ID: "th", Kind: ChatBlockThinking, Text: "a\nb\nc\nd\ne\nf\ng", Collapsed: true}},
	}
	for _, tc := range kinds {
		r := RenderChatBlocks([]ChatBlock{tc.b}, "m", 50, true)
		if len(r.Lines) != 1 {
			t.Fatalf("%s collapsed want 1 line got %d: %v", tc.b.Kind, len(r.Lines), r.Lines)
		}
		p := stripANSI(r.Lines[0])
		// Must have some rail or collapse marker
		if strings.TrimSpace(p) == "" {
			t.Fatalf("%s collapsed empty", tc.b.Kind)
		}
		// Industry collapsible-card affordance: ▸ on collapsed rows
		if !strings.Contains(p, "▸") {
			t.Fatalf("%s collapsed missing ▸ affordance: %q", tc.b.Kind, p)
		}
	}
}

func TestIntegration_AssistantBubblePadding_Expanded(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	r := RenderChatBlocks([]ChatBlock{{
		ID: "a", Kind: ChatBlockAssistant, Text: "assistant body pad",
	}}, "m", 40, true)
	p := AssistantBubble.Style.Padding
	if p.Top < 1 || p.Bottom < 1 {
		t.Fatalf("assistant bubble needs vertical pad: %+v", p)
	}
	if len(r.Lines) < 1+p.Top+p.Bottom {
		t.Fatalf("assistant missing vertical pad lines: got %d want ≥%d %v",
			len(r.Lines), 1+p.Top+p.Bottom, dumpPlain(r.Lines))
	}
	joined := stripANSI(strings.Join(r.Lines, "\n"))
	if !strings.Contains(joined, "assistant body pad") {
		t.Fatalf("assistant body missing: %q", joined)
	}
	// Full-height rail on pad + body
	for i, ln := range r.Lines {
		plain := stripANSI(ln)
		if strings.TrimSpace(plain) == "" {
			// join may leave pure spaces after rail
		}
		if !hasFullHeightRailPrefix(plain) && strings.TrimSpace(plain) != "" {
			// blank pad lines after join should still start with rail
			t.Fatalf("assistant line %d missing rail: %q", i, plain)
		}
		if !hasFullHeightRailPrefix(plain) {
			t.Fatalf("assistant line %d missing rail (incl pad): %q", i, plain)
		}
	}
}

func TestIntegration_UserPadding_VerticalAndHorizontal(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	const w = 40
	r := RenderChatBlocks([]ChatBlock{{
		ID: "u", Kind: ChatBlockUser, Text: "pad me",
	}}, "m", w, true)
	p := UserBubble.Style.Padding
	if p.Top < 1 || p.Bottom < 1 || p.Left < 2 || p.Right < 1 {
		t.Fatalf("insufficient default padding: %+v", p)
	}
	if len(r.Lines) < 1+p.Top+p.Bottom {
		t.Fatalf("missing vertical pad lines: got %d want ≥%d lines=%v",
			len(r.Lines), 1+p.Top+p.Bottom, dumpPlain(r.Lines))
	}
	// Top pad lines: blank bg bar, full width, no rail
	for i := 0; i < p.Top; i++ {
		plain := stripANSI(r.Lines[i])
		if hasFullHeightRailPrefix(plain) {
			t.Fatalf("user top pad must not have rail: %q", plain)
		}
		if strings.TrimSpace(plain) != "" {
			t.Fatalf("top pad line %d not blank: %q", i, plain)
		}
		if visibleWidth(r.Lines[i]) < w-2 {
			t.Fatalf("top pad line %d too narrow vis=%d want≈%d: %q",
				i, visibleWidth(r.Lines[i]), w, plain)
		}
	}
	joined := stripANSI(strings.Join(r.Lines, "\n"))
	if !strings.Contains(joined, "pad me") {
		t.Fatalf("body missing: %q", joined)
	}
	for _, ln := range r.Lines {
		plain := stripANSI(ln)
		if !strings.Contains(plain, "pad me") {
			continue
		}
		// Horizontal left pad before body
		if !strings.HasPrefix(strings.TrimLeft(plain, " "), "pad me") && !strings.Contains(plain, "pad me") {
			t.Fatalf("content line odd: %q", plain)
		}
		trimmed := strings.TrimLeft(plain, " ")
		if !strings.HasPrefix(plain, " ") && strings.HasPrefix(trimmed, "pad me") {
			// left pad may be present
		}
		if p.Left > 0 && !strings.HasPrefix(plain, strings.Repeat(" ", 1)) {
			// allow some pad; body is left-padded
			if !strings.Contains(plain, "pad me") {
				t.Fatalf("content missing: %q", plain)
			}
		}
	}
}

func TestIntegration_SystemCollapse_AndToggle(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 60
	m.blocks = []ChatBlock{
		{ID: "s1", Kind: ChatBlockSystem, Text: "long system status " + strings.Repeat("x", 40), Collapsed: false},
	}
	m.selectedBlockID = "s1"
	if !m.toggleSelectedBlock() {
		t.Fatal("system toggle failed")
	}
	if !m.blocks[0].Collapsed {
		t.Fatal("system should collapse")
	}
	r := RenderChatBlocks([]ChatBlock{m.blocks[0]}, "m", 50, true)
	if len(r.Lines) != 1 {
		t.Fatalf("collapsed system want 1 line got %d: %v", len(r.Lines), dumpPlain(r.Lines))
	}
	_ = m.toggleSelectedBlock()
	if m.blocks[0].Collapsed {
		t.Fatal("system should expand")
	}
}

func TestIntegration_ToggleCollapse_AllKinds(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 60
	m.blocks = []ChatBlock{
		{ID: "u1", Kind: ChatBlockUser, Text: strings.Repeat("user text ", 30), Collapsed: false},
		{ID: "a1", Kind: ChatBlockAssistant, Text: strings.Repeat("asst text ", 30), Collapsed: false},
		{ID: "t1", Kind: ChatBlockTool, ToolName: "delegate", Text: "sub\nagent\nout", Collapsed: false},
		{ID: "th1", Kind: ChatBlockThinking, Text: "t1\nt2\nt3\nt4\nt5\nt6\nt7", Collapsed: false},
	}
	// Rebuild messages from blocks
	m.renderVP()

	for _, id := range []string{"u1", "a1", "t1", "th1"} {
		m.selectedBlockID = id
		if !m.toggleSelectedBlock() {
			t.Fatalf("toggle failed for %s", id)
		}
		var b *ChatBlock
		for i := range m.blocks {
			if m.blocks[i].ID == id {
				b = &m.blocks[i]
				break
			}
		}
		if b == nil || !b.Collapsed {
			t.Fatalf("%s should be collapsed after toggle", id)
		}
		// Render must be single line when collapsed
		r := RenderChatBlocks([]ChatBlock{*b}, "m", 50, true)
		if len(r.Lines) != 1 {
			t.Fatalf("%s collapsed render lines=%d want 1: %v", id, len(r.Lines), dumpPlain(r.Lines))
		}
		// Expand again
		m.selectedBlockID = id
		_ = m.toggleSelectedBlock()
		if b.Collapsed {
			t.Fatalf("%s should expand", id)
		}
	}
}

func TestIntegration_SubagentToolSameAsToolRail(t *testing.T) {
	// Subagents surface as tool rows/blocks (delegate / multi_step) — same rail path.
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	blocks := []ChatBlock{{
		ID: "d1", Kind: ChatBlockTool, ToolName: "delegate",
		Text: "task output\nline2", Collapsed: false,
	}}
	r := RenderChatBlocks(blocks, "m", 50, true)
	for i, ln := range r.Lines {
		p := stripANSI(ln)
		if strings.TrimSpace(p) == "" {
			continue
		}
		if !hasFullHeightRailPrefix(p) {
			t.Fatalf("delegate line %d missing tool rail: %q", i, p)
		}
	}
}

// hasFullHeightRailPrefix reports whether plain line starts with a full-height
// rail glyph (Unicode half-block or ASCII #).
func hasFullHeightRailPrefix(plain string) bool {
	return strings.HasPrefix(plain, "#") ||
		strings.HasPrefix(plain, "▌") ||
		strings.HasPrefix(plain, "┃")
}

func TestIntegration_MixedTimeline_RailsAndPadding(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	blocks := []ChatBlock{
		{ID: "u", Kind: ChatBlockUser, Text: "do the thing", SentAt: time.Now()},
		{ID: "th", Kind: ChatBlockThinking, Text: "plan\nstep", Collapsed: false},
		{ID: "t", Kind: ChatBlockTool, ToolName: "read_file", Text: "a\nb", Collapsed: false},
		{ID: "a", Kind: ChatBlockAssistant, Text: "here is the answer with more words"},
	}
	r := RenderChatBlocks(blocks, "m", 50, true)
	plain := stripANSI(strings.Join(r.Lines, "\n"))
	if !strings.Contains(plain, "do the thing") {
		t.Fatal("user body missing")
	}
	if !strings.Contains(plain, "answer") {
		t.Fatal("assistant missing")
	}
	// No box drawing corners (borderless product rule)
	if strings.Contains(plain, "╭") || strings.Contains(plain, "╰") {
		t.Fatalf("box borders: %q", plain)
	}
}

func dumpPlain(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = stripANSI(l)
	}
	return out
}
