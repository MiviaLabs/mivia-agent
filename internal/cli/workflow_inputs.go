package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// parseWorkflowInputs validates raw name=value input flags against the
// workflow's declared input contract. It returns the parsed values and the
// canonical string snapshot used for the admission digest.
func parseWorkflowInputs(raw []string, defs map[string]definition.InputDef) (map[string]any, map[string]string, error) {
	values := make(map[string]any)
	snapshot := make(map[string]string)
	for _, item := range raw {
		key, value, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, nil, fmt.Errorf("workflow input must use name=value")
		}
		def, exists := defs[key]
		if !exists {
			return nil, nil, fmt.Errorf("unknown workflow input %q", key)
		}
		if def.MaxBytes > 0 && len(value) > def.MaxBytes {
			return nil, nil, fmt.Errorf("workflow input %q exceeds %d bytes", key, def.MaxBytes)
		}
		parsed, err := parseWorkflowInputValue(value, def.Type)
		if err != nil {
			return nil, nil, fmt.Errorf("workflow input %q: %w", key, err)
		}
		values[key] = parsed
		snapshot[key] = value
	}
	for key, def := range defs {
		if def.Required {
			if _, ok := values[key]; !ok {
				return nil, nil, fmt.Errorf("required workflow input %q is missing", key)
			}
		}
	}
	return values, snapshot, nil
}

// parseWorkflowInputValue decodes one input value against the declared type.
// String inputs pass through verbatim; typed inputs must be single JSON
// values of the declared type.
func parseWorkflowInputValue(value, typ string) (any, error) {
	if typ == "string" {
		return value, nil
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(value)))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("value is not valid %s JSON", typ)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("value contains more than one JSON value")
	}
	valid := false
	switch typ {
	case "boolean":
		_, valid = parsed.(bool)
	case "integer":
		number, ok := parsed.(json.Number)
		valid = ok && !strings.ContainsAny(number.String(), ".eE")
	case "number":
		_, valid = parsed.(json.Number)
	case "object":
		_, valid = parsed.(map[string]any)
	case "array":
		_, valid = parsed.([]any)
	default:
		return nil, fmt.Errorf("unsupported input type %q", typ)
	}
	if !valid {
		return nil, fmt.Errorf("value does not match type %q", typ)
	}
	return parsed, nil
}
