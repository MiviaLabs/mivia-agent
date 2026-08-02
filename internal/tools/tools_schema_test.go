package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRegistryRejectsMalformedAndNonObjectArguments(t *testing.T) {
	_, reg := setupWS(t)
	for _, raw := range []json.RawMessage{json.RawMessage(`{"`), json.RawMessage(`[]`), json.RawMessage(`null`)} {
		if _, err := reg.Execute(context.Background(), "read_file", raw); err == nil {
			t.Fatalf("expected argument validation error for %s", raw)
		}
	}
}

func TestRegistryValidatesDeclaredSchema(t *testing.T) {
	_, reg := setupWS(t)
	cases := []json.RawMessage{
		json.RawMessage(`{"path":"a.txt","unexpected":true}`),
		json.RawMessage(`{"content":"x"}`),
		json.RawMessage(`{"path":false,"content":"x"}`),
	}
	for _, raw := range cases {
		if _, err := reg.Execute(context.Background(), "write_file", raw); err == nil {
			t.Fatalf("expected schema error for %s", raw)
		}
	}
}

type schemaProbeTool struct{ called bool }

func (t *schemaProbeTool) Name() string        { return "schema_probe" }
func (t *schemaProbeTool) Description() string { return "schema probe" }
func (t *schemaProbeTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"count": map[string]any{"type": "integer"},
		"mode":  map[string]any{"type": "string", "enum": []string{"safe", "fast"}},
	}, []string{"count", "mode"})
}
func (t *schemaProbeTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.called = true
	return "called", nil
}

func TestRegistryRejectsFractionalIntegerAndInvalidEnum(t *testing.T) {
	reg := NewRegistry()
	probe := &schemaProbeTool{}
	reg.Register(probe)
	for _, raw := range []string{`{"count":1.5,"mode":"safe"}`, `{"count":1,"mode":"unsafe"}`} {
		if _, err := reg.Execute(context.Background(), probe.Name(), json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid arguments: %s", raw)
		}
	}
	if probe.called {
		t.Fatal("schema-invalid input reached Execute")
	}
}

// Probe tools for validateSchema extension tests.

type schemaProbeMinTool struct{ called bool }

func (t *schemaProbeMinTool) Name() string        { return "probe_min" }
func (t *schemaProbeMinTool) Description() string { return "min probe" }
func (t *schemaProbeMinTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"count": map[string]any{"type": "integer", "minimum": float64(1)},
	}, []string{"count"})
}
func (t *schemaProbeMinTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.called = true
	return "called", nil
}

type schemaProbeMaxTool struct{ called bool }

func (t *schemaProbeMaxTool) Name() string        { return "probe_max" }
func (t *schemaProbeMaxTool) Description() string { return "max probe" }
func (t *schemaProbeMaxTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"count": map[string]any{"type": "integer", "maximum": float64(10)},
	}, []string{"count"})
}
func (t *schemaProbeMaxTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.called = true
	return "called", nil
}

type schemaProbeMinItemsTool struct{ called bool }

func (t *schemaProbeMinItemsTool) Name() string        { return "probe_min_items" }
func (t *schemaProbeMinItemsTool) Description() string { return "min items probe" }
func (t *schemaProbeMinItemsTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"items": map[string]any{
			"type": "array", "items": map[string]any{"type": "string"},
			"minItems": float64(1),
		},
	}, []string{"items"})
}
func (t *schemaProbeMinItemsTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.called = true
	return "called", nil
}

type schemaProbeMaxItemsTool struct{ called bool }

func (t *schemaProbeMaxItemsTool) Name() string        { return "probe_max_items" }
func (t *schemaProbeMaxItemsTool) Description() string { return "max items probe" }
func (t *schemaProbeMaxItemsTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"items": map[string]any{
			"type": "array", "items": map[string]any{"type": "string"},
			"maxItems": float64(2),
		},
	}, []string{"items"})
}
func (t *schemaProbeMaxItemsTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.called = true
	return "called", nil
}

type schemaProbeItemEnumTool struct{ called bool }

func (t *schemaProbeItemEnumTool) Name() string        { return "probe_item_enum" }
func (t *schemaProbeItemEnumTool) Description() string { return "item enum probe" }
func (t *schemaProbeItemEnumTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"roles": map[string]any{
			"type":  "array",
			"items": map[string]any{"type": "string", "enum": []string{"definition", "caller", "return"}},
		},
	}, []string{"roles"})
}
func (t *schemaProbeItemEnumTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.called = true
	return "called", nil
}

