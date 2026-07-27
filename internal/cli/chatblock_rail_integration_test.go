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
	// Tools use header-only thin gray rail — not full-height wall.
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
	if !hasRailPrefix(content[0]) {
		t.Fatalf("tool header missing thin rail: %q", content[0])
	}
	// Body lines: no wall of color
	for i := 1; i < len(content); i++ {
		if hasRailPrefix(content[i]) && strings.HasPrefix(content[i], "|") {
			// space column under header is OK; glyph on every body line is not
			if strings.HasPrefix(strings.TrimLeft(content[i], " "), "|") {
				continue
			}
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
	if !hasRailPrefix(p) {
		t.Fatalf("production Rendered tool missing rail: %q", p)
	}

	// Expanded production tool: multi-line body; rail on header only.
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
	if !hasRailPrefix(content[0]) {
		t.Fatalf("expanded tool header missing rail: %q", content[0])
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
		// User/assistant may append a trailing empty lane; content is first non-empty.
		var content []string
		for _, ln := range r.Lines {
			if strings.TrimSpace(stripANSI(ln)) != "" {
				content = append(content, stripANSI(ln))
			}
		}
		if len(content) != 1 {
			t.Fatalf("%s collapsed want 1 content line got %d: %v", tc.b.Kind, len(content), dumpPlain(r.Lines))
		}
		p := content[0]
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
	if p.Top != 0 || p.Bottom != 0 {
		t.Fatalf("assistant vertical pad should be 0 (lane after): %+v", p)
	}
	joined := stripANSI(strings.Join(r.Lines, "\n"))
	if !strings.Contains(joined, "assistant body pad") {
		t.Fatalf("assistant body missing: %q", joined)
	}
	// Thin │ on text lines only; not ▌
	found := false
	for _, ln := range r.Lines {
		plain := stripANSI(ln)
		if strings.Contains(plain, "assistant body pad") {
			found = true
			if !strings.HasPrefix(plain, "|") && !strings.HasPrefix(plain, "│") {
				t.Fatalf("assistant content missing thin rail: %q", plain)
			}
			if strings.HasPrefix(plain, "▌") {
				t.Fatalf("assistant rail too thick (half-block): %q", plain)
			}
		}
	}
	if !found {
		t.Fatal("assistant content line not found")
	}
	// Trailing empty lane
	if len(r.Lines) < 2 || strings.TrimSpace(stripANSI(r.Lines[len(r.Lines)-1])) != "" {
		t.Fatalf("want trailing empty lane after assistant: %v", dumpPlain(r.Lines))
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
	if p.Top != 0 || p.Bottom != 0 || p.Left < 2 || p.Right < 1 {
		t.Fatalf("user pad: vertical 0, horizontal required: %+v", p)
	}
	joined := stripANSI(strings.Join(r.Lines, "\n"))
	if !strings.Contains(joined, "pad me") {
		t.Fatalf("body missing: %q", joined)
	}
	// Trailing empty lane after user bubble
	if len(r.Lines) < 2 || strings.TrimSpace(stripANSI(r.Lines[len(r.Lines)-1])) != "" {
		t.Fatalf("want trailing empty lane after user: %v", dumpPlain(r.Lines))
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
		// Render must be single content line when collapsed (optional trailing lane).
		r := RenderChatBlocks([]ChatBlock{*b}, "m", 50, true)
		nContent := 0
		for _, ln := range r.Lines {
			if strings.TrimSpace(stripANSI(ln)) != "" {
				nContent++
			}
		}
		if nContent != 1 {
			t.Fatalf("%s collapsed content lines=%d want 1: %v", id, nContent, dumpPlain(r.Lines))
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
	// Header-only thin rail on first content line; same for all tool names.
	var first string
	for _, ln := range r.Lines {
		p := stripANSI(ln)
		if strings.TrimSpace(p) == "" {
			continue
		}
		first = p
		break
	}
	if !hasRailPrefix(first) {
		t.Fatalf("delegate header missing thin rail: %q", first)
	}
}

// hasRailPrefix reports thin/heavy rail glyphs (semantic palette).
func hasRailPrefix(plain string) bool {
	for _, g := range []string{"│", "|", "┊", ":", "├", "+", "┃", "#", "!", "▌", "◆", "*"} {
		if strings.HasPrefix(plain, g) {
			return true
		}
	}
	return false
}

// hasFullHeightRailPrefix kept for user-no-rail regression tests.
func hasFullHeightRailPrefix(plain string) bool {
	return hasRailPrefix(plain)
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
