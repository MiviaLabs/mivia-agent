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
