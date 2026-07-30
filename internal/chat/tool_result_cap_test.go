package chat

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// fatOutputTool returns a fixed oversized body.
type fatOutputTool struct{ body string }

func (t *fatOutputTool) Name() string               { return "fat_tool" }
func (t *fatOutputTool) Description() string        { return "returns a large body" }
func (t *fatOutputTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *fatOutputTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead, ResourceKey: "path:fat"}
}
func (t *fatOutputTool) Execute(context.Context, json.RawMessage) (string, error) {
	return t.body, nil
}

func runFatToolTurn(t *testing.T, capBytes int, body string) string {
	t.Helper()
	reg := tools.NewRegistry()
	reg.Register(&fatOutputTool{body: body})
	s := NewSession(&config.Resolved{
		Model:        "m",
		SystemPrompt: "sys",
		Tools:        config.ToolsConfig{MaxToolResultBytes: capBytes},
	}, &sessionToolCompleter{tool: "fat_tool", args: `{}`})
	s.UseTools = true
	s.Tools = reg
	s.MaxSteps = 3
	if _, err := s.SendUser(context.Background(), "go", io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, m := range s.MessagesCopy() {
		if m.Role == provider.RoleTool {
			return m.Content
		}
	}
	t.Fatal("no tool result in history")
	return ""
}

// TestSendAgentUsesConfiguredToolResultCap pins that the interactive loop's
// tool-result ceiling is the operator-configured [tools] max_tool_result_bytes
// — not a hardcoded default — and that 0 means uncapped.
func TestSendAgentUsesConfiguredToolResultCap(t *testing.T) {
	big := strings.Repeat("z", 100*1024)

	capped := runFatToolTurn(t, 2048, big)
	if len(capped) > 2048 {
		t.Fatalf("tool result %d bytes exceeds configured cap 2048", len(capped))
	}
	if !strings.Contains(capped, "(truncated") {
		t.Fatalf("truncation marker missing from capped result (tail %q)", capped[len(capped)-40:])
	}

	uncapped := runFatToolTurn(t, 0, big)
	if uncapped != big {
		t.Fatalf("with cap 0 the tool result must survive untruncated; got %d bytes (want %d)",
			len(uncapped), len(big))
	}
}
