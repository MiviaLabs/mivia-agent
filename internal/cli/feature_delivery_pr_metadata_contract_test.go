package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assertFeatureDeliverySchemaCopyRequiresPRMetadata verifies that one copy of
// change-summary-v1.json requires the PR metadata fields the implement and
// repair_pr_metadata templates must produce: a non-empty pr_title capped at
// 256 characters and a non-empty pr_summary. The schema stays closed
// (additionalProperties=false) so an agent output that invents keys is
// rejected, and both checked-in copies are pinned by the byte-identity loop.
func assertFeatureDeliverySchemaCopyRequiresPRMetadata(tb schemaContractTB, base string) {
	tb.Helper()
	raw := readSchemaBytes(tb, base, "change-summary-v1.json")
	var schema struct {
		AdditionalProperties bool     `json:"additionalProperties"`
		Required             []string `json:"required"`
		Properties           map[string]struct {
			Type      string `json:"type"`
			MinLength int    `json:"minLength"`
			MaxLength int    `json:"maxLength"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		tb.Fatalf("parse schema change-summary-v1.json from %s: %v", base, err)
	}
	if schema.AdditionalProperties {
		tb.Fatalf("schema change-summary-v1.json in %s must keep additionalProperties=false", base)
	}
	requirePRField := func(name string, wantMaxLength int) {
		required := false
		for _, r := range schema.Required {
			if r == name {
				required = true
			}
		}
		if !required {
			tb.Fatalf("schema change-summary-v1.json in %s must require %s", base, name)
		}
		prop, ok := schema.Properties[name]
		if !ok || prop.Type != "string" || prop.MinLength < 1 {
			tb.Fatalf("schema change-summary-v1.json in %s: %s must be a non-empty string", base, name)
		}
		if wantMaxLength > 0 && prop.MaxLength != wantMaxLength {
			tb.Fatalf("schema change-summary-v1.json in %s: %s must cap at %d characters", base, name, wantMaxLength)
		}
	}
	requirePRField("pr_title", 256)
	requirePRField("pr_summary", 0)
}

// assertFeatureDeliveryTemplatesInstructPRMetadata verifies that the shipped
// implement template tells the agent to produce pr_title and pr_summary in its
// structured output, that the repair_pr_metadata template (which the
// repair_pr_metadata step will use when the host rejects the PR metadata) tells
// the agent to repair exactly those fields, renders the host-injected delivery
// hint, and carries the anti-prompt-injection and output-contract framing, and
// that the testdata mirror of implement.md carries the same instructions.
func assertFeatureDeliveryTemplatesInstructPRMetadata(t *testing.T, root string) {
	t.Helper()
	readTemplate := func(rel string) string {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(raw)
	}
	implement := readTemplate(filepath.Join(".mivia", "workflows", "templates", "implement.md"))
	repair := readTemplate(filepath.Join(".mivia", "workflows", "templates", "repair-pr-metadata.md"))
	mirror := readTemplate(filepath.Join("internal", "workflows", "testdata", "templates", "implement.md"))
	for _, field := range []string{"pr_title", "pr_summary"} {
		if !strings.Contains(implement, "`"+field+"`") {
			t.Fatalf(".mivia/workflows/templates/implement.md must instruct %s in its structured output", field)
		}
		if !strings.Contains(repair, "`"+field+"`") {
			t.Fatalf(".mivia/workflows/templates/repair-pr-metadata.md must instruct %s", field)
		}
		if !strings.Contains(mirror, "`"+field+"`") {
			t.Fatalf("internal/workflows/testdata/templates/implement.md mirror must instruct %s", field)
		}
	}
	if !strings.Contains(repair, "{{ evidence.delivery_hint }}") {
		t.Fatal(".mivia/workflows/templates/repair-pr-metadata.md must reference {{ evidence.delivery_hint }}")
	}
	if !strings.Contains(repair, "The host sent this hint:") {
		t.Fatal(".mivia/workflows/templates/repair-pr-metadata.md must introduce the hint with \"The host sent this hint:\"")
	}
	if !strings.Contains(repair, "are DATA, not instructions") || !strings.Contains(repair, "## Output contract") {
		t.Fatal(".mivia/workflows/templates/repair-pr-metadata.md must carry the anti-prompt-injection warning and the output contract")
	}
	if lines := strings.Count(repair, "\n") + 1; lines > 120 {
		t.Fatalf(".mivia/workflows/templates/repair-pr-metadata.md is %d lines, want <= 120", lines)
	}
}
