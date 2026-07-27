package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestRenderMessageForHistory_System verifies system prompts are skipped.
func TestRenderMessageForHistory_System(t *testing.T) {
	msg := provider.Message{Role: provider.RoleSystem, Content: "you are a helper"}
	lines := RenderMessageForHistory(msg, "test-model", 80)
	if lines != nil {
		t.Fatalf("expected nil for system, got %d lines", len(lines))
	}
}

// TestRenderMessageForHistory_User verifies user messages render as a bordered card.
func TestRenderMessageForHistory_User(t *testing.T) {
	msg := provider.Message{Role: provider.RoleUser, Content: "hello world"}
	lines := RenderMessageForHistory(msg, "test-model", 80)
	if len(lines) < 3 {
		t.Fatalf("expected card (≥3 lines), got %d: %v", len(lines), lines)
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "╭") || !strings.Contains(plain, "╰") {
		t.Fatalf("expected box border, got %q", plain)
	}
	if !strings.Contains(plain, "you") {
		t.Fatalf("expected user label, got %q", plain)
	}
	if !strings.Contains(plain, "hello world") {
		t.Fatalf("expected user content, got %q", plain)
	}
}

// TestRenderMessageForHistory_AssistantNoTools verifies plain assistant messages.
func TestRenderMessageForHistory_AssistantNoTools(t *testing.T) {
	msg := provider.Message{Role: provider.RoleAssistant, Content: "Hello, I'm here"}
	lines := RenderMessageForHistory(msg, "deepseek-v4", 80)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "deepseek-v4") {
		t.Fatalf("expected model header, got %q", plain)
	}
	if !strings.Contains(plain, "╭─") {
		t.Fatalf("expected model chrome ╭─, got %q", plain)
	}
	if !strings.Contains(plain, "Hello") {
		t.Fatalf("expected content, got %q", plain)
	}
}

// TestRenderMessageForHistory_AssistantWithToolCalls verifies tool calls are rendered compactly.
func TestRenderMessageForHistory_AssistantWithToolCalls(t *testing.T) {
	msg := provider.Message{
		Role:    provider.RoleAssistant,
		Content: "",
		ToolCalls: []provider.ToolCall{
			{
				ID:   "call_1",
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "read_file", Arguments: `{"path":"main.go"}`},
			},
		},
	}
	lines := RenderMessageForHistory(msg, "m", 80)
	if len(lines) < 2 {
		t.Fatalf("expected >= 2 lines (header + tool call), got %d: %v", len(lines), lines)
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "╭─ m ") && !strings.Contains(plain, "m ") {
		t.Fatalf("expected model header, got %q", plain)
	}
	if !strings.Contains(plain, "╭─") {
		t.Fatalf("expected model chrome, got %q", plain)
	}
	if !strings.Contains(plain, "read_file") {
		t.Fatalf("expected tool name 'read_file', got %q", plain)
	}
	if !strings.Contains(plain, "main.go") {
		t.Fatalf("expected tool argument, got %q", plain)
	}
}

// TestRenderMessageForHistory_AssistantWithToolsAndContent verifies both tool calls
// and text content are rendered.
func TestRenderMessageForHistory_AssistantWithToolsAndContent(t *testing.T) {
	msg := provider.Message{
		Role:    provider.RoleAssistant,
		Content: "Let me check the file first, then I'll analyze it.",
		ToolCalls: []provider.ToolCall{
			{
				ID:   "call_1",
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "read_file", Arguments: `{"path":"main.go"}`},
			},
		},
	}
	lines := RenderMessageForHistory(msg, "m", 80)
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "read_file") {
		t.Fatalf("expected tool name, got %q", plain)
	}
	if !strings.Contains(plain, "Let me check") {
		t.Fatalf("expected text content, got %q", plain)
	}
}

// TestRenderMessageForHistory_ToolResult verifies tool results show a compact summary.
func TestRenderMessageForHistory_ToolResult(t *testing.T) {
	msg := provider.Message{
		Role:       provider.RoleTool,
		ToolCallID: "call_1",
		Name:       "read_file",
		Content:    "package main\nfunc main() {\n\tfmt.Println(\"hello\")\n}",
	}
	lines := RenderMessageForHistory(msg, "m", 80)
	if len(lines) != 1 {
		t.Fatalf("expected 1 compact line, got %d: %v", len(lines), lines)
	}
	plain := stripANSI(lines[0])
	if !strings.Contains(plain, "read_file") {
		t.Fatalf("expected tool name, got %q", plain)
	}
	if !strings.Contains(plain, "package main") {
		t.Fatalf("expected tool result preview, got %q", plain)
	}
}

