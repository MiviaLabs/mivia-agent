package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
)

func validateSchema(object map[string]any, schema map[string]any) error {
	properties, _ := schema["properties"].(map[string]any)
	for _, name := range requiredFields(schema) {
		if _, present := object[name]; !present {
			return fmt.Errorf("invalid arguments: missing required field %q", name)
		}
	}
	additional := true
	if raw, present := schema["additionalProperties"]; present {
		additional, _ = raw.(bool)
	}
	for name, value := range object {
		property, known := properties[name]
		if !known {
			if !additional {
				return fmt.Errorf("invalid arguments: unknown field")
			}
			continue
		}
		definition, _ := property.(map[string]any)
		if err := validateProperty(name, value, definition); err != nil {
			return err
		}
	}
	return nil
}

// requiredFields returns the schema's required field names, accepting both the
// JSON-decoded []any form and a literal []string form.
func requiredFields(schema map[string]any) []string {
	if raw, ok := schema["required"].([]string); ok {
		return raw
	}
	var required []string
	if raw, ok := schema["required"].([]any); ok {
		for _, name := range raw {
			if s, ok := name.(string); ok {
				required = append(required, s)
			}
		}
	}
	return required
}

// validateProperty validates a single known property against its definition:
// enum membership, type match, numeric bounds and array constraints.
func validateProperty(name string, value any, definition map[string]any) error {
	kind, _ := definition["type"].(string)
	if enum, ok := schemaEnum(definition["enum"]); ok && !enumContains(enum, value) {
		return fmt.Errorf("invalid arguments: field %q must be one of the declared values", name)
	}
	if !schemaTypeMatches(value, kind, definition) {
		return fmt.Errorf("invalid arguments: field %q must be %s", name, kind)
	}
	// minimum/maximum for integer/number fields.
	if kind == "integer" || kind == "number" {
		if err := validateNumberBounds(name, value, definition); err != nil {
			return err
		}
	}
	// minItems/maxItems and per-item enums for array fields.
	if kind == "array" {
		if err := validateArrayConstraints(name, value, definition); err != nil {
			return err
		}
	}
	return nil
}

// validateNumberBounds enforces minimum/maximum on integer and number fields.
func validateNumberBounds(name string, value any, definition map[string]any) error {
	numVal, ok := value.(float64)
	if !ok {
		return nil
	}
	if min, ok := definition["minimum"].(float64); ok && numVal < min {
		return fmt.Errorf("invalid arguments: field %q must be >= %v", name, min)
	}
	if max, ok := definition["maximum"].(float64); ok && numVal > max {
		return fmt.Errorf("invalid arguments: field %q must be <= %v", name, max)
	}
	return nil
}

// validateArrayConstraints enforces minItems/maxItems and enum-on-items for
// array fields.
func validateArrayConstraints(name string, value any, definition map[string]any) error {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	if minItems, ok := definition["minItems"].(float64); ok && int(minItems) > len(values) {
		return fmt.Errorf("invalid arguments: field %q must have >= %d items", name, int(minItems))
	}
	if maxItems, ok := definition["maxItems"].(float64); ok && int(maxItems) < len(values) {
		return fmt.Errorf("invalid arguments: field %q must have <= %d items", name, int(maxItems))
	}
	if items, ok := definition["items"].(map[string]any); ok {
		if itemEnum, ok := schemaEnum(items["enum"]); ok {
			for _, item := range values {
				if !enumContains(itemEnum, item) {
					return fmt.Errorf("invalid arguments: array items for %q must be one of the declared values", name)
				}
			}
		}
	}
	return nil
}

func schemaTypeMatches(value any, kind string, definition map[string]any) bool {
	switch kind {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number", "integer":
		number, ok := value.(float64)
		return ok && (kind != "integer" || math.Trunc(number) == number)
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		values, ok := value.([]any)
		if !ok {
			return false
		}
		items, _ := definition["items"].(map[string]any)
		itemType, _ := items["type"].(string)
		for _, item := range values {
			if !schemaTypeMatches(item, itemType, items) {
				return false
			}
		}
		return true
	default:
		return true
	}
}

func enumContains(values []any, value any) bool {
	for _, candidate := range values {
		if reflect.DeepEqual(candidate, value) {
			return true
		}
	}
	return false
}

func schemaEnum(raw any) ([]any, bool) {
	switch values := raw.(type) {
	case []any:
		return values, true
	case []string:
		out := make([]any, len(values))
		for i, value := range values {
			out[i] = value
		}
		return out, true
	default:
		return nil, false
	}
}

func schemaObject(props map[string]any, required []string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	}
}

func decodeArgs[T any](raw json.RawMessage, dst *T) error {
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}
