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
	depth, properties := schemaComplexity(schema, 1)
	if depth > maxSchemaDepth || properties > maxSchemaProperties {
		return "", nil, fmt.Errorf("MCP tool schema is too complex")
	}
	return description, schema, nil
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
