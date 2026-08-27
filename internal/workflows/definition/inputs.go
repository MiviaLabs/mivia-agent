package definition

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ParseInputValue decodes one workflow input value against the declared type.
// String inputs pass through verbatim; typed inputs must be single JSON values
// of the declared type. Both resume surfaces (CLI and local engine) parse
// admitted input strings through this one function so a typed input resumes
// with the same Go value on either path.
func ParseInputValue(value, typ string) (any, error) {
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
