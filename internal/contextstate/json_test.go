package contextstate

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestCanonicalJSONPreservesNumberTypes proves R0-1: MarshalCanonical on a
// struct with numeric fields must produce unquoted numbers, not quoted strings.
func TestCanonicalJSONPreservesNumberTypes(t *testing.T) {
	t.Parallel()
	type numStruct struct {
		U64 uint64  `json:"u64"`
		I64 int64   `json:"i64"`
		I   int     `json:"i"`
		F64 float64 `json:"f64"`
	}
	v := numStruct{U64: 42, I64: -99, I: 7, F64: 3.14}
	data, err := MarshalCanonical(v)
	if err != nil {
		t.Fatal(err)
	}
	// Each field must appear as an unquoted number: "u64":42, not "u64":"42"
	for _, pair := range []struct{ key, num string }{
		{"u64", "42"},
		{"i64", "-99"},
		{"i", "7"},
		{"f64", "3.14"},
	} {
		want := `"` + pair.key + `":` + pair.num
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("MarshalCanonical output missing %q in %s", want, data)
		}
		corrupted := `"` + pair.key + `":"` + pair.num + `"`
		if bytes.Contains(data, []byte(corrupted)) {
			t.Errorf("MarshalCanonical output contains corrupted number-as-string %q in %s", corrupted, data)
		}
	}
}

// TestMarshalCanonicalRoundTripUint64 checks that a full MarshalCanonical →
// UnmarshalCanonical round-trip preserves uint64 boundary values.
func TestMarshalCanonicalRoundTripUint64(t *testing.T) {
	t.Parallel()
	type trip struct {
		Seq   uint64 `json:"seq"`
		Small uint64 `json:"small"`
		Max   uint64 `json:"max"`
	}
	original := trip{Seq: 1, Small: 0, Max: 18446744073709551615}
	data, err := MarshalCanonical(original)
	if err != nil {
		t.Fatal(err)
	}
	// The output must be valid JSON.
	if !json.Valid(data) {
		t.Fatalf("MarshalCanonical produced invalid JSON: %s", data)
	}
	var decoded trip
	if err := UnmarshalCanonical(data, &decoded); err != nil {
		t.Fatalf("UnmarshalCanonical failed: %v", err)
	}
	if decoded != original {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, original)
	}
	// Also check with int and float64.
	type mixed struct {
		I int     `json:"i"`
		F float64 `json:"f"`
	}
	om := mixed{I: -1, F: 0.5}
	data, err = MarshalCanonical(om)
	if err != nil {
		t.Fatal(err)
	}
	var dm mixed
	if err := UnmarshalCanonical(data, &dm); err != nil {
		t.Fatalf("UnmarshalCanonical mixed failed: %v", err)
	}
	if dm.I != om.I || dm.F != om.F {
		t.Errorf("mixed round-trip: got %+v, want %+v", dm, om)
	}
}