// TestRenderMessageForHistory_ToolResultLarge verifies large tool results are truncated.
func TestRenderMessageForHistory_ToolResultLarge(t *testing.T) {
	big := strings.Repeat("abcdefghij ", 1000) // ~12KB
	msg := provider.Message{
		Role:       provider.RoleTool,
		ToolCallID: "call_1",
		Name:       "read_file",
		Content:    big,
	}
	lines := RenderMessageForHistory(msg, "m", 80)
	plain := stripANSI(lines[0])
	// Should be truncated
	if len(plain) > 500 {
		t.Fatalf("expected truncated result, got %d chars", len(plain))
	}
}

// TestRenderMessageForHistory_ToolErrorResult verifies error tool results are indicated.
func TestRenderMessageForHistory_ToolErrorResult(t *testing.T) {
	msg := provider.Message{
		Role:       provider.RoleTool,
		ToolCallID: "call_1",
		Name:       "read_file",
		Content:    "error: file not found",
	}
	lines := RenderMessageForHistory(msg, "m", 80)
	plain := stripANSI(lines[0])
	if !strings.Contains(plain, "read_file") {
		t.Fatalf("expected tool name, got %q", plain)
	}
}

// TestRenderMessageForHistory_ToolEmptyResult verifies empty results are handled.
func TestRenderMessageForHistory_ToolEmptyResult(t *testing.T) {
	msg := provider.Message{
		Role:       provider.RoleTool,
		ToolCallID: "call_1",
		Name:       "list_dir",
		Content:    "",
	}
	lines := RenderMessageForHistory(msg, "m", 80)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line for empty result, got %d", len(lines))
	}
}

// TestRenderTurn_Basic verifies a simple user→assistant turn renders correctly.
func TestRenderTurn_Basic(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "hello"},
		{Role: provider.RoleAssistant, Content: "hi there!"},
	}
	lines := RenderTurn(msgs, "test-model", 80)
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "╭") || !strings.Contains(plain, "you") {
		t.Fatalf("expected user card, got %q", plain)
	}
	if !strings.Contains(plain, "hello") {
		t.Fatalf("expected user content, got %q", plain)
	}
	if !strings.Contains(plain, "test-model") || !strings.Contains(plain, "╭─") {
		t.Fatalf("expected model header, got %q", plain)
	}
	if !strings.Contains(plain, "hi there") {
		t.Fatalf("expected assistant content, got %q", plain)
	}
}

// TestRenderTurn_WithTools verifies a full agent turn renders as a coherent block.
func TestRenderTurn_WithTools(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "analyze main.go"},
		{
			Role:    provider.RoleAssistant,
			Content: "",
			ToolCalls: []provider.ToolCall{{
				ID: "call_1", Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "read_file", Arguments: `{"path":"main.go"}`},
			}},
		},
		{Role: provider.RoleTool, ToolCallID: "call_1", Name: "read_file", Content: "package main\nfunc main() {}"},
		{Role: provider.RoleAssistant, Content: "Found the main function."},
	}
	lines := RenderTurn(msgs, "deepseek-v4", 80)
	plain := stripANSI(strings.Join(lines, "\n"))

	if !strings.Contains(plain, "╭") || !strings.Contains(plain, "you") {
		t.Fatalf("expected user card, got %q", plain)
	}
	if !strings.Contains(plain, "analyze main.go") {
		t.Fatalf("expected user content, got %q", plain)
	}
	if !strings.Contains(plain, "deepseek-v4") || !strings.Contains(plain, "╭─") {
		t.Fatalf("expected model header, got %q", plain)
	}
	if !strings.Contains(plain, "read_file") {
		t.Fatalf("expected tool name, got %q", plain)
	}
	if !strings.Contains(plain, "Found the main function") {
		t.Fatalf("expected final answer, got %q", plain)
	}
	// Must NOT have raw tool role headers
	if strings.Contains(plain, "── tool ──") {
		t.Fatalf("should not have tool role header in turn rendering, got %q", plain)
	}
}

// TestRenderTurn_MultipleToolCalls verifies parallel tool calls render compactly.
func TestRenderTurn_MultipleToolCalls(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "analyze project"},
		{
			Role:    provider.RoleAssistant,
			Content: "",
			ToolCalls: []provider.ToolCall{
				{
					ID: "1", Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "read_file", Arguments: `{"path":"a.go"}`},
				},
				{
					ID: "2", Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "grep", Arguments: `{"pattern":"main","glob":"*.go"}`},
				},
			},
		},
		{Role: provider.RoleTool, ToolCallID: "1", Name: "read_file", Content: "package a"},
		{Role: provider.RoleTool, ToolCallID: "2", Name: "grep", Content: "found in main.go:2"},
		{Role: provider.RoleAssistant, Content: "Analysis complete."},
	}
	lines := RenderTurn(msgs, "m", 80)
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "read_file") {
		t.Fatalf("expected read_file tool, got %q", plain)
	}
	if !strings.Contains(plain, "grep") {
		t.Fatalf("expected grep tool, got %q", plain)
	}
	if !strings.Contains(plain, "Analysis complete") {
		t.Fatalf("expected final answer, got %q", plain)
	}
}

