package clichat

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestHydrateChatBlocks_WithReasoningContent(t *testing.T) {
	t.Parallel()
	call := provider.ToolCall{ID: "c1"}
	call.Function.Name = "read_file"
	call.Function.Arguments = `{"path":"main.go"}`
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "read file"},
		{
			Role:             provider.RoleAssistant,
			Content:          "",
			ReasoningContent: "I need to check the file contents.\nAnalyzing...",
			ToolCalls:        []provider.ToolCall{call},
		},
		{Role: provider.RoleTool, Name: "read_file", ToolCallID: "c1", Content: "package main"},
		{
			Role:             provider.RoleAssistant,
			Content:          "File contents read.",
			ReasoningContent: "Done analyzing.",
		},
	}

	blocks := HydrateChatBlocks(msgs)
	var thinkingBlocks []ChatBlock
	for _, b := range blocks {
		if b.Kind == ChatBlockThinking {
			thinkingBlocks = append(thinkingBlocks, b)
		}
	}

	if len(thinkingBlocks) != 2 {
		t.Fatalf("expected 2 thinking blocks, got %d", len(thinkingBlocks))
	}
	if thinkingBlocks[0].Text != "I need to check the file contents.\nAnalyzing..." {
		t.Fatalf("unexpected first thinking text: %q", thinkingBlocks[0].Text)
	}
	if !thinkingBlocks[0].Collapsed {
		t.Fatalf("expected hydrated thinking block to be collapsed by default")
	}
	if thinkingBlocks[1].Text != "Done analyzing." {
		t.Fatalf("unexpected second thinking text: %q", thinkingBlocks[1].Text)
	}
}
