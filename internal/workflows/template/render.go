package template

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// DefaultMaxRenderedBytes bounds one rendered prompt context.
const DefaultMaxRenderedBytes = MaxTemplateBytes

// Render expands only explicit input and evidence bindings. It does not read
// files, execute commands, or inspect an agent transcript.
func Render(source string, inputs, evidence map[string]any, maxBindingBytes, maxRenderedBytes int) (string, error) {
	if !utf8.ValidString(source) {
		return "", fmt.Errorf("template is not valid UTF-8")
	}
	if maxBindingBytes <= 0 {
		maxBindingBytes = MaxTemplateBytes
	}
	if maxRenderedBytes <= 0 {
		maxRenderedBytes = DefaultMaxRenderedBytes
	}
	var out bytes.Buffer
	for pos := 0; pos < len(source); {
		start := strings.Index(source[pos:], "{{")
		if start < 0 {
			out.WriteString(source[pos:])
			break
		}
		start += pos
		out.WriteString(source[pos:start])
		end := strings.Index(source[start+2:], "}}")
		if end < 0 {
			return "", fmt.Errorf("template has an unclosed binding")
		}
		end += start + 2
		name := strings.TrimSpace(source[start+2 : end])
		value, ok := lookupBinding(name, inputs, evidence)
		if !ok {
			return "", fmt.Errorf("template binding %q is missing", name)
		}
		encoded, err := encodeBinding(value)
		if err != nil {
			return "", fmt.Errorf("template binding %q: %w", name, err)
		}
		if len(encoded) > maxBindingBytes {
			return "", fmt.Errorf("template binding %q exceeds %d bytes", name, maxBindingBytes)
		}
		out.Write(encoded)
		if out.Len() > maxRenderedBytes {
			return "", fmt.Errorf("rendered template exceeds %d bytes", maxRenderedBytes)
		}
		pos = end + 2
	}
	if out.Len() > maxRenderedBytes {
		return "", fmt.Errorf("rendered template exceeds %d bytes", maxRenderedBytes)
	}
	return out.String(), nil
}

func lookupBinding(name string, inputs, evidence map[string]any) (any, bool) {
	parts := strings.Split(name, ".")
	if len(parts) != 2 || (parts[0] != "inputs" && parts[0] != "evidence") || parts[1] == "" {
		return nil, false
	}
	if parts[0] == "inputs" {
		v, ok := inputs[parts[1]]
		return v, ok
	}
	v, ok := evidence[parts[1]]
	return v, ok
}

func encodeBinding(value any) ([]byte, error) {
	if s, ok := value.(string); ok {
		return []byte(s), nil
	}
	return json.Marshal(value)
}