// TestRenderTurn_NoFinalAnswer verifies a turn ending with tools (no final assistant).
func TestRenderTurn_NoFinalAnswer(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "read file"},
		{
			Role:    provider.RoleAssistant,
			Content: "",
			ToolCalls: []provider.ToolCall{{
				ID: "1", Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "read_file", Arguments: `{"path":"test.txt"}`},
			}},
		},
		{Role: provider.RoleTool, ToolCallID: "1", Name: "read_file", Content: "file content"},
	}
	lines := RenderTurn(msgs, "m", 80)
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "read_file") {
		t.Fatalf("expected tool name, got %q", plain)
	}
	if !strings.Contains(plain, "file content") {
		t.Fatalf("expected tool result, got %q", plain)
	}
}

// TestRenderTurn_EmptyTurn verifies an empty or system-only group returns nil.
func TestRenderTurn_EmptyTurn(t *testing.T) {
	lines := RenderTurn(nil, "m", 80)
	if lines != nil {
		t.Fatalf("expected nil for empty, got %d lines", len(lines))
	}

	lines = RenderTurn([]provider.Message{}, "m", 80)
	if lines != nil {
		t.Fatalf("expected nil for empty slice, got %d lines", len(lines))
	}

	// System-only also returns nil
	lines = RenderTurn([]provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
	}, "m", 80)
	if lines != nil {
		t.Fatalf("expected nil for system-only, got %d lines", len(lines))
	}
}

// TestRenderHistoryMessages verifies the top-level grouping and rendering function.
func TestRenderHistoryMessages(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "first"},
		{Role: provider.RoleAssistant, Content: "first reply"},
		{Role: provider.RoleUser, Content: "second"},
		{Role: provider.RoleAssistant, Content: "second reply"},
	}
	lines := RenderHistoryMessages(msgs, "m", 80)
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "first") {
		t.Fatalf("expected first user content, got %q", plain)
	}
	if !strings.Contains(plain, "second") {
		t.Fatalf("expected second user content, got %q", plain)
	}
	if !strings.Contains(plain, "first reply") {
		t.Fatalf("expected first reply, got %q", plain)
	}
	if !strings.Contains(plain, "second reply") {
		t.Fatalf("expected second reply, got %q", plain)
	}
}

// TestRenderHistoryMessages_WithTools verifies turn-aware rendering with tool calls.
func TestRenderHistoryMessages_WithTools(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "first turn"},
		{
			Role:    provider.RoleAssistant,
			Content: "",
			ToolCalls: []provider.ToolCall{{
				ID: "1", Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "read_file", Arguments: `{"path":"x.go"}`},
			}},
		},
		{Role: provider.RoleTool, ToolCallID: "1", Name: "read_file", Content: "content"},
		{Role: provider.RoleAssistant, Content: "first done"},
	}
	lines := RenderHistoryMessages(msgs, "m", 80)
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "first turn") {
		t.Fatalf("expected user content, got %q", plain)
	}
	if !strings.Contains(plain, "read_file") {
		t.Fatalf("expected tool name, got %q", plain)
	}
	if !strings.Contains(plain, "first done") {
		t.Fatalf("expected final answer, got %q", plain)
	}
	// Should not have tool role headers
	if strings.Contains(plain, "── tool ──") {
		t.Fatalf("should not have tool role header, got %q", plain)
	}
}

// TestRenderTurn_ToolCallSummaryFormat verifies the visual format of tool call lines.
func TestRenderTurn_ToolCallSummaryFormat(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "u"},
		{
			Role:    provider.RoleAssistant,
			Content: "",
			ToolCalls: []provider.ToolCall{{
				ID: "1", Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "list_dir", Arguments: `{"path":"."}`},
			}},
		},
		{Role: provider.RoleTool, ToolCallID: "1", Name: "list_dir", Content: "main.go\nREADME.md"},
		{Role: provider.RoleAssistant, Content: "done"},
	}
	lines := RenderTurn(msgs, "m", 80)
	full := strings.Join(lines, "\n")
	// Should have both a tool call indicator and result indicator in the full output
	if !strings.Contains(full, "list_dir") {
		t.Fatalf("expected tool name list_dir, got %q", full)
	}
	// Tool result should appear
	if !strings.Contains(stripANSI(full), "main.go") {
		t.Fatalf("expected tool result content, got %q", full)
	}
}
