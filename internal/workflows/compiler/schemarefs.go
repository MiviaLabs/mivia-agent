package compiler

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/jschema"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// MaxSchemaBytes is the maximum allowed size for a single schema file.
const MaxSchemaBytes = 65536

// ValidateSchemaReferenceBytes validates the exact schema bytes selected by a workflow.
func ValidateSchemaReferenceBytes(wf *definition.WorkflowFile, schemas map[string][]byte) error {
	if wf == nil {
		return fmt.Errorf("workflow is nil")
	}
	for _, step := range wf.Steps {
		if err := validateSchemaBytes(step.ID, "", step.OutputSchema, schemas); err != nil {
			return err
		}
		if step.Kind != "agent_panel" || step.Panel == nil {
			continue
		}
		for _, member := range step.Panel.Members {
			if err := validateSchemaBytes(step.ID, member.ID, member.OutputSchema, schemas); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateSchemaReferences checks that every step with a non-empty OutputSchema
// references a valid JSON Schema file with additionalProperties set to false
// or a more restrictive schema object. Paths are resolved relative to baseDir.
func ValidateSchemaReferences(wf *definition.WorkflowFile, baseDir string) error {
	schemas := make(map[string][]byte)
	for _, s := range wf.Steps {
		if err := loadSchemaReference(schemas, baseDir, s.ID, "", s.OutputSchema); err != nil {
			return err
		}
		if s.Kind != "agent_panel" || s.Panel == nil {
			continue
		}
		for _, member := range s.Panel.Members {
			if err := loadSchemaReference(schemas, baseDir, s.ID, member.ID, member.OutputSchema); err != nil {
				return err
			}
		}
	}
	return ValidateSchemaReferenceBytes(wf, schemas)
}

func validateSchemaBytes(stepID, memberID, reference string, schemas map[string][]byte) error {
	if reference == "" {
		return nil
	}
	prefix := schemaReferencePrefix(stepID, memberID)
	if err := validateSchemaReferencePath(reference); err != nil {
		return fmt.Errorf("%s: output_schema %q: %w", prefix, reference, err)
	}
	data, ok := schemas[reference]
	if !ok {
		return fmt.Errorf("%s: output_schema %q: file not found", prefix, reference)
	}
	if err := validateClosedSchema(data); err != nil {
		return fmt.Errorf("%s: output_schema %q: %w", prefix, reference, err)
	}
	var schema map[string]any
	_ = json.Unmarshal(data, &schema)
	if _, err := jschema.Compile(schema); err != nil {
		return fmt.Errorf("%s: output_schema %q: compile schema: %w", prefix, reference, err)
	}
	return nil
}

func loadSchemaReference(schemas map[string][]byte, baseDir, stepID, memberID, reference string) error {
	if reference == "" {
		return nil
	}
	prefix := schemaReferencePrefix(stepID, memberID)
	if err := validateSchemaReferencePath(reference); err != nil {
		return fmt.Errorf("%s: output_schema %q: path traversal not allowed", prefix, reference)
	}
	data, err := readBoundedFile(filepath.Join(baseDir, reference), MaxSchemaBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s: output_schema %q: file not found", prefix, reference)
		}
		return fmt.Errorf("%s: output_schema %q: reading file: %w", prefix, reference, err)
	}
	schemas[reference] = data
	return nil
}

func schemaReferencePrefix(stepID, memberID string) string {
	prefix := fmt.Sprintf("step %q", stepID)
	if memberID != "" {
		prefix += fmt.Sprintf(": panel member %q", memberID)
	}
	return prefix
}

func validateSchemaReferencePath(ref string) error {
	clean := filepath.Clean(ref)
	if clean == "." || filepath.IsAbs(ref) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path traversal not allowed")
	}
	return nil
}

func validateClosedSchema(data []byte) error {
	if len(data) > MaxSchemaBytes {
		return fmt.Errorf("file exceeds %d bytes", MaxSchemaBytes)
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil || obj == nil {
		return fmt.Errorf("must be a JSON object")
	}
	ap, ok := obj["additionalProperties"]
	if !ok {
		return fmt.Errorf("must set additionalProperties to false")
	}
	var allow bool
	if err := json.Unmarshal(ap, &allow); err == nil {
		if allow {
			return fmt.Errorf("additionalProperties must not be true (too permissive)")
		}
		return nil
	}
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(ap, &schema); err == nil && schema != nil {
		if schemaHasRestriction(schema) {
			return nil
		}
		return fmt.Errorf("additionalProperties schema object must contain a validation rule")
	}
	return fmt.Errorf("additionalProperties must be false or a schema object")
}

func schemaHasRestriction(schema map[string]json.RawMessage) bool {
	if schemaRejectsWitness(schema) {
		return true
	}
	for key, raw := range schema {
		if schemaKeywordRestricts(key, raw) {
			return true
		}
	}
	return false
}

func schemaKeywordRestricts(key string, raw json.RawMessage) bool {
	switch key {
	case "enum":
		var values []json.RawMessage
		return json.Unmarshal(raw, &values) == nil && len(values) > 0
	case "pattern":
		return patternKeywordRestricts(raw)
	case "required":
		var values []json.RawMessage
		return json.Unmarshal(raw, &values) == nil && len(values) > 0
	case "minLength", "minItems", "minProperties":
		var value float64
		return json.Unmarshal(raw, &value) == nil && value > 0
	case "maxLength", "maxItems", "maxProperties", "maximum", "minimum", "exclusiveMaximum", "exclusiveMinimum", "multipleOf":
		var value float64
		return json.Unmarshal(raw, &value) == nil
	case "uniqueItems":
		var value bool
		return json.Unmarshal(raw, &value) == nil && value
	case "properties":
		var values map[string]json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return false
		}
		for _, child := range values {
			if rawSchemaRestricts(child) {
				return true
			}
		}
		return false
	case "allOf":
		return schemaListRestricts(raw)
	default:
		return false
	}
}

func schemaRejectsWitness(schema map[string]json.RawMessage) bool {
	raw, err := json.Marshal(schema)
	if err != nil {
		return false
	}
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	compiled, err := jschema.Compile(value)
	if err != nil {
		return false
	}
	witnesses := []any{
		nil, false, true, float64(-1), float64(0), float64(1), "", "\n", "a", "x",
		[]any{}, []any{nil}, []any{"x"}, map[string]any{},
		map[string]any{"name": nil}, map[string]any{"name": "x"}, map[string]any{"other": true},
	}
	for _, witness := range witnesses {
		if compiled.Validate(witness) != nil {
			return true
		}
	}
	return false
}

func patternKeywordRestricts(raw json.RawMessage) bool {
	var pattern string
	if json.Unmarshal(raw, &pattern) != nil {
		return false
	}
	compiled, err := jschema.Compile(map[string]any{"pattern": pattern})
	if err != nil {
		return false
	}
	for _, witness := range []string{"", "\n", "a", "x", "xx", "0", " "} {
		if compiled.Validate(witness) != nil {
			return true
		}
	}
	return false
}

func rawSchemaRestricts(raw json.RawMessage) bool {
	var allowed bool
	if json.Unmarshal(raw, &allowed) == nil {
		return !allowed
	}
	var schema map[string]json.RawMessage
	return json.Unmarshal(raw, &schema) == nil && schema != nil && schemaHasRestriction(schema)
}

func schemaListRestricts(raw json.RawMessage) bool {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil || len(values) == 0 {
		return false
	}
	for _, value := range values {
		if rawSchemaRestricts(value) {
			return true
		}
	}
	return false
}

func typeKeywordRestricts(raw json.RawMessage) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single != ""
	}
	var values []string
	if json.Unmarshal(raw, &values) != nil || len(values) == 0 {
		return false
	}
	present := make(map[string]bool, len(values))
	for _, value := range values {
		present[value] = true
	}
	for _, value := range []string{"null", "boolean", "object", "array", "number", "string"} {
		if !present[value] {
			return true
		}
	}
	return false
}

// readBoundedFile reads a file with a size limit and returns its contents.
func readBoundedFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxBytes+1))
}
