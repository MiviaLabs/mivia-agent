package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestReconstructStatus_EmptyContentTools(t *testing.T) {
	t.Parallel()
	call := provider.ToolCall{ID: "c1"}
	call.Function.Name = "list_dir"
	call.Function.Arguments = `{"path":"."}`
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "list"},
		{Role: provider.RoleAssistant, Content: "", ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, Name: "list_dir", ToolCallID: "c1", Content: "cmd/"},
		{Role: provider.RoleAssistant, Content: "Here are the files."},
	}
	blocks := HydrateChatBlocksForView(msgs)
	if !hasBlockKind(blocks, ChatBlockSystem) {
		t.Fatalf("expected status system line, kinds=%v", blockKinds(blocks))
	}
	found := false
	for _, b := range blocks {
		if IsWorkStatusBlock(b) {
			found = true
			if !strings.Contains(b.Text, "→") {
				t.Fatalf("status missing →: %q", b.Text)
			}
		}
	}
	if !found {
		t.Fatal("no work status block")
	}
	if !kindOrderContains(blockKinds(blocks),
		ChatBlockUser, ChatBlockSystem, ChatBlockTool, ChatBlockTool, ChatBlockAssistant,
	) {
		// Hydrate may emit assistant tool-call block + tool result as two tools.
		if !kindOrderContains(blockKinds(blocks), ChatBlockUser, ChatBlockSystem, ChatBlockTool) {
			t.Fatalf("status before tools missing: %v", blockKinds(blocks))
		}
	}
}

func TestReconstructStatus_ShortContentUsesStatus(t *testing.T) {
	t.Parallel()
	call := provider.ToolCall{ID: "c1"}
	call.Function.Name = "grep"
	call.Function.Arguments = `{"pattern":"auth"}`
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "go"},
		{Role: provider.RoleAssistant, Content: "OK.", ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, Name: "grep", ToolCallID: "c1", Content: "matches"},
	}
	blocks := HydrateChatBlocksForView(msgs)
	for _, b := range blocks {
		if b.Kind == ChatBlockAssistant && strings.Contains(b.Text, "OK") {
			t.Fatalf("ghost interim should be dropped: %q", b.Text)
		}
	}
	if !hasBlockKind(blocks, ChatBlockSystem) {
		t.Fatal("expected status when short interim rejected")
	}
}

func TestReconstructStatus_RealInterimSkipsStatus(t *testing.T) {
	t.Parallel()
	call := provider.ToolCall{ID: "c1"}
	call.Function.Name = "grep"
	call.Function.Arguments = `{"pattern":"bug"}`
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "find"},
		{Role: provider.RoleAssistant, Content: "I'll search the codebase first.", ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, Name: "grep", ToolCallID: "c1", Content: "ok"},
	}
	blocks := HydrateChatBlocksForView(msgs)
	for _, b := range blocks {
		if IsWorkStatusBlock(b) {
			t.Fatalf("status must not accompany real interim: %q", b.Text)
		}
	}
	if !hasAssistantText(blocks, "I'll search") {
		t.Fatal("real interim must remain")
	}
}

func TestReconstructStatus_DoesNotMutateMessages(t *testing.T) {
	t.Parallel()
	call := provider.ToolCall{ID: "c1"}
	call.Function.Name = "list_dir"
	call.Function.Arguments = `{}`
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "x"},
		{Role: provider.RoleAssistant, Content: "", ToolCalls: []provider.ToolCall{call}},
	}
	before := len(msgs)
	_ = HydrateChatBlocksForView(msgs)
	if len(msgs) != before {
		t.Fatal("messages slice length changed")
	}
	if msgs[1].Role != provider.RoleAssistant || msgs[1].Content != "" {
		t.Fatal("message content mutated")
	}
	// Pure hydrate still has no status system line.
	pure := HydrateChatBlocks(msgs)
	for _, b := range pure {
		if IsWorkStatusBlock(b) {
			t.Fatal("pure HydrateChatBlocks must not insert status")
		}
	}
}

func TestHydrateChatBlocksForView_VsPure(t *testing.T) {
	t.Parallel()
	// Final-only turn: reconstruct is a no-op.
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "hi"},
		{Role: provider.RoleAssistant, Content: "hello there friend"},
	}
	a := HydrateChatBlocks(msgs)
	b := HydrateChatBlocksForView(msgs)
	if len(a) != len(b) {
		t.Fatalf("final-only lengths %d vs %d", len(a), len(b))
	}
}
