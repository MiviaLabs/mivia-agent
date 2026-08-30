package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type discoveredTool struct {
	name, serverID, remoteName, description string
	schema                                  map[string]any
	client                                  remoteClient
	maxResultBytes                          int
	timeout                                 time.Duration
	redaction                               *redact.Policy
}

func (t discoveredTool) Name() string               { return t.name }
func (t discoveredTool) Description() string        { return t.description }
func (t discoveredTool) Parameters() map[string]any { return t.schema }
func (t discoveredTool) ResultBudgetBytes() int     { return t.maxResultBytes }
func (t discoveredTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{
		Class:       tools.ExecutionExternal,
		ResourceKey: "mcp:" + t.serverID,
		Timeout:     t.timeout,
	}
}
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
		// Preserve the cancellation/timeout identity so the runtime can stamp
		// "canceled"/"timed_out" instead of a generic "failed" (DC-9). The
		// sentinels carry no external content; server-owned error text stays
		// hidden on every branch.
		if errors.Is(err, context.Canceled) {
			return "", context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", context.DeadlineExceeded
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("MCP tool call failed")
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
		description, err := composeToolDescription(serverID, tool.Name, tool.Description, maxDescriptionBytes, redaction)
		if err != nil {
			return nil, fmt.Errorf("MCP tool %q: %w", tool.Name, err)
		}
		schema, err := sanitizeToolSchema(tool.Schema, maxSchemaBytes, redaction)
		if err != nil {
			return nil, fmt.Errorf("MCP tool %q: %w", tool.Name, err)
		}
		out = append(out, discoveredTool{name: name, serverID: serverID, remoteName: tool.Name, description: description, schema: schema, client: client, maxResultBytes: maxResultBytes, timeout: config.SaturatingSeconds(timeoutSeconds), redaction: redaction})
	}
	return out, nil
}

func capMCPResult(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	marker := "\n[MCP result truncated]"
	if limit <= len(marker) {
		return marker[:limit]
	}
	cut := limit - len(marker)
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + marker
}
