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
		{ID: "t1", Kind: ChatBlockThinking, Text: "thinking", Collapsed: true},
		{ID: "s1", Kind: ChatBlockSystem, Text: "status", Rendered: tuiInfoStyle.Render("  ⚙ status")},
	}, "model", 80)
	joined := strings.Join(rendered.Lines, "\n")
	if !strings.Contains(joined, "thinking") {
		t.Fatalf("expected thinking content: %q", joined)
	}
	if !strings.Contains(joined, "status") {
		t.Fatalf("expected system content: %q", joined)
	}
}

// TestAppendBlock_BlockBasedTruncate ensures appendBlock drops whole blocks,
// not lines, preserving block identity and order after truncation.
func TestAppendBlock_BlockBasedTruncate(t *testing.T) {
	m := journeyModel(t)
	m.width = 80
	m.modelName = "test-model"

	// Fill blocks past maxBlocks to trigger truncation.
	// Use single-line user blocks for predictability.
	const maxBlocks = 1000 // must match appendBlock const
	for i := 0; i < maxBlocks+50; i++ {
		m.appendBlock(ChatBlock{
			Kind: ChatBlockUser,
			Text: "msg",
		})
	}
	// Verify blocks truncated to maxBlocks.
	if len(m.blocks) > maxBlocks {
		t.Fatalf("expected max %d blocks after truncation, got %d", maxBlocks, len(m.blocks))
	}
	// Verify block kinds remain valid (no ChatBlockSystem corruption).
	for _, b := range m.blocks {
		if b.Kind != ChatBlockUser {
			t.Fatalf("expected all ChatBlockUser, got %s", b.Kind)
		}
	}
	// Verify sequence numbers are contiguous starting at 1.
	for i, b := range m.blocks {
		if b.Sequence != uint64(i+1) {
			t.Fatalf("block %d sequence=%d want %d", i, b.Sequence, i+1)
		}
	}
	// Verify hit ranges exist for all blocks.
	if m.chatBlockRanges == nil {
		t.Fatal("chatBlockRanges must not be nil")
	}
	for _, b := range m.blocks {
		rng, ok := m.chatBlockRanges[b.ID]
		if !ok {
			t.Fatalf("missing range for block %s", b.ID)
		}
		if rng[0] < 0 || rng[1] <= rng[0] {
			t.Fatalf("invalid range for block %s: %v", b.ID, rng)
		}
	}
	// Verify oldest blocks were dropped (the first block should be old but
	// not the original first).
	if len(m.blocks) > 0 && m.blocks[0].Sequence != 1 {
		t.Fatalf("first block after truncation should have sequence 1 for clean start, got %d", m.blocks[0].Sequence)
	}
}

// TestAppendBlock_BlockTruncatePreservesMultiLineBlocks ensures that blocks
// with many rendered lines are still properly truncated as whole blocks.
func TestAppendBlock_BlockTruncatePreservesMultiLineBlocks(t *testing.T) {
	m := journeyModel(t)
	m.width = 80
	m.modelName = "test-model"

	// Add assistant blocks with multi-line content to exercise multi-line rendering.
	const maxBlocks = 1000
	for i := 0; i < maxBlocks+20; i++ {
		m.appendBlock(ChatBlock{
			Kind: ChatBlockAssistant,
			Text: "line 1\nline 2\nline 3\n",
		})
	}
	if len(m.blocks) > maxBlocks {
		t.Fatalf("expected max %d blocks after truncation, got %d", maxBlocks, len(m.blocks))
	}
	// Even with multi-line blocks, the count must stay within maxBlocks.
	if len(m.blocks) == 0 {
		t.Fatal("blocks should not be empty after truncation")
	}
	// Verify no partial blocks (all block IDs should have ranges).
	for _, b := range m.blocks {
		rng, ok := m.chatBlockRanges[b.ID]
		if !ok {
			t.Fatalf("missing range for block %s", b.ID)
		}
		if rng[0] < 0 || rng[1] <= rng[0] {
			t.Fatalf("invalid range for block %s: %v", b.ID, rng)
		}
	}
}
