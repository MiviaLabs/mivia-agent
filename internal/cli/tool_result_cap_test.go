package cli

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// capProbeCompleter drives one nested tool call, then records the tool-role
// history message the nested loop sends back on the following turn.
type capProbeCompleter struct {
	calls      int
	toolResult string
}

func (c *capProbeCompleter) Name() string { return "cap-probe" }
func (c *capProbeCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return r.Content, nil
}
func (c *capProbeCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}
func (c *capProbeCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.calls++
	if c.calls == 1 {
		var call provider.ToolCall
		call.ID = "tc1"
		call.Type = "function"
		call.Function.Name = "cap_probe_tool"
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

// capProbeTool returns an oversized body from inside a nested loop.
type capProbeTool struct{ body string }

func (t *capProbeTool) Name() string               { return "cap_probe_tool" }
func (t *capProbeTool) Description() string        { return "returns a large body" }
func (t *capProbeTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *capProbeTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead, ResourceKey: "path:cap-probe"}
}
func (t *capProbeTool) Execute(context.Context, json.RawMessage) (string, error) {
	return t.body, nil
}

func invokeNestedHandler(t *testing.T, handlerName string, capBytes int) string {
	t.Helper()
	reg := tools.NewRegistry()
	reg.Register(&capProbeTool{body: strings.Repeat("w", 50*1024)})
	comp := &capProbeCompleter{}

	skillReg := skills.NewRegistry()
	if err := skillReg.Register(skills.Definition{
		Name:         "cap-probe-skill",
		Instructions: "Probe the tool-result cap.",
		Run: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	d, err := NewSessionDispatcher(reg, comp, "test-model",
		config.SubagentConfig{DefaultTimeout: 60, StoreBackend: "memory"},
		capBytes, skillReg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)

	res := d.Invoke(context.Background(), runtime.Request{
		ID:    "cap-probe-req",
		Kind:  runtime.Subagent,
		Name:  handlerName,
		Input: json.RawMessage(`"use the tool"`),
	})
	if res.Err != nil {
		t.Fatalf("invoke %s: %v", handlerName, res.Err)
	}
	if comp.toolResult == "" {
		t.Fatalf("handler %s produced no nested tool result", handlerName)
	}
	return comp.toolResult
}

// TestSessionDispatcherPropagatesToolResultCap pins that the cap handed to
// NewSessionDispatcher reaches both the multi_step handler and skill handlers.
func TestSessionDispatcherPropagatesToolResultCap(t *testing.T) {
	for _, handler := range []string{"multi_step", "cap-probe-skill"} {
		t.Run(handler, func(t *testing.T) {
			capped := invokeNestedHandler(t, handler, 2048)
			if len(capped) > 2048 {
				t.Fatalf("nested tool result via %s is %d bytes, exceeds cap 2048", handler, len(capped))
			}
			if !strings.Contains(capped, "(truncated") {
				t.Fatalf("truncation marker missing via %s", handler)
			}

			uncapped := invokeNestedHandler(t, handler, 0)
			if len(uncapped) != 50*1024 {
				t.Fatalf("with cap 0 nested tool result via %s is %d bytes, want %d",
					handler, len(uncapped), 50*1024)
			}
		})
	}
}
