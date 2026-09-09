package miviaauth

import (
	"encoding/json"
	"testing"
)

// TestStringOrStringsUnmarshalJSON_UnexpectedShapeDecodesEmpty pins
// stringOrStrings' own final fallback: a JSON value that is neither a
// string nor a string array (a number here) must decode to the empty
// value rather than fail the whole envelope.
func TestStringOrStringsUnmarshalJSON_UnexpectedShapeDecodesEmpty(t *testing.T) {
	var s stringOrStrings
	if err := json.Unmarshal([]byte(`42`), &s); err != nil {
		t.Fatalf("UnmarshalJSON(42) error = %v, want nil", err)
	}
	if s != "" {
		t.Fatalf("UnmarshalJSON(42) = %q, want empty", s)
	}
}
