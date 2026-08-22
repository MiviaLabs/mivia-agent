package cliworkflow

import (
	"fmt"
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
// It delegates to the shared definition parser so the CLI and the local engine
// resume paths produce identical values for typed inputs.
func parseWorkflowInputValue(value, typ string) (any, error) {
	return definition.ParseInputValue(value, typ)
}
