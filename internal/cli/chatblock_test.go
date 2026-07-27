package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestHydrateChatBlocksStableLegacyOrder(t *testing.T) {
	call := provider.ToolCall{ID: "c1"}
	call.Function.Name = "read_file"
	call.Function.Arguments = `{"path":"a.go"}`
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "hidden"},
		{Role: provider.RoleUser, Content: "hello"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, Name: "read_file", ToolCallID: "c1", Content: "ok"},
		{Role: provider.RoleAssistant, Content: "done"},
	}
	blocks := HydrateChatBlocks(msgs)
	if len(blocks) != 4 || blocks[0].Kind != ChatBlockUser || blocks[1].ToolName != "read_file" || blocks[3].Text != "done" {
		t.Fatalf("unexpected blocks: %#v", blocks)
	}
	for i, block := range blocks {
		if block.ID == "" || block.Sequence != uint64(i+1) {
			t.Fatalf("unstable identity: %#v", block)
		}
	}
}

func TestApplyChatBlockEventRejectsDuplicateAndStale(t *testing.T) {
	blocks := ApplyChatBlockEvent(nil, ChatBlockEvent{TurnID: 1, Sequence: 1, Kind: ChatBlockAssistant, Text: "a"})
	blocks = ApplyChatBlockEvent(blocks, ChatBlockEvent{TurnID: 1, Sequence: 1, Kind: ChatBlockAssistant, Text: "duplicate"})
	blocks = ApplyChatBlockEvent(blocks, ChatBlockEvent{TurnID: 1, Sequence: 0, Kind: ChatBlockAssistant, Text: "invalid"})
	if len(blocks) != 1 || blocks[0].Text != "a" {
		t.Fatalf("duplicate/stale event changed transcript: %#v", blocks)
	}
	blocks = ApplyChatBlockEvent(blocks, ChatBlockEvent{TurnID: 1, Sequence: 2, BlockID: blocks[0].ID, Text: "ab"})
	if len(blocks) != 1 || blocks[0].Text != "ab" {
		t.Fatalf("stream update did not mutate existing block: %#v", blocks)
	}
}

func TestRenderChatBlocksDivider(t *testing.T) {
	blocks := []ChatBlock{
		{ID: "u1", Kind: ChatBlockUser, Text: "first"},
		{ID: "d1", Kind: ChatBlockDivider, Text: ""},
		{ID: "u2", Kind: ChatBlockUser, Text: "second"},
	}
	rendered := RenderChatBlocks(blocks, "model", 80)
	joined := strings.Join(rendered.Lines, "\n")
	if !strings.Contains(joined, "─── · ───") {
		t.Fatalf("expected divider line in rendered blocks, got %q", joined)
	}
	// Divider must appear between the two user blocks.
	plain := stripANSI(joined)
	firstIdx := strings.Index(plain, "first")
	sepIdx := strings.Index(plain, "─── · ───")
	secondIdx := strings.Index(plain, "second")
	if firstIdx < 0 || sepIdx < 0 || secondIdx < 0 {
		t.Fatalf("missing expected content: first=%d sep=%d second=%d", firstIdx, sepIdx, secondIdx)
	}
	if firstIdx > sepIdx {
		t.Fatalf("divider before first block: firstIdx=%d sepIdx=%d", firstIdx, sepIdx)
	}
	if sepIdx > secondIdx {
		t.Fatalf("divider after second block: sepIdx=%d secondIdx=%d", sepIdx, secondIdx)
	}
}

func TestRenderChatBlocksWidthMatrixAndIsolation(t *testing.T) {
	blocks := HydrateChatBlocks([]provider.Message{{Role: provider.RoleUser, Content: strings.Repeat("hello ", 30)}, {Role: provider.RoleAssistant, Content: "answer"}})
	for _, width := range []int{0, 40, 80, 120} {
		rendered := RenderChatBlocks(blocks, "model", width)
		if len(rendered.Lines) == 0 || rendered.Ranges[blocks[0].ID][0] != 0 {
			t.Fatalf("invalid render at width %d: %#v", width, rendered)
		}
	}
	collapsed := append([]ChatBlock(nil), blocks...)
	collapsed[0].Collapsed = true
	rendered := RenderChatBlocks(collapsed, "model", 80)
	if rendered.Ranges[blocks[1].ID][0] != 1 {
		t.Fatalf("collapse changed unrelated block range: %#v", rendered.Ranges)
	}
}

func TestSafeChatBlockTextRedactsTerminalControlsAndCaps(t *testing.T) {
	text := "token=abc\x1b]0;secret\x07" + strings.Repeat("x", 20)
	clean := SafeChatBlockText(text, 10)
	if strings.Contains(clean, "\x1b") || len([]rune(clean)) > 11 || !strings.Contains(clean, "token") {
		t.Fatalf("unsafe text handling: %q", clean)
	}
}

