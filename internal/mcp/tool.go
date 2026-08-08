package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type discoveredTool struct {
	name, remoteName, description string
	schema                        map[string]any
	client                        remoteClient
}

func (t discoveredTool) Name() string               { return t.name }
func (t discoveredTool) Description() string        { return t.description }
func (t discoveredTool) Parameters() map[string]any { return t.schema }
func (t discoveredTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var values map[string]any
	if err := json.Unmarshal(args, &values); err != nil {
		return "", fmt.Errorf("decode MCP arguments: %w", err)
	}
	return t.client.CallTool(ctx, t.remoteName, values)
}

func wrapRemoteTools(serverID string, client remoteClient, remote []remoteTool) ([]tools.Tool, error) {
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
		out = append(out, discoveredTool{name: name, remoteName: tool.Name, description: tool.Description, schema: tool.Schema, client: client})
	}
	return out, nil
}
