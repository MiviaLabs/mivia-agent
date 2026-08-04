package compiler

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// MaxSchemaBytes is the maximum allowed size for a single schema file.
const MaxSchemaBytes = 65536

// ValidateSchemaReferences checks that every step with a non-empty OutputSchema
// references a valid JSON Schema file with additionalProperties set to false
// or a more restrictive schema object. Paths are resolved relative to baseDir.
func ValidateSchemaReferences(wf *definition.WorkflowFile, baseDir string) error {
	for _, s := range wf.Steps {
		if s.OutputSchema == "" {
			continue
		}

		// Reject path traversal
		if strings.Contains(s.OutputSchema, "..") {
			return fmt.Errorf("step %q: output_schema %q: path traversal not allowed", s.ID, s.OutputSchema)
		}

		// Resolve path relative to baseDir
		schemaPath := filepath.Join(baseDir, s.OutputSchema)

		// Read the file (bounded read for safety)
		data, err := readBoundedFile(schemaPath, MaxSchemaBytes)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("step %q: output_schema %q: file not found", s.ID, s.OutputSchema)
			}
			return fmt.Errorf("step %q: output_schema %q: reading file: %w", s.ID, s.OutputSchema, err)
		}

		// Check size (readBoundedFile reads maxBytes+1, so > maxBytes means oversized)
		if int64(len(data)) > MaxSchemaBytes {
			return fmt.Errorf("step %q: output_schema %q: file exceeds %d bytes", s.ID, s.OutputSchema, MaxSchemaBytes)
		}

		// Parse as JSON
		var raw json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("step %q: output_schema %q: invalid JSON: %w", s.ID, s.OutputSchema, err)
		}

		// Must be a JSON object
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(data, &obj); err != nil {
			return fmt.Errorf("step %q: output_schema %q: must be a JSON object", s.ID, s.OutputSchema)
		}

		// Check additionalProperties
		ap, hasAP := obj["additionalProperties"]
		if !hasAP {
			return fmt.Errorf("step %q: output_schema %q: must set additionalProperties to false", s.ID, s.OutputSchema)
		}

		// Unmarshal additionalProperties to detect its type
		var apBool bool
		var apObj map[string]json.RawMessage
		if err := json.Unmarshal(ap, &apBool); err == nil {
			// It's a boolean
			if apBool {
				return fmt.Errorf("step %q: output_schema %q: additionalProperties must not be true (too permissive)", s.ID, s.OutputSchema)
			}
			// false is OK
		} else if err := json.Unmarshal(ap, &apObj); err == nil {
			// It's a JSON object (a subschema) — more restrictive than false, OK
			_ = apObj
		} else {
			return fmt.Errorf("step %q: output_schema %q: additionalProperties must be false or a schema object", s.ID, s.OutputSchema)
		}
	}
	return nil
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
