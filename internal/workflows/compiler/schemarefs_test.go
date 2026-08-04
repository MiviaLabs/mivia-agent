package compiler

import (
	"os"
	"path/filepath"
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
	err := ValidateSchemaReferences(wf, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error for valid schema: %v", err)
	}
}

func TestValidateSchemaReferences_SchemaNotFound(t *testing.T) {
	tmpDir := t.TempDir()

	wf := schemaTestWorkflow("step1", "schemas/nonexistent.json")
	err := ValidateSchemaReferences(wf, tmpDir)
	if err == nil {
		t.Fatal("expected error for nonexistent schema file")
	}
	if err.Error() != `step "step1": output_schema "schemas/nonexistent.json": file not found` {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateSchemaReferences_PathTraversal(t *testing.T) {
	tmpDir := t.TempDir()

	wf := schemaTestWorkflow("step1", "../etc/passwd")
	err := ValidateSchemaReferences(wf, tmpDir)
	if err == nil {
		t.Fatal("expected error for path traversal in schema")
	}
	if err.Error() != `step "step1": output_schema "../etc/passwd": path traversal not allowed` {
		t.Errorf("unexpected error: %v", err)
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
	err := ValidateSchemaReferences(wf, tmpDir)
	if err == nil {
		t.Fatal("expected error for invalid JSON schema")
	}
	expected := `step "step1": output_schema "schemas/bad.json": invalid JSON`
	if !contains(err.Error(), expected) {
		t.Errorf("error %q should contain %q", err.Error(), expected)
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
	err := ValidateSchemaReferences(wf, tmpDir)
	if err == nil {
		t.Fatal("expected error for additionalProperties: true")
	}
	if !contains(err.Error(), "additionalProperties must not be true") {
		t.Errorf("error %q should mention additionalProperties", err.Error())
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
	err := ValidateSchemaReferences(wf, tmpDir)
	if err == nil {
		t.Fatal("expected error for missing additionalProperties")
	}
	if !contains(err.Error(), "must set additionalProperties to false") {
		t.Errorf("error %q should mention additionalProperties", err.Error())
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
	if err := ValidateSchemaReferences(wf, tmpDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
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
	err := ValidateSchemaReferences(wf, tmpDir)
	if err == nil {
		t.Fatal("expected error for schema path that is a directory")
	}
	if !contains(err.Error(), "reading file") {
		t.Errorf("error %q should mention reading file", err.Error())
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
	err := ValidateSchemaReferences(wf, tmpDir)
	if err == nil {
		t.Fatal("expected error for oversized schema file")
	}
	if !contains(err.Error(), "exceeds") {
		t.Errorf("error %q should mention exceeds", err.Error())
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
	err := ValidateSchemaReferences(wf, tmpDir)
	if err == nil {
		t.Fatal("expected error for non-object JSON schema")
	}
	if !contains(err.Error(), "must be a JSON object") {
		t.Errorf("error %q should mention JSON object", err.Error())
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
	if err := ValidateSchemaReferences(wf, tmpDir); err != nil {
		t.Fatalf("unexpected error for additionalProperties object: %v", err)
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
	err := ValidateSchemaReferences(wf, tmpDir)
	if err == nil {
		t.Fatal("expected error for additionalProperties of invalid type")
	}
	if !contains(err.Error(), "must be false or a schema object") {
		t.Errorf("error %q should mention additionalProperties type", err.Error())
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
