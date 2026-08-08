package jschema

// Model-facing schema rendering: the contract text the model sees in prompts
// and corrective messages. The compiled validator still uses the raw admitted
// document (Compiled.Raw()); these helpers only render text for the model, and
// they never echo the schema document verbatim.

import (
	"encoding/json"
	"strings"
)

// PromptAppendix is the deterministic host instruction appended when a schema
// is in force.
func PromptAppendix(schema map[string]any) string {
	contract := ModelSchemaContract(schema)
	if contract == "" {
		return "\n\nReturn ONLY valid JSON matching the required output schema."
	}
	return "\n\nReturn ONLY valid JSON matching this schema (no prose, no markdown fences):\n" + contract
}

// schemaMetaKeys are JSON Schema meta/annotation keywords that describe the
// document, not the instance shape. Every model-facing renderer strips them.
// A verbatim schema document invites the model to echo the document back as
// its answer, so the model must never see one.
var schemaMetaKeys = map[string]struct{}{
	"$schema":     {},
	"title":       {},
	"description": {},
	"$id":         {},
	"$comment":    {},
	"default":     {},
}

// maxExampleRequiredKeys bounds the compact example. An example is practical
// only for small object schemas.
const maxExampleRequiredKeys = 4

// ModelSchemaContract renders the model-facing contract for a schema map.
// It strips the schema meta-keywords, adds the never-echo instruction, and
// appends a compact filled example for small object schemas. The compiled
// validator still uses the raw document (Compiled.Raw()); this is only the
// text the model sees. It returns "" when the schema cannot be rendered.
func ModelSchemaContract(schema map[string]any) string {
	if schema == nil {
		return ""
	}
	raw, err := json.Marshal(stripMetaKeys(schema))
	if err != nil || len(raw) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Output an instance of the schema, never the schema document itself.")
	if ex := exampleForSchema(schema); ex != "" {
		b.WriteString("\nExample: " + ex)
	}
	b.WriteString("\n" + string(raw))
	return b.String()
}

// stripMetaKeys removes the schema meta-keywords recursively, so the rendered
// contract keeps only the instance-shape keywords.
func stripMetaKeys(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			if _, meta := schemaMetaKeys[k]; meta {
				continue
			}
			out[k] = stripMetaKeys(child)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = stripMetaKeys(child)
		}
		return out
	default:
		return v
	}
}

// exampleForSchema builds a compact one-line example instance for a small
// object schema (up to maxExampleRequiredKeys required properties). It
// returns "" when no compact example is practical.
func exampleForSchema(schema map[string]any) string {
	keys := requiredKeys(schema)
	if len(keys) == 0 || len(keys) > maxExampleRequiredKeys {
		return ""
	}
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return ""
	}
	inst := make(map[string]any, len(keys))
	for _, name := range keys {
		prop, _ := props[name].(map[string]any)
		inst[name] = exampleValue(prop)
	}
	raw, err := json.Marshal(inst)
	if err != nil {
		return ""
	}
	return string(raw)
}

// exampleValue picks a plausible instance value for one property subschema.
func exampleValue(prop map[string]any) any {
	if prop == nil {
		return ""
	}
	if e := firstEnumValue(prop); e != nil {
		return e
	}
	if c, ok := prop["const"]; ok {
		return c
	}
	switch prop["type"] {
	case "boolean":
		return true
	case "number", "integer":
		return 0
	case "array":
		items, _ := prop["items"].(map[string]any)
		return []any{exampleValue(items)}
	case "object":
		out := map[string]any{}
		childProps, _ := prop["properties"].(map[string]any)
		for _, name := range requiredKeys(prop) {
			child, _ := childProps[name].(map[string]any)
			out[name] = exampleValue(child)
		}
		return out
	default:
		return "..."
	}
}

// requiredKeys returns the schema's required property names. It accepts both
// []any (JSON decode) and []string (Go literals).
func requiredKeys(schema map[string]any) []string {
	switch req := schema["required"].(type) {
	case []any:
		out := make([]string, 0, len(req))
		for _, r := range req {
			if s, ok := r.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return req
	}
	return nil
}

// firstEnumValue returns the first enum member when the property declares one.
func firstEnumValue(prop map[string]any) any {
	switch e := prop["enum"].(type) {
	case []any:
		if len(e) > 0 {
			return e[0]
		}
	case []string:
		if len(e) > 0 {
			return e[0]
		}
	}
	return nil
}
