package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

const (
	maxSchemaDepth      = 16
	maxSchemaProperties = 128
)

func sanitizeToolMetadata(description string, schema map[string]any, maxDescription, maxSchema int, redaction *redact.Policy) (string, map[string]any, error) {
	description = strings.Map(func(value rune) rune {
		if unicode.IsControl(value) {
			return ' '
		}
		return value
	}, description)
	description = strings.TrimSpace(redaction.Text(description))
	if maxDescription > 0 && len(description) > maxDescription {
		return "", nil, fmt.Errorf("MCP tool description exceeds configured limit")
	}
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return "", nil, fmt.Errorf("marshal MCP tool schema: %w", err)
	}
	if maxSchema > 0 && len(encoded) > maxSchema {
		return "", nil, fmt.Errorf("MCP tool schema exceeds configured limit")
	}
	redactedSchema, ok := redaction.JSONValue(schema).(map[string]any)
	if !ok {
		return "", nil, fmt.Errorf("redact MCP tool schema")
	}
	depth, properties := schemaComplexity(schema, 1)
	if depth > maxSchemaDepth || properties > maxSchemaProperties {
		return "", nil, fmt.Errorf("MCP tool schema is too complex")
	}
	return description, bridgeToolSchema(redactedSchema), nil
}

// bridgeToolSchema copies the JSON Schema subset the host can safely expose.
// Unsupported forms fall back to an open object. The MCP server remains the
// argument validator, so this fallback cannot reject valid server arguments.
func bridgeToolSchema(schema map[string]any) map[string]any {
	bridged, ok := bridgeSchemaValue(schema, 1)
	if !ok || bridged["type"] == nil {
		return map[string]any{"type": "object", "additionalProperties": true}
	}
	return bridged
}

func bridgeSchemaValue(value map[string]any, depth int) (map[string]any, bool) {
	if depth > maxSchemaDepth {
		return nil, false
	}
	out := make(map[string]any, len(value))
	for key, raw := range value {
		switch key {
		case "type":
			kind, ok := raw.(string)
			if !ok || !validSchemaType(kind) {
				return nil, false
			}
			out[key] = kind
		case "properties":
			properties, ok := raw.(map[string]any)
			if !ok || len(properties) > maxSchemaProperties {
				return nil, false
			}
			copied := make(map[string]any, len(properties))
			for name, property := range properties {
				child, ok := property.(map[string]any)
				if !ok {
					return nil, false
				}
				child, ok = bridgeSchemaValue(child, depth+1)
				if !ok {
					return nil, false
				}
				copied[name] = child
			}
			out[key] = copied
		case "items":
			item, ok := raw.(map[string]any)
			if !ok {
				return nil, false
			}
			child, ok := bridgeSchemaValue(item, depth+1)
			if !ok {
				return nil, false
			}
			out[key] = child
		case "required":
			values, ok := stringSlice(raw)
			if !ok || len(values) > maxSchemaProperties {
				return nil, false
			}
			out[key] = values
		case "enum":
			values, ok := scalarSlice(raw)
			if !ok || len(values) > 32 {
				return nil, false
			}
			out[key] = values
		case "additionalProperties":
			allowed, ok := raw.(bool)
			if !ok {
				return nil, false
			}
			out[key] = allowed
		default:
			return nil, false
		}
	}
	return out, true
}

func validSchemaType(kind string) bool {
	return kind == "object" || kind == "array" || kind == "string" || kind == "number" || kind == "integer" || kind == "boolean" || kind == "null"
}

func stringSlice(value any) ([]string, bool) {
	values, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		out = append(out, text)
	}
	return out, true
}

func scalarSlice(value any) ([]any, bool) {
	values, ok := value.([]any)
	if !ok {
		return nil, false
	}
	for _, value := range values {
		switch value.(type) {
		case nil, bool, string, float64:
		default:
			return nil, false
		}
	}
	return append([]any(nil), values...), true
}

func schemaComplexity(value any, depth int) (int, int) {
	maxDepth, properties := depth, 0
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			childDepth, childProperties := schemaComplexity(child, depth+1)
			if childDepth > maxDepth {
				maxDepth = childDepth
			}
			properties += childProperties
			if key == "properties" {
				if nested, ok := child.(map[string]any); ok {
					properties += len(nested)
				}
			}
		}
	case []any:
		for _, child := range typed {
			childDepth, childProperties := schemaComplexity(child, depth+1)
			if childDepth > maxDepth {
				maxDepth = childDepth
			}
			properties += childProperties
		}
	}
	return maxDepth, properties
}
