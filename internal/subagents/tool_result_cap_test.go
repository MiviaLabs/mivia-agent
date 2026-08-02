package subagents

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// capCaptureCompleter drives one tool call, then captures the tool-role
// history message it is sent on the following turn.
type capCaptureCompleter struct {
	calls      int
	toolResult string
}

func (c *capCaptureCompleter) Name() string { return "cap-capture" }
func (c *capCaptureCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return r.Content, nil
}
func (c *capCaptureCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}
func (c *capCaptureCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.calls++
	if c.calls == 1 {
		var call provider.ToolCall
		call.ID = "tc1"
		call.Type = "function"
		call.Function.Name = "nested_fat_tool"
		call.Function.Arguments = `{}`
		return &provider.Response{ToolCalls: []provider.ToolCall{call}, FinishReason: "tool_calls"}, nil
	}
	for _, m := range req.Messages {
		if m.Role == provider.RoleTool {
			c.toolResult = m.Content
		}
	}
	return &provider.Response{Content: "done", FinishReason: "stop"}, nil
}

// nestedFatTool returns an oversized body inside a nested sub-agent loop.
type nestedFatTool struct{ body string }

func (t *nestedFatTool) Name() string               { return "nested_fat_tool" }
func (t *nestedFatTool) Description() string        { return "returns a large body" }
func (t *nestedFatTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *nestedFatTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead, ResourceKey: "path:nested-fat"}
}
func (t *nestedFatTool) Execute(context.Context, json.RawMessage) (string, error) {
	return t.body, nil
}

func runNestedFatToolTask(t *testing.T, capBytes int, body string) string {
	t.Helper()
	reg := tools.NewRegistry()
	reg.Register(&nestedFatTool{body: body})
	comp := &capCaptureCompleter{}
	h := &MultiStepHandler{
		Completer:          comp,
		FullRegistry:       reg,
		Model:              "test-model",
		MaxSteps:           3,
		MaxToolResultChars: capBytes,
	}
	if _, err := h.Invoke(context.Background(), runtime.Request{
		ID: "task-1", Name: "multi_step", Kind: runtime.Subagent,
		Input: json.RawMessage(`"use the tool"`),
	}); err != nil {
		t.Fatal(err)
	}
	if comp.toolResult == "" {
		t.Fatal("nested loop produced no tool result")
	}
	return comp.toolResult
}

// TestMultiStepHandlerAppliesToolResultCap pins that the operator-configured
// tool-result ceiling governs nested sub-agent loops exactly like the
// interactive session loop, and that 0 leaves results whole.
func TestMultiStepHandlerAppliesToolResultCap(t *testing.T) {
	big := strings.Repeat("q", 50*1024)

	capped := runNestedFatToolTask(t, 2048, big)
	if len(capped) > 2048 {
		t.Fatalf("nested tool result %d bytes exceeds configured cap 2048", len(capped))
	}
	if !strings.Contains(capped, "truncated") {
		t.Fatalf("truncation marker missing (tail %q)", capped[len(capped)-40:])
	}

	uncapped := runNestedFatToolTask(t, 0, big)
	if uncapped != big {
		t.Fatalf("with cap 0 the nested tool result must survive whole; got %d bytes (want %d)",
			len(uncapped), len(big))
	}
}
