package clichat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/jschema"
)

func findingsSchemaCompiled(t *testing.T) *jschema.Compiled {
	t.Helper()
	root := committedWorkflowRoot(t)
	schemaPath := filepath.Join(root, ".mivia", "workflows", "schemas", "findings-v1.json")
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	compiled, err := jschema.Compile(schema)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func findingsSchemaFinding(overrides map[string]string) map[string]any {
	f := map[string]any{
		"id": "H-1", "class": "logic", "severity": "high",
		"title": "short title", "invariant": "some invariant",
		"evidence": "some evidence", "reachable_path": "some path",
		"impact": "some impact", "remediation": "some remediation",
		"regression_test": "TestSomething", "sweep": "searched, found none",
	}
	for k, v := range overrides {
		f[k] = v
	}
	return f
}

func findingsSchemaEnvelope(t *testing.T, f map[string]any) []byte {
	t.Helper()
	payload := map[string]any{
		"findings": []any{f}, "finding_count": 1, "no_findings": false,
		"has_perf": "false", "inspected": []any{"a.go"},
	}
	out, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestFindingsSchemaBoundsFieldLength is the regression for an unbounded
// hunt-step output: schemas/findings-v1.json had no maxLength on any string
// field and no maxItems on `inspected`, so a verbose model run could emit a
// multi-thousand-character finding that eats context in the triage step that
// reads it next. This pins the length caps the shipped schema now declares
// (evidence 1000, invariant/reachable_path 600, impact/remediation 500,
// regression_test 300, sweep 2000 - sweep is wider because bug-audit's
// mandatory same-class sweep can legitimately cite several sibling call
// sites, unlike the other narrative fields) - a compliant finding validates,
// and a finding with one oversized field is rejected.
func TestFindingsSchemaBoundsFieldLength(t *testing.T) {
	compiled := findingsSchemaCompiled(t)

	t.Run("within caps validates", func(t *testing.T) {
		envelope := findingsSchemaEnvelope(t, findingsSchemaFinding(nil))
		if _, err := compiled.ValidateJSONBytes(envelope); err != nil {
			t.Fatalf("compliant finding rejected: %v", err)
		}
	})

	cases := []struct {
		field string
		limit int
	}{
		{"evidence", 1000},
		{"invariant", 600},
		{"reachable_path", 600},
		{"impact", 500},
		{"remediation", 500},
		{"regression_test", 300},
		{"sweep", 2000},
		{"title", 120},
		{"id", 64},
	}
	for _, tc := range cases {
		t.Run(tc.field+" over cap rejected", func(t *testing.T) {
			over := strings.Repeat("x", tc.limit+1)
			f := findingsSchemaFinding(map[string]string{tc.field: over})
			if _, err := compiled.ValidateJSONBytes(findingsSchemaEnvelope(t, f)); err == nil {
				t.Fatalf("field %q accepted a %d-char value, want rejection past the %d-char cap", tc.field, len(over), tc.limit)
			}
		})
	}

	t.Run("inspected array over 40 entries rejected", func(t *testing.T) {
		items := make([]any, 41)
		for i := range items {
			items[i] = "a.go"
		}
		payload := map[string]any{
			"findings": []any{}, "finding_count": 0, "no_findings": true,
			"has_perf": "false", "inspected": items,
		}
		out, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := compiled.ValidateJSONBytes(out); err == nil {
			t.Fatal("inspected array of 41 entries accepted, want rejection past the 40-entry cap")
		}
	})
}