// TestCanonicalJSONIsIdempotent checks that canonicalizeJSON is idempotent:
// a second pass on the output produces byte-identical output.
func TestCanonicalJSONIsIdempotent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
	}{
		{"plain number", `{"n":42}`},
		{"two numbers sorted", `{"a":1,"b":2}`},
		{"array of numbers", `[1,2,3]`},
		{"mixed string and number", `{"s":"hello","n":42}`},
		{"booleans and null", `{"b":true,"f":false,"n":null}`},
		{"negative float", `{"v":-3.14}`},
		{"zero", `{"v":0}`},
		{"max uint64", `{"v":18446744073709551615}`},
		{"scientific notation", `{"v":1e10}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out1, err := canonicalizeJSON([]byte(tc.input))
			if err != nil {
				t.Fatalf("first pass: %v", err)
			}
			out2, err := canonicalizeJSON(out1)
			if err != nil {
				t.Fatalf("second pass: %v", err)
			}
			if !bytes.Equal(out1, out2) {
				t.Errorf("canonicalizeJSON not idempotent:\n  pass1: %s\n  pass2: %s", out1, out2)
			}
			// Output must be valid JSON.
			if !json.Valid(out2) {
				t.Errorf("idempotent output is not valid JSON: %s", out2)
			}
		})
	}
}

// TestCanonicalJSONHandlesNegativeNumbers checks that negative numbers survive
// canonicalization unquoted.
func TestCanonicalJSONHandlesNegativeNumbers(t *testing.T) {
	t.Parallel()
	out, err := canonicalizeJSON([]byte(`{"v":-42}`))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`-42`)) {
		t.Fatalf("expected -42 unquoted in %s", out)
	}
	if bytes.Contains(out, []byte(`"-42"`)) {
		t.Fatalf("unexpected quoted number in %s", out)
	}
}

// TestCanonicalJSONHandlesScientificNotation checks that scientific-notation
// number literals survive canonicalization unquoted.
func TestCanonicalJSONHandlesScientificNotation(t *testing.T) {
	t.Parallel()
	cases := []string{`{"v":1e10}`, `{"v":1.5e-3}`, `{"v":-0.0}`}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			out, err := canonicalizeJSON([]byte(input))
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid(out) {
				t.Fatalf("output is not valid JSON: %s", out)
			}
			if bytes.Contains(out, []byte(`"1e10"`)) || bytes.Contains(out, []byte(`"1.5e-3"`)) || bytes.Contains(out, []byte(`"-0.0"`)) {
				t.Errorf("scientific-notation number was quoted in %s", out)
			}
		})
	}
}

// TestCanonicalJSONHandlesBoundaryNumbers checks that 0, max uint64, and min
// int64 survive canonicalization unquoted.
func TestCanonicalJSONHandlesBoundaryNumbers(t *testing.T) {
	t.Parallel()
	cases := []string{
		`{"v":0}`,
		`{"v":18446744073709551615}`,
		`{"v":-9223372036854775808}`,
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			out, err := canonicalizeJSON([]byte(input))
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid(out) {
				t.Fatalf("output is not valid JSON: %s", out)
			}
			// No quoted digit after a colon — quick smoke check.
			if bytes.Contains(out, []byte(`:"`)) {
				// Only acceptable if it's a genuine string field; but these inputs
				// have only number values, so ":"" is corruption.
				t.Errorf("output appears to quote a value in %s", out)
			}
		})
	}
}

// TestCanonicalJSONNestedNumbers checks that numbers at any nesting depth and
// in arrays survive canonicalization unquoted.
func TestCanonicalJSONNestedNumbers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
	}{
		{"deeply nested object", `{"a":{"b":{"c":1}}}`},
		{"nested arrays", `[[[1]]]`},
		{"mixed nested", `{"a":[1,{"b":2}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := canonicalizeJSON([]byte(tc.input))
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid(out) {
				t.Fatalf("output is not valid JSON: %s", out)
			}
			// Idempotent.
			out2, err := canonicalizeJSON(out)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out, out2) {
				t.Errorf("not idempotent:\n  pass1: %s\n  pass2: %s", out, out2)
			}
		})
	}
}

// TestCanonicalJSONMixedStringAndNumber checks that a string "42" stays quoted
// while a number 42 stays unquoted; they must not collapse.
func TestCanonicalJSONMixedStringAndNumber(t *testing.T) {
	t.Parallel()
	out, err := canonicalizeJSON([]byte(`{"name":"42","value":42}`))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"name":"42"`)) {
		t.Errorf("string value lost its quotes: %s", out)
	}
	if !bytes.Contains(out, []byte(`"value":42`)) {
		t.Errorf("number value lost: %s", out)
	}
	if bytes.Contains(out, []byte(`"value":"42"`)) {
		t.Errorf("number value corrupted to quoted string: %s", out)
	}
}

// TestCanonicalJSONEmptyStructures checks that {} and [] round-trip unchanged.
func TestCanonicalJSONEmptyStructures(t *testing.T) {
	t.Parallel()
	for _, input := range []string{`{}`, `[]`} {
		t.Run(input, func(t *testing.T) {
			out, err := canonicalizeJSON([]byte(input))
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != input {
				t.Errorf("empty structure changed: got %s, want %s", out, input)
			}
		})
	}
}