func TestOpenAIToolsSchemaValidRequiredArrays(t *testing.T) {
	_, reg := setupWS(t)
	tools := reg.OpenAITools()
	if len(tools) < 8 {
		t.Fatalf("tools=%d", len(tools))
	}
	raw, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	// DeepSeek rejected null required arrays; ensure we never emit "required":null
	if strings.Contains(string(raw), `"required":null`) {
		t.Fatalf("schema has null required: %s", raw)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, tool := range decoded {
		fn, _ := tool["function"].(map[string]any)
		params, _ := fn["parameters"].(map[string]any)
		req, ok := params["required"]
		if !ok {
			continue
		}
		if req == nil {
			t.Fatalf("null required in %v", fn["name"])
		}
		if _, ok := req.([]any); !ok {
			t.Fatalf("required not array for %v: %T", fn["name"], req)
		}
	}
}

func TestValidateSchemaEnforcesMinimum(t *testing.T) {
	reg := NewRegistry()
	probe := &schemaProbeMinTool{}
	reg.Register(probe)
	// count=0 should fail (minimum:1)
	_, err := reg.Execute(context.Background(), probe.Name(), json.RawMessage(`{"count":0}`))
	if err == nil {
		t.Fatal("accepted count=0 with minimum:1")
	}
	if !strings.Contains(err.Error(), ">= 1") {
		t.Fatalf("wrong error: %v", err)
	}
	// count=1 should pass
	_, err = reg.Execute(context.Background(), probe.Name(), json.RawMessage(`{"count":1}`))
	if err != nil {
		t.Fatalf("rejected count=1: %v", err)
	}
}

func TestValidateSchemaEnforcesMaximum(t *testing.T) {
	reg := NewRegistry()
	probe := &schemaProbeMaxTool{}
	reg.Register(probe)
	_, err := reg.Execute(context.Background(), probe.Name(), json.RawMessage(`{"count":11}`))
	if err == nil {
		t.Fatal("accepted count=11 with maximum:10")
	}
	if !strings.Contains(err.Error(), "<= 10") {
		t.Fatalf("wrong error: %v", err)
	}
	_, err = reg.Execute(context.Background(), probe.Name(), json.RawMessage(`{"count":10}`))
	if err != nil {
		t.Fatalf("rejected count=10: %v", err)
	}
}

func TestValidateSchemaEnforcesMinItems(t *testing.T) {
	reg := NewRegistry()
	probe := &schemaProbeMinItemsTool{}
	reg.Register(probe)
	_, err := reg.Execute(context.Background(), probe.Name(), json.RawMessage(`{"items":[]}`))
	if err == nil {
		t.Fatal("accepted empty array with minItems:1")
	}
	if !strings.Contains(err.Error(), ">= 1 items") {
		t.Fatalf("wrong error: %v", err)
	}
	_, err = reg.Execute(context.Background(), probe.Name(), json.RawMessage(`{"items":["a"]}`))
	if err != nil {
		t.Fatalf("rejected 1-item array: %v", err)
	}
}

func TestValidateSchemaEnforcesMaxItems(t *testing.T) {
	reg := NewRegistry()
	probe := &schemaProbeMaxItemsTool{}
	reg.Register(probe)
	_, err := reg.Execute(context.Background(), probe.Name(), json.RawMessage(`{"items":["a","b","c"]}`))
	if err == nil {
		t.Fatal("accepted 3-item array with maxItems:2")
	}
	if !strings.Contains(err.Error(), "<= 2 items") {
		t.Fatalf("wrong error: %v", err)
	}
	_, err = reg.Execute(context.Background(), probe.Name(), json.RawMessage(`{"items":["a","b"]}`))
	if err != nil {
		t.Fatalf("rejected 2-item array: %v", err)
	}
}

// TestRequiredFieldsFromAnySlice covers the JSON-decoded []any form of
// requiredFields: non-string entries are skipped so only field names survive.
func TestRequiredFieldsFromAnySlice(t *testing.T) {
	schema := map[string]any{
		"required": []any{"field1", 123, "field2"},
	}
	got := requiredFields(schema)
	want := []string{"field1", "field2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("requiredFields(%v) = %v, want %v", schema["required"], got, want)
	}
}

// TestValidateNumberBounds exercises validateNumberBounds directly: minimum
// violations, maximum violations, and the no-bounds pass.
func TestValidateNumberBounds(t *testing.T) {
	err := validateNumberBounds("n", float64(5), map[string]any{"minimum": float64(10)})
	if err == nil {
		t.Fatal("expected minimum violation error")
	}
	if !strings.Contains(err.Error(), ">= 10") {
		t.Fatalf("wrong minimum error: %v", err)
	}

	err = validateNumberBounds("n", float64(5), map[string]any{"maximum": float64(3)})
	if err == nil {
		t.Fatal("expected maximum violation error")
	}
	if !strings.Contains(err.Error(), "<= 3") {
		t.Fatalf("wrong maximum error: %v", err)
	}

	if err := validateNumberBounds("n", float64(5), map[string]any{}); err != nil {
		t.Fatalf("no bounds should pass, got: %v", err)
	}
}

// TestValidateArrayConstraints exercises validateArrayConstraints directly: a
// minItems violation where the array has fewer items than required.
func TestValidateArrayConstraints(t *testing.T) {
	err := validateArrayConstraints("items", []any{"a"}, map[string]any{"minItems": float64(3)})
	if err == nil {
		t.Fatal("expected minItems violation error")
	}
	if !strings.Contains(err.Error(), ">= 3 items") {
		t.Fatalf("wrong minItems error: %v", err)
	}
}

func TestValidateSchemaEnforcesEnumOnArrayItems(t *testing.T) {
	reg := NewRegistry()
	probe := &schemaProbeItemEnumTool{}
	reg.Register(probe)
	_, err := reg.Execute(context.Background(), probe.Name(), json.RawMessage(`{"roles":["definition","bogus"]}`))
	if err == nil {
		t.Fatal("accepted invalid item enum value")
	}
	if !strings.Contains(err.Error(), "declared values") {
		t.Fatalf("wrong error: %v", err)
	}
	_, err = reg.Execute(context.Background(), probe.Name(), json.RawMessage(`{"roles":["definition","caller"]}`))
	if err != nil {
		t.Fatalf("rejected valid item enum: %v", err)
	}
}
