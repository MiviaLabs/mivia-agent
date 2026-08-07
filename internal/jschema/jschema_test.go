package jschema_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/jschema"
)

func TestCompileAndValidateOK(t *testing.T) {
	sch, err := jschema.Compile(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ok": map[string]any{"type": "boolean"},
		},
		"required":             []any{"ok"},
		"additionalProperties": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	inst, err := sch.ValidateJSONBytes([]byte(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := inst.(map[string]any)
	if !ok || m["ok"] != true {
		t.Fatalf("instance = %#v", inst)
	}
}

func TestRejectRemoteRef(t *testing.T) {
	_, err := jschema.Compile(map[string]any{
		"$ref": "https://example.com/schema.json",
	})
	if err == nil {
		t.Fatal("want remote/admission reject")
	}
	if !strings.Contains(err.Error(), "admission") && !strings.Contains(err.Error(), "remote") {
		t.Fatalf("want remote/admission reject, got %v", err)
	}
}

func TestRejectOversizedSchema(t *testing.T) {
	_, err := jschema.Compile(map[string]any{
		"type":        "object",
		"description": strings.Repeat("x", jschema.MaxSchemaBytes+1),
	})
	if err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("want size reject, got %v", err)
	}
}

func TestRejectDeepSchema(t *testing.T) {
	var node any = map[string]any{"type": "string"}
	for i := 0; i < jschema.MaxSchemaDepth+5; i++ {
		node = map[string]any{"type": "object", "properties": map[string]any{"c": node}}
	}
	_, err := jschema.Compile(node.(map[string]any))
	if err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("want depth reject, got %v", err)
	}
}

func TestStripOneCodeFence(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`{"a":1}`, `{"a":1}`},
		{"```json\n{\"a\":1}\n```", `{"a":1}`},
		{"```\n{\"a\":1}\n```", `{"a":1}`},
		// Nested fences: leave untouched.
		{"```\n```inner```\n```", "```\n```inner```\n```"},
	}
	for _, tc := range cases {
		got := jschema.StripOneCodeFence(tc.in)
		if strings.TrimSpace(got) != strings.TrimSpace(tc.want) {
			t.Fatalf("StripOneCodeFence(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatCorrectiveBounded(t *testing.T) {
	msg := jschema.FormatCorrective(jschema.ErrValidation, nil)
	if len(msg) > jschema.MaxCorrectiveBytes {
		t.Fatalf("corrective %d bytes over cap", len(msg))
	}
	if !strings.Contains(msg, "JSON schema") {
		t.Fatalf("corrective missing guidance: %q", msg)
	}
}

func TestPromptAppendixRendersAContractNotTheSchemaDocument(t *testing.T) {
	schema := map[string]any{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"title":       "Review output",
		"description": "A review verdict with findings.",
		"type":        "object",
		"required":    []any{"verdict", "inspected"},
		"properties": map[string]any{
			"verdict":   map[string]any{"type": "string", "enum": []any{"approved", "changes_requested"}},
			"inspected": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	}
	appendix := jschema.PromptAppendix(schema)
	for _, key := range []string{`"$schema"`, `"title"`, `"description"`} {
		if strings.Contains(appendix, key) {
			t.Fatalf("PromptAppendix leaked the %s meta-key: %q", key, appendix)
		}
	}
	if !strings.Contains(appendix, "never the schema document") {
		t.Fatalf("PromptAppendix must carry the never-echo instruction: %q", appendix)
	}
	if !strings.Contains(appendix, "Example: ") {
		t.Fatalf("PromptAppendix should include a compact filled example: %q", appendix)
	}
	// The schema part (the final line) must stay a valid JSON object.
	lines := strings.Split(strings.TrimSpace(appendix), "\n")
	var doc map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &doc); err != nil {
		t.Fatalf("appendix schema part is not valid JSON: %v\nappendix=%q", err, appendix)
	}
	if doc["type"] != "object" || doc["required"] == nil {
		t.Fatalf("appendix schema part lost the instance shape: %#v", doc)
	}
	if _, ok := doc["properties"].(map[string]any); !ok {
		t.Fatalf("appendix schema part lost its properties: %#v", doc)
	}
}

func TestFormatCorrectiveWithSchemaHidesReviewSchemaMetaKeys(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".mivia", "workflows", "schemas", "review-v1.json"))
	if err != nil {
		t.Skipf("committed review-v1.json is not present: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema["$schema"]; !ok {
		t.Fatalf("test fixture review-v1.json must carry the $schema meta-key")
	}
	msg := jschema.FormatCorrectiveWithSchema(errors.New("missing properties 'verdict'"), schema, nil)
	if strings.Contains(msg, `"$schema"`) {
		t.Fatalf("corrective leaked the $schema meta-key: %q", msg)
	}
	if !strings.Contains(msg, "never the schema document") {
		t.Fatalf("corrective must carry the never-echo instruction: %q", msg)
	}
	if !strings.Contains(msg, `"required"`) || !strings.Contains(msg, "verdict") {
		t.Fatalf("corrective lost the instance shape: %q", msg)
	}
}
