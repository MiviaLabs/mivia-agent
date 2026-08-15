package compiler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// schemaTestWorkflow returns a minimal WorkflowFile for schema reference tests.
func schemaTestWorkflow(stepID, schemaPath string) *definition.WorkflowFile {
	return &definition.WorkflowFile{
		Version:     1,
		Name:        "test-schema-refs",
		Description: "test",
		InitialStep: stepID,
		Steps: []definition.Step{
			{ID: stepID, Kind: "agent", Agent: "test", OutputSchema: schemaPath},
		},
		Transitions: []definition.Transition{
			{From: stepID, To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
}

func TestValidateSchemaReferences_SchemaFound(t *testing.T) {
	tmpDir := t.TempDir()
	schemasDir := filepath.Join(tmpDir, "schemas")
	if err := os.MkdirAll(schemasDir, 0755); err != nil {
		t.Fatalf("creating schemas dir: %v", err)
	}

	schemaContent := `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "verdict": { "type": "string" }
  },
  "required": ["verdict"],
  "additionalProperties": false
}`
	if err := os.WriteFile(filepath.Join(schemasDir, "valid.json"), []byte(schemaContent), 0644); err != nil {
		t.Fatalf("creating schema file: %v", err)
	}

	wf := schemaTestWorkflow("step1", "schemas/valid.json")
	errs := ValidateSchemaReferences(wf, tmpDir)
	if len(errs) > 0 {
		t.Fatalf("unexpected error for valid schema: %v", strings.Join(errs, "; "))
	}
}

func TestValidateSchemaReferences_SchemaNotFound(t *testing.T) {
	tmpDir := t.TempDir()

	wf := schemaTestWorkflow("step1", "schemas/nonexistent.json")
	errs := ValidateSchemaReferences(wf, tmpDir)
	if len(errs) == 0 {
		t.Fatal("expected error for nonexistent schema file")
	}
	if strings.Join(errs, "; ") != `step "step1": output_schema "schemas/nonexistent.json": file not found` {
		t.Errorf("unexpected error: %v", strings.Join(errs, "; "))
	}
}

func TestValidateSchemaReferences_PathTraversal(t *testing.T) {
	tmpDir := t.TempDir()

	wf := schemaTestWorkflow("step1", "../etc/passwd")
	errs := ValidateSchemaReferences(wf, tmpDir)
	if len(errs) == 0 {
		t.Fatal("expected error for path traversal in schema")
	}
	if strings.Join(errs, "; ") != `step "step1": output_schema "../etc/passwd": path traversal not allowed` {
		t.Errorf("unexpected error: %v", strings.Join(errs, "; "))
	}
}

func TestValidateSchemaReferences_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	schemasDir := filepath.Join(tmpDir, "schemas")
	if err := os.MkdirAll(schemasDir, 0755); err != nil {
		t.Fatalf("creating schemas dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(schemasDir, "bad.json"), []byte("not json at all"), 0644); err != nil {
		t.Fatalf("creating schema file: %v", err)
	}

	wf := schemaTestWorkflow("step1", "schemas/bad.json")
	errs := ValidateSchemaReferences(wf, tmpDir)
	if len(errs) == 0 {
		t.Fatal("expected error for invalid JSON schema")
	}
	joined := strings.Join(errs, "; ")
	expected := `step "step1": output_schema "schemas/bad.json": invalid JSON`
	if !strings.Contains(joined, expected) {
		t.Errorf("error %q should contain %q", joined, expected)
	}
}

func TestValidateSchemaReferences_AdditionalPropertiesTrue(t *testing.T) {
	tmpDir := t.TempDir()
	schemasDir := filepath.Join(tmpDir, "schemas")
	if err := os.MkdirAll(schemasDir, 0755); err != nil {
		t.Fatalf("creating schemas dir: %v", err)
	}

	schemaContent := `{
  "type": "object",
  "additionalProperties": true
}`
	if err := os.WriteFile(filepath.Join(schemasDir, "open.json"), []byte(schemaContent), 0644); err != nil {
		t.Fatalf("creating schema file: %v", err)
	}

	wf := schemaTestWorkflow("step1", "schemas/open.json")
	errs := ValidateSchemaReferences(wf, tmpDir)
	if len(errs) == 0 {
		t.Fatal("expected error for additionalProperties: true")
	}
	joined := strings.Join(errs, "; ")
	if !strings.Contains(joined, "additionalProperties must not be true") {
		t.Errorf("error %q should mention additionalProperties", joined)
	}
}

func TestValidateSchemaReferences_MissingAdditionalProperties(t *testing.T) {
	tmpDir := t.TempDir()
	schemasDir := filepath.Join(tmpDir, "schemas")
	if err := os.MkdirAll(schemasDir, 0755); err != nil {
		t.Fatalf("creating schemas dir: %v", err)
	}

	schemaContent := `{
  "type": "object",
  "properties": {
    "verdict": { "type": "string" }
  }
}`
	if err := os.WriteFile(filepath.Join(schemasDir, "no-ap.json"), []byte(schemaContent), 0644); err != nil {
		t.Fatalf("creating schema file: %v", err)
	}

	wf := schemaTestWorkflow("step1", "schemas/no-ap.json")
	errs := ValidateSchemaReferences(wf, tmpDir)
	if len(errs) == 0 {
		t.Fatal("expected error for missing additionalProperties")
	}
	joined := strings.Join(errs, "; ")
	if !strings.Contains(joined, "must set additionalProperties to false") {
		t.Errorf("error %q should mention additionalProperties", joined)
	}
}

func TestValidateSchemaReferences_EmptyOutputSchemaSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	schemasDir := filepath.Join(tmpDir, "schemas")
	if err := os.MkdirAll(schemasDir, 0755); err != nil {
		t.Fatalf("creating schemas dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(schemasDir, "valid.json"), []byte(`{"type":"object","additionalProperties":false}`), 0644); err != nil {
		t.Fatalf("creating schema file: %v", err)
	}

	// The first step has no output_schema (empty string) and is skipped; the
	// second step's schema is validated normally.
	wf := &definition.WorkflowFile{
		Version:     1,
		Name:        "test-schema-refs",
		InitialStep: "step1",
		Steps: []definition.Step{
			{ID: "step1", Kind: "agent", Agent: "test"},
			{ID: "step2", Kind: "agent", Agent: "test", OutputSchema: "schemas/valid.json"},
		},
		Transitions: []definition.Transition{
			{From: "step1", To: "step2", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "step2", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	if errs := ValidateSchemaReferences(wf, tmpDir); len(errs) > 0 {
		t.Fatalf("unexpected error: %v", strings.Join(errs, "; "))
	}
}

func TestValidateSchemaReferences_SchemaPathIsDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	schemasDir := filepath.Join(tmpDir, "schemas")
	if err := os.MkdirAll(filepath.Join(schemasDir, "dir.json"), 0755); err != nil {
		t.Fatalf("creating schema dir: %v", err)
	}

	// os.Open succeeds on a directory but reading it fails, so the error is
	// not IsNotExist and hits the "reading file" branch.
	wf := schemaTestWorkflow("step1", "schemas/dir.json")
	errs := ValidateSchemaReferences(wf, tmpDir)
	if len(errs) == 0 {
		t.Fatal("expected error for schema path that is a directory")
	}
	joined := strings.Join(errs, "; ")
	if !strings.Contains(joined, "reading file") {
		t.Errorf("error %q should mention reading file", joined)
	}
}

func TestValidateSchemaReferences_OversizedSchema(t *testing.T) {
	tmpDir := t.TempDir()
	schemasDir := filepath.Join(tmpDir, "schemas")
	if err := os.MkdirAll(schemasDir, 0755); err != nil {
		t.Fatalf("creating schemas dir: %v", err)
	}

	big := make([]byte, MaxSchemaBytes+100)
	for i := range big {
		big[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(schemasDir, "big.json"), big, 0644); err != nil {
		t.Fatalf("creating oversized schema file: %v", err)
	}

	wf := schemaTestWorkflow("step1", "schemas/big.json")
	errs := ValidateSchemaReferences(wf, tmpDir)
	if len(errs) == 0 {
		t.Fatal("expected error for oversized schema file")
	}
	joined := strings.Join(errs, "; ")
	if !strings.Contains(joined, "exceeds") {
		t.Errorf("error %q should mention exceeds", joined)
	}
}

func TestValidateSchemaReferences_NonObjectJSON(t *testing.T) {
	tmpDir := t.TempDir()
	schemasDir := filepath.Join(tmpDir, "schemas")
	if err := os.MkdirAll(schemasDir, 0755); err != nil {
		t.Fatalf("creating schemas dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(schemasDir, "arr.json"), []byte(`[1, 2, 3]`), 0644); err != nil {
		t.Fatalf("creating schema file: %v", err)
	}

	wf := schemaTestWorkflow("step1", "schemas/arr.json")
	errs := ValidateSchemaReferences(wf, tmpDir)
	if len(errs) == 0 {
		t.Fatal("expected error for non-object JSON schema")
	}
	joined := strings.Join(errs, "; ")
	if !strings.Contains(joined, "must be a JSON object") {
		t.Errorf("error %q should mention JSON object", joined)
	}
}

func TestValidateSchemaReferences_AdditionalPropertiesObject(t *testing.T) {
	tmpDir := t.TempDir()
	schemasDir := filepath.Join(tmpDir, "schemas")
	if err := os.MkdirAll(schemasDir, 0755); err != nil {
		t.Fatalf("creating schemas dir: %v", err)
	}

	// additionalProperties as a subschema object is more restrictive than
	// false, so it is accepted.
	schemaContent := `{
  "type": "object",
  "additionalProperties": { "type": "string" }
}`
	if err := os.WriteFile(filepath.Join(schemasDir, "ap-object.json"), []byte(schemaContent), 0644); err != nil {
		t.Fatalf("creating schema file: %v", err)
	}

	wf := schemaTestWorkflow("step1", "schemas/ap-object.json")
	if errs := ValidateSchemaReferences(wf, tmpDir); len(errs) > 0 {
		t.Fatalf("unexpected error for additionalProperties object: %v", strings.Join(errs, "; "))
	}
}

func TestValidateSchemaReferences_AdditionalPropertiesInvalidType(t *testing.T) {
	tmpDir := t.TempDir()
	schemasDir := filepath.Join(tmpDir, "schemas")
	if err := os.MkdirAll(schemasDir, 0755); err != nil {
		t.Fatalf("creating schemas dir: %v", err)
	}

	// additionalProperties that is neither a boolean nor an object is rejected.
	schemaContent := `{
  "type": "object",
  "additionalProperties": "yes"
}`
	if err := os.WriteFile(filepath.Join(schemasDir, "ap-bad.json"), []byte(schemaContent), 0644); err != nil {
		t.Fatalf("creating schema file: %v", err)
	}

	wf := schemaTestWorkflow("step1", "schemas/ap-bad.json")
	errs := ValidateSchemaReferences(wf, tmpDir)
	if len(errs) == 0 {
		t.Fatal("expected error for additionalProperties of invalid type")
	}
	joined := strings.Join(errs, "; ")
	if !strings.Contains(joined, "must be false or a schema object") {
		t.Errorf("error %q should mention additionalProperties type", joined)
	}
}

func TestValidateSchemaReferenceBytesUsesSuppliedContent(t *testing.T) {
	wf := &definition.WorkflowFile{Steps: []definition.Step{{
		ID: "review", OutputSchema: "review.json",
	}}}
	closed := []byte(`{"type":"object","additionalProperties":false}`)
	if errs := ValidateSchemaReferenceBytes(wf, map[string][]byte{"review.json": closed}); len(errs) > 0 {
		t.Fatalf("ValidateSchemaReferenceBytes: %v", strings.Join(errs, "; "))
	}

	open := []byte(`{"type":"object","additionalProperties":true}`)
	if errs := ValidateSchemaReferenceBytes(wf, map[string][]byte{"review.json": open}); len(errs) == 0 {
		t.Fatal("ValidateSchemaReferenceBytes accepted an open schema")
	}
}

func TestValidateSchemaReferenceBytesRequiresEveryReference(t *testing.T) {
	wf := &definition.WorkflowFile{Steps: []definition.Step{{
		ID: "review", OutputSchema: "review.json",
	}}}
	if errs := ValidateSchemaReferenceBytes(wf, map[string][]byte{}); len(errs) == 0 {
		t.Fatal("ValidateSchemaReferenceBytes accepted a missing schema")
	}
}

func TestValidateSchemaReferenceBytes_AgentPanel(t *testing.T) {
	wf := newAgentPanelWorkflow()
	errs := ValidateSchemaReferenceBytes(wf, map[string][]byte{})
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, "; "), `panel member "correctness": output_schema "schemas/panel.json": file not found`) {
		t.Fatalf("ValidateSchemaReferenceBytes() error = %v", strings.Join(errs, "; "))
	}
}

func TestValidateSchemaReferences_AgentPanel(t *testing.T) {
	tmpDir := t.TempDir()
	wf := newAgentPanelWorkflow()
	errs := ValidateSchemaReferences(wf, tmpDir)
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, "; "), `panel member "correctness": output_schema "schemas/panel.json": file not found`) {
		t.Fatalf("ValidateSchemaReferences() error = %v", strings.Join(errs, "; "))
	}
}

func TestValidateSchemaReferenceBytesRejectsInvalidWorkflowAndPath(t *testing.T) {
	if errs := ValidateSchemaReferenceBytes(nil, nil); len(errs) == 0 {
		t.Fatal("nil workflow was accepted")
	}
	wf := &definition.WorkflowFile{Steps: []definition.Step{{ID: "review", OutputSchema: "../review.json"}}}
	if errs := ValidateSchemaReferenceBytes(wf, map[string][]byte{}); len(errs) == 0 {
		t.Fatal("escaping schema reference was accepted")
	}
}

func TestValidateSchemaReferenceBytesRejectsEmptySubschema(t *testing.T) {
	wf := &definition.WorkflowFile{Steps: []definition.Step{{
		ID: "review", OutputSchema: "review.json",
	}}}
	data := []byte(`{"type":"object","additionalProperties":{}}`)
	if errs := ValidateSchemaReferenceBytes(wf, map[string][]byte{"review.json": data}); len(errs) == 0 {
		t.Fatal("ValidateSchemaReferenceBytes accepted an unrestricted empty subschema")
	}
}

func TestValidateSchemaReferenceBytesRejectsNoOpSubschemas(t *testing.T) {
	wf := &definition.WorkflowFile{Steps: []definition.Step{{ID: "review", OutputSchema: "review.json"}}}
	for name, subschema := range map[string]string{
		"required":   `{"required":[]}`,
		"properties": `{"properties":{}}`,
		"allOf":      `{"allOf":[]}`,
		"pattern":    `{"pattern":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			data := []byte(`{"type":"object","additionalProperties":` + subschema + `}`)
			if errs := ValidateSchemaReferenceBytes(wf, map[string][]byte{"review.json": data}); len(errs) == 0 {
				t.Fatalf("accepted no-op subschema %s", subschema)
			}
		})
	}
}

func TestValidateSchemaReferenceBytesRejectsMoreNoOpSubschemas(t *testing.T) {
	wf := &definition.WorkflowFile{Steps: []definition.Step{{ID: "review", OutputSchema: "review.json"}}}
	for name, subschema := range map[string]string{
		"uniqueItems":       `{"uniqueItems":false}`,
		"minLength":         `{"minLength":0}`,
		"minItems":          `{"minItems":0}`,
		"minProperties":     `{"minProperties":0}`,
		"propertyNames":     `{"propertyNames":{}}`,
		"if":                `{"if":{"type":"string"}}`,
		"then":              `{"then":{"type":"string"}}`,
		"property child":    `{"properties":{"name":{}}}`,
		"dependentRequired": `{"dependentRequired":{"name":[]}}`,
		"self dependency":   `{"dependentRequired":{"name":["name"]}}`,
		"dependent schema":  `{"dependentSchemas":{"name":{"required":["name"]}}}`,
		"zero contains":     `{"contains":false,"minContains":0}`,
		"anyOf tautology":   `{"anyOf":[{}]}`,
		"not false":         `{"not":false}`,
		"universal pattern": `{"pattern":".*"}`,
	} {
		t.Run(name, func(t *testing.T) {
			data := []byte(`{"type":"object","additionalProperties":` + subschema + `}`)
			if errs := ValidateSchemaReferenceBytes(wf, map[string][]byte{"review.json": data}); len(errs) == 0 {
				t.Fatalf("accepted no-op subschema %s", subschema)
			}
		})
	}
}

func TestValidateSchemaReferenceBytesAcceptsRestrictivePatternSubschema(t *testing.T) {
	wf := &definition.WorkflowFile{Steps: []definition.Step{{ID: "review", OutputSchema: "review.json"}}}
	data := []byte(`{"type":"object","additionalProperties":{"pattern":"^x+$"}}`)
	if errs := ValidateSchemaReferenceBytes(wf, map[string][]byte{"review.json": data}); len(errs) > 0 {
		t.Fatalf("rejected restrictive pattern: %v", strings.Join(errs, "; "))
	}
}

func TestValidateSchemaReferenceBytesCompilesSelectedSchema(t *testing.T) {
	wf := &definition.WorkflowFile{Steps: []definition.Step{{ID: "review", OutputSchema: "review.json"}}}
	data := []byte(`{"$ref":"https://example.test/schema.json","additionalProperties":false}`)
	if errs := ValidateSchemaReferenceBytes(wf, map[string][]byte{"review.json": data}); len(errs) == 0 {
		t.Fatal("accepted unsupported remote schema reference")
	}
}

func TestSchemaRestrictionHelpersRejectMalformedValues(t *testing.T) {
	for name, schema := range map[string]map[string]json.RawMessage{
		"bad enum":       {"enum": json.RawMessage(`{}`)},
		"bad pattern":    {"pattern": json.RawMessage(`1`)},
		"bad required":   {"required": json.RawMessage(`{}`)},
		"zero minimum":   {"minLength": json.RawMessage(`0`)},
		"bad maximum":    {"maxLength": json.RawMessage(`"x"`)},
		"false unique":   {"uniqueItems": json.RawMessage(`false`)},
		"bad properties": {"properties": json.RawMessage(`[]`)},
		"empty allOf":    {"allOf": json.RawMessage(`[]`)},
		"unknown":        {"unknown": json.RawMessage(`true`)},
	} {
		t.Run(name, func(t *testing.T) {
			if schemaHasRestriction(schema) {
				t.Fatalf("schema %+v was classified as restrictive", schema)
			}
		})
	}
	if !rawSchemaRestricts(json.RawMessage(`false`)) || rawSchemaRestricts(json.RawMessage(`true`)) {
		t.Fatal("boolean schema restriction classification is incorrect")
	}
	if rawSchemaRestricts(json.RawMessage(`[]`)) {
		t.Fatal("array schema was classified as restrictive")
	}
}

func TestSchemaKeywordRestrictionPositiveBranches(t *testing.T) {
	for name, schema := range map[string]map[string]json.RawMessage{
		"const":          {"const": json.RawMessage(`null`)},
		"empty enum":     {"enum": json.RawMessage(`[]`)},
		"property child": {"properties": json.RawMessage(`{"name":{"type":"string"}}`)},
		"contains":       {"contains": json.RawMessage(`{}`)},
	} {
		t.Run(name, func(t *testing.T) {
			if !schemaHasRestriction(schema) {
				t.Fatalf("schema %+v was not restrictive", schema)
			}
		})
	}
	if patternKeywordRestricts(json.RawMessage(`"["`)) {
		t.Fatal("invalid pattern was classified as restrictive")
	}
}

func TestSchemaRestrictionFallbackAndWitnessErrors(t *testing.T) {
	rareProperty := map[string]json.RawMessage{
		"properties": json.RawMessage(`{"rare":{"type":"string"}}`),
	}
	if !schemaHasRestriction(rareProperty) {
		t.Fatal("rare property restriction was not detected")
	}
	pattern := map[string]json.RawMessage{
		"pattern": json.RawMessage(`"^(|\\n|a|x)$"`),
	}
	if !schemaHasRestriction(pattern) {
		t.Fatal("pattern fallback restriction was not detected")
	}
	invalidRaw := map[string]json.RawMessage{"bad": json.RawMessage(`{`)}
	if schemaRejectsWitness(invalidRaw) {
		t.Fatal("invalid raw schema was classified as restrictive")
	}
}

func TestTypeKeywordRestrictionBoundaries(t *testing.T) {
	for _, raw := range []json.RawMessage{json.RawMessage(`""`), json.RawMessage(`[]`), json.RawMessage(`{}`), json.RawMessage(`["null","boolean","object","array","number","string"]`)} {
		if typeKeywordRestricts(raw) {
			t.Fatalf("type value %s was classified as restrictive", raw)
		}
	}
	if !typeKeywordRestricts(json.RawMessage(`"string"`)) || !typeKeywordRestricts(json.RawMessage(`["string"]`)) {
		t.Fatal("narrow type was classified as unrestricted")
	}
}

func TestSchemaListRestrictionBoundaries(t *testing.T) {
	for _, raw := range []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`[]`), json.RawMessage(`[true,{}]`)} {
		if schemaListRestricts(raw) {
			t.Fatalf("schema list %s was classified as restrictive", raw)
		}
	}
	if !schemaListRestricts(json.RawMessage(`[true,{"type":"string"}]`)) {
		t.Fatal("schema list with a narrow member was classified as unrestricted")
	}
}

// contains is a simple string contains helper for tests.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
