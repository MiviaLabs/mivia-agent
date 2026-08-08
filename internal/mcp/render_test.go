package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestSanitizeToolMetadataBoundsUntrustedValues(t *testing.T) {
	if _, _, err := sanitizeToolMetadata("too long", map[string]any{"type": "object"}, 3, 100, nil); err == nil {
		t.Fatal("sanitizeToolMetadata() accepted an oversized description")
	}
	schema := map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}}
	description, got, err := sanitizeToolMetadata("line\nvalue", schema, 100, 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if description != "line value" || got["type"] != "object" {
		t.Fatalf("sanitized metadata = %q %#v", description, got)
	}
}

func TestSanitizeToolMetadataBridgesUnsupportedSchema(t *testing.T) {
	schema := map[string]any{"$ref": "https://untrusted.invalid/schema"}
	_, got, err := sanitizeToolMetadata("tool", schema, 100, 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["type"] != "object" || got["additionalProperties"] != true || len(got) != 2 {
		t.Fatalf("bridged schema = %#v", got)
	}
}

func TestSanitizeToolMetadataCopiesSafeSchema(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}}
	_, got, err := sanitizeToolMetadata("tool", schema, 100, 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	properties := got["properties"].(map[string]any)
	if properties["name"].(map[string]any)["type"] != "string" {
		t.Fatalf("bridged schema = %#v", got)
	}
	properties["name"].(map[string]any)["type"] = "changed"
	if schema["properties"].(map[string]any)["name"].(map[string]any)["type"] != "string" {
		t.Fatal("bridge returned a mutable source schema")
	}
}

func TestSanitizeToolMetadataRejectsDeepSchema(t *testing.T) {
	value := map[string]any{}
	root := value
	for range maxSchemaDepth + 1 {
		next := map[string]any{}
		value["nested"] = next
		value = next
	}
	if _, _, err := sanitizeToolMetadata("ok", root, 100, 10000, nil); err == nil {
		t.Fatal("sanitizeToolMetadata() accepted a deep schema")
	}
}

func TestSanitizeToolMetadataAppliesRedaction(t *testing.T) {
	policy, err := redact.Compile([]string{"secret"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	description, _, err := sanitizeToolMetadata("secret value", nil, 100, 100, policy)
	if err != nil {
		t.Fatal(err)
	}
	if description == "secret value" {
		t.Fatal("sanitizeToolMetadata() did not redact the description")
	}
}

func TestDiscoveredToolRedactsResult(t *testing.T) {
	policy, err := redact.Compile([]string{"secret"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	tool := discoveredTool{remoteName: "result", client: resultClient{}, redaction: policy}
	got, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got == "secret value" {
		t.Fatal("discoveredTool.Execute() did not redact result text")
	}
}

func TestDiscoveredToolUsesExternalServerCapability(t *testing.T) {
	tool := discoveredTool{serverID: "repository", timeout: 3 * time.Second, maxResultBytes: 100}
	capability := tool.Capability(nil)
	if capability.Class != tools.ExecutionExternal || capability.ResourceKey != "mcp:repository" || capability.Timeout != 3*time.Second {
		t.Fatalf("Capability() = %#v", capability)
	}
	if tool.ResultBudgetBytes() != 100 {
		t.Fatalf("ResultBudgetBytes() = %d, want 100", tool.ResultBudgetBytes())
	}
}

func TestDiscoveredToolHidesRemoteError(t *testing.T) {
	tool := discoveredTool{remoteName: "result", client: failingResultClient{}}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil || err.Error() != "MCP tool call failed" {
		t.Fatalf("Execute() error = %v", err)
	}
}

type resultClient struct{}

func (resultClient) ListTools(context.Context) ([]remoteTool, error) { return nil, nil }
func (resultClient) CallTool(context.Context, string, map[string]any) (string, error) {
	return "secret value", nil
}
func (resultClient) Close() error { return nil }

type failingResultClient struct{}

func (failingResultClient) ListTools(context.Context) ([]remoteTool, error) { return nil, nil }
func (failingResultClient) CallTool(context.Context, string, map[string]any) (string, error) {
	return "", errors.New("untrusted server diagnostic")
}
func (failingResultClient) Close() error { return nil }
