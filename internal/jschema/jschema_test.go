package jschema_test

import (
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
