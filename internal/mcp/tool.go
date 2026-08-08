package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type discoveredTool struct {
	name, remoteName, description string
	schema                        map[string]any
	client                        remoteClient
	maxResultBytes                int
}

func (t discoveredTool) Name() string               { return t.name }
func (t discoveredTool) Description() string        { return t.description }
func (t discoveredTool) Parameters() map[string]any { return t.schema }
func (t discoveredTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var values map[string]any
	if err := json.Unmarshal(args, &values); err != nil {
		return "", fmt.Errorf("decode MCP arguments: %w", err)
	}
	result, err := t.client.CallTool(ctx, t.remoteName, values)
	if err != nil {
		return "", err
	}
	return capMCPResult(result, t.maxResultBytes), nil
}

func wrapRemoteTools(serverID string, client remoteClient, remote []remoteTool, maxResultBytes int) ([]tools.Tool, error) {
	out := make([]tools.Tool, 0, len(remote))
	seen := map[string]bool{}
	for _, tool := range remote {
		name, err := EncodeToolName(serverID, tool.Name)
		if err != nil {
			return nil, err
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate MCP tool %q", name)
		}
		seen[name] = true
		out = append(out, discoveredTool{name: name, remoteName: tool.Name, description: tool.Description, schema: tool.Schema, client: client, maxResultBytes: maxResultBytes})
	}
	return out, nil
}

func capMCPResult(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "\n[MCP result truncated]"
}
