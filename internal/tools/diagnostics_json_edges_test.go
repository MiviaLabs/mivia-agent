package tools

import (
	"math"
	"testing"
)

// The JSON diagnostics parser is total: a producer that emits a shape the
// grammar does not model must still have every element accounted for as a
// raw row. A dropped element is a diagnostic the model never sees, and a
// fabricated one reads as a real finding.

// TestAJSONElementThatIsNotAnObjectBecomesARawRow: a linter that emits an
// array of strings (or numbers) has no message/line/column fields to read.
// Each element must survive as a raw row echoing itself, not vanish and
// not acquire a line 0 that points at nothing.
func TestAJSONElementThatIsNotAnObjectBecomesARawRow(t *testing.T) {
	const input = `[{"message":"real finding","file":"a.go","line":3,"column":1,"severity":"error"},"just a string",42]`
	out := parseForTest(t, input, "")

	requireRows(t, out, []diagnosticsRow{
		{Severity: "error", Message: "real finding", File: "a.go", Line: 3, Column: 1},
		{Severity: "info", Message: `"just a string"`, Raw: true},
		{Severity: "info", Message: `42`, Raw: true},
	})
	if out.Summary.Total != 3 {
		t.Fatalf("summary total = %d; want 3 - every element must be accounted for", out.Summary.Total)
	}
}

// TestAScalarJSONDocumentBecomesOneRawRow: parseJSONRows also accepts a
// document that is neither an array nor an object. The whole document is
// then the single element, so it echoes as one raw row rather than
// yielding nothing at all.
func TestAScalarJSONDocumentBecomesOneRawRow(t *testing.T) {
	for _, tc := range []struct {
		doc  any
		want string
	}{
		{doc: "linter produced no structure", want: `"linter produced no structure"`},
		{doc: float64(7), want: `7`},
		{doc: true, want: `true`},
		{doc: nil, want: `null`},
	} {
		rows := parseJSONRows(tc.doc, "")
		if len(rows) != 1 {
			t.Fatalf("parseJSONRows(%#v) produced %d rows; want exactly 1", tc.doc, len(rows))
		}
		want := diagnosticsRow{Severity: "info", Message: tc.want, Raw: true}
		if rows[0] != want {
			t.Errorf("parseJSONRows(%#v) = %+v; want %+v", tc.doc, rows[0], want)
		}
	}
}

// TestMarshalJSONFallsBackToAReadableRendering: the raw-row echo must
// always produce text. A value with no JSON form (the parser's own inputs
// come from a decoder, so this is the helper's floor) must still render as
// something a reader can act on, never as an empty message that reads as a
// diagnostic with nothing in it.
func TestMarshalJSONFallsBackToAReadableRendering(t *testing.T) {
	if got := marshalJSON(math.NaN()); got != "NaN" {
		t.Fatalf("marshalJSON(NaN) = %q; want the fmt rendering %q", got, "NaN")
	}
	if got := marshalJSON(math.Inf(1)); got != "+Inf" {
		t.Fatalf("marshalJSON(+Inf) = %q; want the fmt rendering %q", got, "+Inf")
	}
	// The encodable path is unchanged, so the fallback is a fallback.
	if got := marshalJSON(map[string]any{"k": "v"}); got != `{"k":"v"}` {
		t.Fatalf("marshalJSON of an encodable value = %q; want compact JSON", got)
	}
}
