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
	}, "model", 80)
	joined := strings.Join(rendered.Lines, "\n")
	if !strings.Contains(joined, "▾ thinking") || !strings.Contains(joined, "plan") || !strings.Contains(joined, "⚙ /status") {
		t.Fatalf("missing thinking/system presentation: %q", joined)
	}

	collapsed := RenderChatBlocks([]ChatBlock{{ID: "thinking", Kind: ChatBlockThinking, Text: "secret reasoning", Collapsed: true}}, "model", 80)
	if strings.Contains(strings.Join(collapsed.Lines, "\n"), "secret reasoning") {
		t.Fatalf("collapsed thinking leaked body: %#v", collapsed.Lines)
	}
}
