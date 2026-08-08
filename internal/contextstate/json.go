package contextstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"unicode/utf8"
)

// MarshalCanonical encodes a DTO with sorted object keys and no insignificant
// whitespace. Validation remains the responsibility of the DTO's Validate
// method; this function only canonicalizes the JSON representation.
func MarshalCanonical(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal context DTO: %w", err)
	}
	return canonicalizeJSON(raw)
}

func UnmarshalCanonical(data []byte, target any) error {
	if target == nil || !utf8.Valid(data) {
		return invalid("json", "invalid UTF-8 or nil target")
	}
	canonical, err := canonicalizeJSON(data)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(canonical))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return invalid("json", err.Error())
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return invalid("json", "multiple JSON values")
	}
	return nil
}

func canonicalizeJSON(data []byte) ([]byte, error) {
	if !utf8.Valid(data) {
		return nil, invalid("json", "invalid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	out, err := canonicalJSONValue(dec)
	if err != nil {
		return nil, invalid("json", err.Error())
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, invalid("json", "multiple JSON values")
	}
	return out, nil
}

func canonicalJSONValue(dec *json.Decoder) ([]byte, error) {
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			fields := map[string][]byte{}
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("object key is not a string")
				}
				if _, exists := fields[key]; exists {
					return nil, fmt.Errorf("duplicate object key %q", key)
				}
				encoded, err := canonicalJSONValue(dec)
				if err != nil {
					return nil, err
				}
				fields[key] = encoded
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			keys := make([]string, 0, len(fields))
			for key := range fields {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			var out bytes.Buffer
			out.WriteByte('{')
			for i, key := range keys {
				if i > 0 {
					out.WriteByte(',')
				}
				keyJSON, _ := json.Marshal(key)
				out.Write(keyJSON)
				out.WriteByte(':')
				out.Write(fields[key])
			}
			out.WriteByte('}')
			return out.Bytes(), nil
		case '[':
			var values [][]byte
			for dec.More() {
				encoded, err := canonicalJSONValue(dec)
				if err != nil {
					return nil, err
				}
				values = append(values, encoded)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			var out bytes.Buffer
			out.WriteByte('[')
			for i, encoded := range values {
				if i > 0 {
					out.WriteByte(',')
				}
				out.Write(encoded)
			}
			out.WriteByte(']')
			return out.Bytes(), nil
		}
	case string, bool, nil:
		return json.Marshal(value)
	case json.Number:
		return []byte(string(value)), nil
	}
	return nil, fmt.Errorf("unsupported JSON token %v", token)
}
