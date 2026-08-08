package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
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

type resultClient struct{}

func (resultClient) ListTools(context.Context) ([]remoteTool, error) { return nil, nil }
func (resultClient) CallTool(context.Context, string, map[string]any) (string, error) {
	return "secret value", nil
}
func (resultClient) Close() error { return nil }
