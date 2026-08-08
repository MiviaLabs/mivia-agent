package mcp

import "testing"

func TestSanitizeToolMetadataBoundsUntrustedValues(t *testing.T) {
	if _, _, err := sanitizeToolMetadata("too long", map[string]any{"type": "object"}, 3, 100); err == nil {
		t.Fatal("sanitizeToolMetadata() accepted an oversized description")
	}
	schema := map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}}
	description, got, err := sanitizeToolMetadata("line\nvalue", schema, 100, 1000)
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
	if _, _, err := sanitizeToolMetadata("ok", root, 100, 10000); err == nil {
		t.Fatal("sanitizeToolMetadata() accepted a deep schema")
	}
}
