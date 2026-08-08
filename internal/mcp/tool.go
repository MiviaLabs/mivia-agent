package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type discoveredTool struct {
	name, remoteName, description string
	schema                        map[string]any
	client                        remoteClient
	maxResultBytes                int
	timeout                       time.Duration
	redaction                     *redact.Policy
}

func (t discoveredTool) Name() string               { return t.name }
func (t discoveredTool) Description() string        { return t.description }
func (t discoveredTool) Parameters() map[string]any { return t.schema }
func (t discoveredTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var values map[string]any
	if err := json.Unmarshal(args, &values); err != nil {
		return "", fmt.Errorf("decode MCP arguments: %w", err)
	}
	if t.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.timeout)
		defer cancel()
	}
	result, err := t.client.CallTool(ctx, t.remoteName, values)
	if err != nil {
		return "", err
	}
	return capMCPResult(t.redaction.Text(result), t.maxResultBytes), nil
}

func wrapRemoteTools(serverID string, client remoteClient, remote []remoteTool, maxDescriptionBytes, maxSchemaBytes, maxResultBytes, timeoutSeconds int, redaction *redact.Policy) ([]tools.Tool, error) {
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
		description, schema, err := sanitizeToolMetadata(tool.Description, tool.Schema, maxDescriptionBytes, maxSchemaBytes, redaction)
		if err != nil {
			return nil, fmt.Errorf("MCP tool %q: %w", tool.Name, err)
		}
		out = append(out, discoveredTool{name: name, remoteName: tool.Name, description: description, schema: schema, client: client, maxResultBytes: maxResultBytes, timeout: time.Duration(timeoutSeconds) * time.Second, redaction: redaction})
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
