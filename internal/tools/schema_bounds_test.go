package tools

import (
	"strings"
	"testing"
)

// The schema validator's refusal arms. Every one of these is what stops a
// model-supplied argument reaching a tool's Execute, so an arm that
// stopped refusing does not fail a test - it widens what the model can
// make a tool do.

func numField(def map[string]any) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"n": def},
	}
}

// TestNumberBoundsAreEnforcedAtBothEnds: minimum and maximum are separate
// arms, and a validator that checked only one lets half the out-of-range
// values through.
func TestNumberBoundsAreEnforcedAtBothEnds(t *testing.T) {
	def := map[string]any{"type": "number", "minimum": float64(1), "maximum": float64(10)}

	for _, tc := range []struct {
		n       float64
		wantErr string
	}{
		{0, "must be >= 1"},
		{-5, "must be >= 1"},
		{11, "must be <= 10"},
		{999, "must be <= 10"},
	} {
		err := validateSchema(map[string]any{"n": tc.n}, numField(def))
		if err == nil {
			t.Errorf("n=%v was accepted, want %q", tc.n, tc.wantErr)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("n=%v gave %q, want it to say %q", tc.n, err, tc.wantErr)
		}
	}
	for _, n := range []float64{1, 5, 10} {
		if err := validateSchema(map[string]any{"n": n}, numField(def)); err != nil {
			t.Errorf("n=%v is inside the bounds but was refused: %v", n, err)
		}
	}
}

// TestArrayItemCountsAreEnforcedAtBothEnds: minItems and maxItems, same
// reasoning. A tool that declares "at least one path" gets nothing useful
// from a validator that lets an empty array through.
func TestArrayItemCountsAreEnforcedAtBothEnds(t *testing.T) {
	def := map[string]any{"type": "array", "minItems": float64(2), "maxItems": float64(3)}
	schema := map[string]any{"type": "object", "properties": map[string]any{"n": def}}

	for _, tc := range []struct {
		items   []any
		wantErr string
	}{
		{[]any{}, "must have >= 2 items"},
		{[]any{"a"}, "must have >= 2 items"},
		{[]any{"a", "b", "c", "d"}, "must have <= 3 items"},
	} {
		err := validateSchema(map[string]any{"n": tc.items}, schema)
		if err == nil {
			t.Errorf("%d items were accepted, want %q", len(tc.items), tc.wantErr)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%d items gave %q, want %q", len(tc.items), err, tc.wantErr)
		}
	}
	if err := validateSchema(map[string]any{"n": []any{"a", "b"}}, schema); err != nil {
		t.Errorf("2 items are inside the bounds but were refused: %v", err)
	}
}

// TestAnUnknownFieldIsRefusedUnlessAdditionalPropertiesAllowsIt: the
// closed-world default is what keeps a model from smuggling an argument a
// tool never declared.
func TestAnUnknownFieldIsRefusedUnlessAdditionalPropertiesAllowsIt(t *testing.T) {
	closed := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"known": map[string]any{"type": "string"}},
		"additionalProperties": false,
	}
	if err := validateSchema(map[string]any{"surprise": "x"}, closed); err == nil {
		t.Error("an undeclared field was accepted by a closed schema")
	} else if !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("error %q does not name the unknown field", err)
	}

	open := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"known": map[string]any{"type": "string"}},
		"additionalProperties": true,
	}
	if err := validateSchema(map[string]any{"surprise": "x"}, open); err != nil {
		t.Errorf("an open schema refused an extra field: %v", err)
	}
}

// TestAnEnumDeclaredAsStringsIsAccepted: schemas are written both by hand
// (as []string) and decoded from JSON (as []any). An enum the validator
// cannot read is an enum it silently stops enforcing, so both spellings
// have to reach the same check.
func TestAnEnumDeclaredAsStringsIsAccepted(t *testing.T) {
	for name, enum := range map[string]any{
		"[]string": []string{"red", "green"},
		"[]any":    []any{"red", "green"},
	} {
		schema := map[string]any{
			"type":       "object",
			"properties": map[string]any{"c": map[string]any{"type": "string", "enum": enum}},
		}
		if err := validateSchema(map[string]any{"c": "red"}, schema); err != nil {
			t.Errorf("%s enum refused a declared value: %v", name, err)
		}
		if err := validateSchema(map[string]any{"c": "purple"}, schema); err == nil {
			t.Errorf("%s enum accepted a value it does not declare", name)
		}
	}

	// An enum in a shape the validator cannot read must not silently
	// accept everything under a different guise either.
	weird := map[string]any{
		"type":       "object",
		"properties": map[string]any{"c": map[string]any{"type": "string", "enum": 42}},
	}
	if err := validateSchema(map[string]any{"c": "anything"}, weird); err != nil {
		t.Errorf("an unreadable enum should not itself refuse values: %v", err)
	}
}