// TestCanonicalJSONPreservesBooleansAndNull checks that true, false, and null
// survive canonicalization unchanged.
func TestCanonicalJSONPreservesBooleansAndNull(t *testing.T) {
	t.Parallel()
	out, err := canonicalizeJSON([]byte(`{"t":true,"f":false,"n":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"t":true`)) {
		t.Errorf("true corrupted: %s", out)
	}
	if !bytes.Contains(out, []byte(`"f":false`)) {
		t.Errorf("false corrupted: %s", out)
	}
	if !bytes.Contains(out, []byte(`"n":null`)) {
		t.Errorf("null corrupted: %s", out)
	}
}

// FuzzCanonicalJSON is a deterministic fuzz target for canonicalizeJSON. It
// accepts arbitrary bytes, runs canonicalizeJSON, and when successful verifies
// that the output is valid JSON, idempotent, and free of quoted-number
// corruption.
func FuzzCanonicalJSON(f *testing.F) {
	// Seed corpus with interesting cases.
	f.Add([]byte(`{"n":42}`))
	f.Add([]byte(`{"a":1,"b":2}`))
	f.Add([]byte(`[1,2,3]`))
	f.Add([]byte(`{"s":"hello","n":42}`))
	f.Add([]byte(`{"b":true,"f":false,"n":null}`))
	f.Add([]byte(`{"v":-42}`))
	f.Add([]byte(`{"v":1e10}`))
	f.Add([]byte(`{"v":18446744073709551615}`))
	f.Add([]byte(`{"a":{"b":{"c":1}}}`))
	f.Add([]byte(`[[[1]]]`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`{"name":"42","value":42}`))
	f.Add([]byte(`{"test":"number","int":123,"neg":-1,"float":3.14}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		out, err := canonicalizeJSON(data)
		if err != nil {
			// Non-JSON or malformed input is expected to fail.
			return
		}
		// (a) Output must be valid JSON.
		if !json.Valid(out) {
			t.Errorf("canonicalizeJSON produced invalid JSON: input=%q output=%s", data, out)
		}
		// (b) Output must be idempotent.
		out2, err := canonicalizeJSON(out)
		if err != nil {
			t.Errorf("second pass failed on first-pass output: %v; input=%q first=%s", err, data, out)
			return
		}
		if !bytes.Equal(out, out2) {
			t.Errorf("canonicalizeJSON not idempotent: input=%q\n  first: %s\n  second: %s", data, out, out2)
		}
		// (c) No quoted-number corruption: value types must be preserved.
		// Parse input and output with UseNumber, then walk both trees
		// structurally. Any json.Number in the input that became a string in
		// the output is corruption.
		var rawIn, rawOut any
		decIn := json.NewDecoder(bytes.NewReader(data))
		decIn.UseNumber()
		if decIn.Decode(&rawIn) != nil {
			return // original not valid JSON, nothing more to check
		}
		decOut := json.NewDecoder(bytes.NewReader(out))
		decOut.UseNumber()
		if err := decOut.Decode(&rawOut); err != nil {
			t.Errorf("canonical output not decodable: %v; output=%s", err, out)
			return
		}
		if !jsonTypesPreserved(rawIn, rawOut) {
			t.Errorf("type changed during canonicalization: input=%q output=%s", data, out)
		}
	})
}

// jsonTypesPreserved returns true when the two JSON trees have the same value
// types at every path. A json.Number in a must be a json.Number in b; a string
// must be a string; bool must be bool; nil must be nil.
func jsonTypesPreserved(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			return false
		}
		for k, va := range av {
			vb, ok := bv[k]
			if !ok || !jsonTypesPreserved(va, vb) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !jsonTypesPreserved(av[i], bv[i]) {
				return false
			}
		}
		return true
	case json.Number:
		_, ok := b.(json.Number)
		return ok
	case string:
		_, ok := b.(string)
		return ok
	case bool:
		_, ok := b.(bool)
		return ok
	case nil:
		return b == nil
	default:
		return false
	}
}
