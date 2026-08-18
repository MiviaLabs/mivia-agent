package matcher

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// TestMatchFloatCriteriaUseJSONCanonicalForm pins the confirmed bug: scalarString
// canonicalized float64 with strconv 'g', which diverges from encoding/json's own
// float serializer (json.Marshal) for values in [1e-6, 1e-4), [2^63, 1e21), and
// for single-digit negative exponents ("1e-07" vs "1e-7"). A transition criterion
// written as the value's JSON form - the form the engine stores and surfaces -
// silently failed to match, so the route fell to zero_match and the run failed.
// Each subtest uses the exact string encoding/json produces for the value as the
// criterion and asserts the match succeeds.
func TestMatchFloatCriteriaUseJSONCanonicalForm(t *testing.T) {
	cases := []struct {
		name  string
		value float64
		want  string
	}{
		{"integer", 2.0, "2"},
		{"fraction", 0.5, "0.5"},
		{"small_fraction_json_f_form", 1e-5, "0.00001"},
		{"tiny_exponent_leading_zero_stripped", 1e-7, "1e-7"},
		{"large_whole_in_json_f_form", 1e20, "100000000000000000000"},
		{"two_to_63", float64(1 << 63), "9223372036854776000"},
		{"negative_fraction", -2.5, "-2.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transitions := []definition.Transition{
				{From: "a", To: "b", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"n": tc.want}}},
			}
			d, err := Match("a", "succeeded", map[string]any{"n": tc.value}, transitions)
			if err != nil {
				t.Fatalf("Match(%v) with criterion %q: %v", tc.value, tc.want, err)
			}
			if d.Outcome != "matched" || d.ToStepID != "b" {
				t.Fatalf("decision = %+v, want matched -> b", d)
			}
			if got := d.Selected["n"]; got != tc.want {
				t.Fatalf("selected n = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestMatchNonJSONFloatsNeverMatch pins the fail-closed contract for float values
// JSON cannot carry: NaN and ±Inf must never satisfy an output key. Before the
// fix scalarString stringified them ("NaN", "+Inf", "-Inf"), so a criterion
// spelled that way matched and routed on a value that can never appear in a
// step's JSON output. encoding/json refuses to serialize them, so they must be
// treated like any other non-scalar leaf.
func TestMatchNonJSONFloatsNeverMatch(t *testing.T) {
	transitions := []definition.Transition{
		{From: "a", To: "b", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"n": "NaN"}}},
		{From: "a", To: "c", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"n": "+Inf"}}},
		{From: "a", To: "d", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"n": "-Inf"}}},
	}
	outputs := []map[string]any{
		{"n": math.NaN()},
		{"n": math.Inf(1)},
		{"n": math.Inf(-1)},
	}
	for _, out := range outputs {
		d, err := Match("a", "succeeded", out, transitions)
		if err == nil {
			t.Fatalf("Match with output %#v must not match a non-JSON float", out["n"])
		}
		if d.Outcome != "zero_match" {
			t.Fatalf("decision = %+v, want zero_match for %#v", d, out["n"])
		}
	}
}

// TestMatchAllIntegerScalarTypesMatch pins that every JSON-representable Go
// integer type canonicalizes to its decimal string. Before the fix
// int8/int16/uint/uint8/uint16/uint32/uintptr fell into scalarString's default
// branch and never matched any numeric criterion, so a Go-built output map with
// a typed integer silently failed its route with zero_match.
func TestMatchAllIntegerScalarTypesMatch(t *testing.T) {
	values := []struct {
		name string
		val  any
	}{
		{"int", int(3)},
		{"int8", int8(3)},
		{"int16", int16(3)},
		{"int32", int32(3)},
		{"int64", int64(3)},
		{"uint", uint(3)},
		{"uint8", uint8(3)},
		{"uint16", uint16(3)},
		{"uint32", uint32(3)},
		{"uint64", uint64(3)},
		{"uintptr", uintptr(3)},
	}
	for _, tc := range values {
		t.Run(tc.name, func(t *testing.T) {
			transitions := []definition.Transition{
				{From: "a", To: "b", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"n": "3"}}},
			}
			d, err := Match("a", "succeeded", map[string]any{"n": tc.val}, transitions)
			if err != nil {
				t.Fatalf("Match with %T(%v): %v", tc.val, tc.val, err)
			}
			if d.Outcome != "matched" || d.Selected["n"] != "3" {
				t.Fatalf("decision = %+v, want matched n=3", d)
			}
		})
	}
	// Negative values must canonicalize with their sign.
	transitions := []definition.Transition{
		{From: "a", To: "b", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"n": "-7"}}},
	}
	d, err := Match("a", "succeeded", map[string]any{"n": int16(-7)}, transitions)
	if err != nil {
		t.Fatalf("Match with int16(-7): %v", err)
	}
	if d.Outcome != "matched" || d.Selected["n"] != "-7" {
		t.Fatalf("decision = %+v, want matched n=-7", d)
	}
}

// TestMatchFloat32UsesJSONCanonicalForm pins float32 canonicalization through
// encoding/json at bitSize 32 - the engine's serialization of float32 values -
// instead of strconv 'g' (which produced "1e-05" for float32(1e-5) while the
// JSON form is "0.00001").
func TestMatchFloat32UsesJSONCanonicalForm(t *testing.T) {
	transitions := []definition.Transition{
		{From: "a", To: "b", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"n": "0.00001"}}},
	}
	d, err := Match("a", "succeeded", map[string]any{"n": float32(1e-5)}, transitions)
	if err != nil {
		t.Fatalf("Match(float32(1e-5)) with criterion %q: %v", "0.00001", err)
	}
	if d.Outcome != "matched" || d.Selected["n"] != "0.00001" {
		t.Fatalf("decision = %+v, want matched n=0.00001", d)
	}
}

// FuzzScalarStringFloatMatchesJSON is the deterministic property check behind
// the float canonicalization fix: for every float64, scalarString must agree
// exactly with encoding/json's own serialization of the value (the form the
// engine stores), and must refuse values JSON cannot represent (NaN and ±Inf)
// instead of stringifying them.
func FuzzScalarStringFloatMatchesJSON(f *testing.F) {
	for _, seed := range []float64{2, 0.5, -2.5, 1e-5, 1e-7, 1e20, float64(1 << 63), math.Inf(1), math.Inf(-1), math.NaN()} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, v float64) {
		got, ok := scalarString(v)
		raw, err := json.Marshal(v)
		if err != nil {
			// NaN/±Inf are not JSON numbers and must never match a criterion.
			if ok {
				t.Fatalf("scalarString(%v) = (%q, true), want no match for a non-JSON float", v, got)
			}
			return
		}
		if !ok {
			t.Fatalf("scalarString(%v) = no match, want %q", v, string(raw))
		}
		if got != string(raw) {
			t.Fatalf("scalarString(%v) = %q, want %q (encoding/json canonical form)", v, got, string(raw))
		}
	})
}