func TestRenderChatBlocksSanitizesCompatibilityRenderedLines(t *testing.T) {
	rendered := RenderChatBlocks([]ChatBlock{{ID: "compat", Kind: ChatBlockSystem, Rendered: "ok\x1b]0;secret\a"}}, "model", 80)
	if len(rendered.Lines) != 1 || strings.Contains(rendered.Lines[0], "\x1b") || strings.Contains(rendered.Lines[0], "secret") {
		t.Fatalf("compatibility line was not sanitized: %#v", rendered.Lines)
	}
}

func TestRenderChatBlocksThinkingAndSystem(t *testing.T) {
	rendered := RenderChatBlocks([]ChatBlock{
		{ID: "thinking", Kind: ChatBlockThinking, Text: "plan\nthen act", Collapsed: false},
		{ID: "slash", Kind: ChatBlockSystem, Text: "/status"},
	}, "model", 80, true)
	joined := strings.Join(rendered.Lines, "\n")
	if !strings.Contains(joined, "▾ thinking") || !strings.Contains(joined, "plan") || !strings.Contains(joined, "⚙ /status") {
		t.Fatalf("missing thinking/system presentation: %q", joined)
	}

	collapsed := RenderChatBlocks([]ChatBlock{{ID: "thinking", Kind: ChatBlockThinking, Text: "secret reasoning", Collapsed: true}}, "model", 80, true)
	if strings.Contains(strings.Join(collapsed.Lines, "\n"), "secret reasoning") {
		t.Fatalf("collapsed thinking leaked body: %#v", collapsed.Lines)
	}

	// Global default false hides thinking even when not per-block collapsed.
	hidden := RenderChatBlocks([]ChatBlock{{ID: "thinking", Kind: ChatBlockThinking, Text: "hidden content", Collapsed: false}}, "model", 80, false)
	if strings.Contains(strings.Join(hidden.Lines, "\n"), "hidden content") {
		t.Fatalf("global default false should hide thinking content: %#v", hidden.Lines)
	}
}

func TestRenderChatBlocksToolExpanded(t *testing.T) {
	// Large content (> maxToolResultPreview, ~200 chars) when not collapsed renders full text.
	largeText := strings.Repeat("line of content\n", 20) // well over 200 chars
	blocks := []ChatBlock{
		{ID: "tool1", Kind: ChatBlockTool, ToolName: "read_file", Text: largeText, Collapsed: false},
	}
	rendered := RenderChatBlocks(blocks, "model", 80)
	joined := strings.Join(rendered.Lines, "\n")
	if !strings.Contains(joined, "read_file") {
		t.Fatalf("expected tool name in expanded output, got %q", joined)
	}
	if !strings.Contains(joined, "line of content") {
		t.Fatalf("expected full content in expanded output, got %q", joined)
	}
	// Must have more than one line (header + content).
	if len(rendered.Lines) < 3 {
		t.Fatalf("expected multi-line expanded rendering, got %d lines: %v", len(rendered.Lines), rendered.Lines)
	}
}

func TestRenderChatBlocksToolCollapsed(t *testing.T) {
	// Large content when collapsed should show a compact one-liner, not multi-line content.
	largeText := strings.Repeat("line of content\n", 20)
	blocks := []ChatBlock{
		{ID: "tool1", Kind: ChatBlockTool, ToolName: "read_file", Text: largeText, Collapsed: true},
	}
	rendered := RenderChatBlocks(blocks, "model", 80)
	joined := strings.Join(rendered.Lines, "\n")
	// Should be a single line with icon, name, and truncated preview.
	if len(rendered.Lines) != 1 {
		t.Fatalf("collapsed tool should be a single line, got %d lines: %v", len(rendered.Lines), rendered.Lines)
	}
	if !strings.Contains(joined, "read_file") {
		t.Fatalf("collapsed tool should show tool name, got %q", joined)
	}
	// Should NOT contain a newline within the content (multi-line = expanded).
	if strings.Count(joined, "\n") > 0 {
		t.Fatalf("collapsed tool should not have multiple lines: %q", joined)
	}
}

func TestRenderChatBlocksToolSmallContent(t *testing.T) {
	// Small content (<= maxToolResultPreview) renders compactly regardless of collapse state.
	smallText := "short result"
	blocks := []ChatBlock{
		{ID: "tool1", Kind: ChatBlockTool, ToolName: "grep", Text: smallText, Collapsed: false},
	}
	rendered := RenderChatBlocks(blocks, "model", 80)
	joined := strings.Join(rendered.Lines, "\n")
	if !strings.Contains(joined, "grep") {
		t.Fatalf("expected tool name, got %q", joined)
	}
	if !strings.Contains(joined, "short result") {
		t.Fatalf("expected small content rendered, got %q", joined)
	}
	// Should be compact (1-2 lines).
	if len(rendered.Lines) > 3 {
		t.Fatalf("small content should render compactly, got %d lines: %v", len(rendered.Lines), rendered.Lines)
	}
}
