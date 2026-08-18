package template

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestRenderEmptyAndLiteralOnly pins the empty and no-binding passthrough
// behavior: an empty template renders to an empty string, and a template with
// no {{ }} binding is returned verbatim.
func TestRenderEmptyAndLiteralOnly(t *testing.T) {
	if got, err := Render("", nil, nil, 100, 100); err != nil || got != "" {
		t.Fatalf("empty template rendered %q, err %v", got, err)
	}
	if got, err := Render("plain text with no bindings", nil, nil, 100, 100); err != nil || got != "plain text with no bindings" {
		t.Fatalf("literal template rendered %q, err %v", got, err)
	}
}

// TestRenderRepeatedBinding renders the same binding twice. Each occurrence
// must expand independently with no state carried between expansions.
func TestRenderRepeatedBinding(t *testing.T) {
	got, err := Render("{{ inputs.task }} and {{ inputs.task }}", map[string]any{"task": "build"}, nil, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if got != "build and build" {
		t.Fatalf("repeated binding rendered %q", got)
	}
}

// TestRenderDelimiterCharactersInsideValues pins that {{ and }} inside a
// binding VALUE are emitted verbatim and are never re-scanned as bindings.
// Expansion is single-pass in template order.
func TestRenderDelimiterCharactersInsideValues(t *testing.T) {
	got, err := Render("a {{ inputs.x }} b {{ inputs.y }}",
		map[string]any{"x": "x }} y", "y": "z {{ w"}, nil, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if got != "a x }} y b z {{ w" {
		t.Fatalf("rendered %q", got)
	}
}

// TestRenderRejectsMalformedBindings covers the malformed binding-name shapes:
// unclosed delimiters, an empty name, a single-part name, a dotted value part,
// and an unknown prefix. Each must fail closed rather than render partially.
func TestRenderRejectsMalformedBindings(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"unclosed opening", "prefix {{ inputs.task"},
		{"unclosed single brace", "{{ inputs.task }"},
		{"empty name", "{{ }}"},
		{"single part", "{{ inputs }}"},
		{"dotted value part", "{{ inputs.a.b }}"},
		{"unknown prefix", "{{ outputs.task }}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Render(tc.src, map[string]any{"task": "x"}, nil, 100, 100); err == nil {
				t.Fatalf("malformed template %q was accepted", tc.src)
			}
		})
	}
}

// TestRenderRejectsOversizedLiteralOutput pins the rendered-size cap against a
// template whose LITERAL text alone exceeds maxRenderedBytes. The cap must
// fire even when no binding write ever happens (a binding after the oversized
// literal must also fail).
func TestRenderRejectsOversizedLiteralOutput(t *testing.T) {
	big := strings.Repeat("a", 200)
	if _, err := Render(big, nil, nil, 100, 100); err == nil {
		t.Fatal("oversized literal-only template was accepted")
	}
	if _, err := Render(big+"{{ inputs.task }}", map[string]any{"task": "x"}, nil, 100, 100); err == nil {
		t.Fatal("oversized literal prefix was accepted")
	}
}

// TestRenderRejectsInvalidUTF8Source pins the source encoding check: a
// template that is not valid UTF-8 is refused before any expansion.
func TestRenderRejectsInvalidUTF8Source(t *testing.T) {
	if _, err := Render("a\xffb", nil, nil, 100, 100); err == nil {
		t.Fatal("invalid UTF-8 template was accepted")
	}
}

// TestRenderRejectsInvalidUTF8BindingValue is the RED regression for the
// fail-open encoding gap in encodeBinding: a string binding value that is not
// valid UTF-8 must fail instead of flowing verbatim into the rendered output.
// The template source is validated for UTF-8, but before the fix a malformed
// binding value produced a rendered prompt (or a delivered title/commit
// message) carrying invalid UTF-8 with no error - text that model providers
// and GitHub reject or silently mangle.
func TestRenderRejectsInvalidUTF8BindingValue(t *testing.T) {
	if _, err := Render("{{ inputs.s }}", map[string]any{"s": "ok\xffbad"}, nil, 100, 200); err == nil {
		t.Fatal("invalid UTF-8 input binding value was accepted")
	}
	if _, err := Render("{{ evidence.s }}", nil, map[string]any{"s": "\xff"}, 100, 200); err == nil {
		t.Fatal("invalid UTF-8 evidence binding value was accepted")
	}
}

// TestRenderMultiByteBoundary pins that a valid multi-byte rune in a binding
// value survives intact and the rendered output stays valid UTF-8.
func TestRenderMultiByteBoundary(t *testing.T) {
	got, err := Render("{{ inputs.s }}", map[string]any{"s": "h\u00e9llo"}, nil, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(got) || got != "h\u00e9llo" {
		t.Fatalf("rendered %q (valid utf8: %v)", got, utf8.ValidString(got))
	}
}

// TestRenderJSONEncodedBindingIsValidUTF8 pins that non-string binding values
// (JSON-encoded) never carry invalid bytes into the rendered output.
func TestRenderJSONEncodedBindingIsValidUTF8(t *testing.T) {
	got, err := Render("{{ inputs.m }}", map[string]any{"m": map[string]any{"a": "\xff"}}, nil, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("JSON-encoded binding produced invalid UTF-8: %q", got)
	}
}

// invalidUTF8Marshaler is a json.Marshaler whose output carries an invalid
// UTF-8 byte inside a JSON string literal. encoding/json compacts Marshaler
// output with a syntax-only pass and does not validate UTF-8, so json.Marshal
// returns these bytes verbatim with a nil error.
type invalidUTF8Marshaler struct{}

func (invalidUTF8Marshaler) MarshalJSON() ([]byte, error) {
	return []byte("\"ok\xffbad\""), nil
}

// TestRenderRejectsInvalidUTF8FromJSONMarshaler is the RED regression for the
// remaining fail-open gap in encodeBinding: the non-string branch returned
// json.Marshal(value) verbatim, and json.Marshal validates UTF-8 only for the
// strings it encodes - a value implementing json.Marshaler (json.RawMessage
// included) is copied verbatim after a syntax-only compact, so invalid bytes
// inside a JSON string literal rendered successfully with no error, violating
// the invariant that every successful render is valid UTF-8. Both shapes must
// fail closed, while a valid Marshaler value keeps rendering.
func TestRenderRejectsInvalidUTF8FromJSONMarshaler(t *testing.T) {
	raw := json.RawMessage("\"ok\xffbad\"")
	if _, err := Render("{{ inputs.r }}", map[string]any{"r": raw}, nil, 100, 200); err == nil {
		t.Fatal("json.RawMessage binding value with invalid UTF-8 was accepted")
	}
	if _, err := Render("{{ inputs.m }}", map[string]any{"m": invalidUTF8Marshaler{}}, nil, 100, 200); err == nil {
		t.Fatal("json.Marshaler binding value emitting invalid UTF-8 was accepted")
	}
	if _, err := Render("{{ evidence.m }}", nil, map[string]any{"m": invalidUTF8Marshaler{}}, 100, 200); err == nil {
		t.Fatal("json.Marshaler evidence value emitting invalid UTF-8 was accepted")
	}
	if _, err := Render("{{ inputs.r }}", map[string]any{"r": json.RawMessage("{\"ok\":true}")}, nil, 100, 200); err != nil {
		t.Fatalf("valid json.RawMessage binding was refused: %v", err)
	}
}

// assertRenderInvariants renders source with the given bindings and pins the
// invariants every successful render must satisfy: valid UTF-8 output that
// respects the rendered-size cap (the default cap when the caller passes a
// non-positive value).
func assertRenderInvariants(t *testing.T, source string, inputs, evidence map[string]any, capBinding, capRendered int) {
	t.Helper()
	out, err := Render(source, inputs, evidence, capBinding, capRendered)
	if err != nil {
		return
	}
	if !utf8.ValidString(out) {
		t.Fatalf("Render returned invalid UTF-8 for source %q: %q", source, out)
	}
	maxRendered := capRendered
	if maxRendered <= 0 {
		maxRendered = DefaultMaxRenderedBytes
	}
	if len(out) > maxRendered {
		t.Fatalf("rendered %d bytes, exceeding cap %d (source %q)", len(out), maxRendered, source)
	}
}

// FuzzRender guards Render's core invariants under arbitrary input: it must
// never panic, and any successful render must be valid UTF-8 that respects
// the requested rendered-size cap (the default cap when the caller passes a
// non-positive value). Both encodeBinding branches are exercised: the string
// branch directly, and the json.Marshal branch through a json.RawMessage
// binding, whose bytes are copied verbatim after a syntax-only compact - so
// invalid UTF-8 inside the quoted value must fail the render rather than flow
// into the output. The seeds cover the empty, literal-only, malformed, and
// oversized shapes plus invalid-UTF-8 binding values.
func FuzzRender(f *testing.F) {
	f.Add("", "", 100, 100)
	f.Add("plain text", "", 100, 100)
	f.Add("{{ inputs.a }}", "x", 100, 100)
	f.Add("{{ }}", "", 10, 100)
	f.Add("{{ inputs.a }} tail {{ evidence.b }}", "\xff", 1000, 1000)
	f.Add("prefix {{ inputs.a }} suffix", "h\u00e9llo", 100, 100)
	f.Fuzz(func(t *testing.T, source, value string, capBinding, capRendered int) {
		assertRenderInvariants(t, source, map[string]any{"a": value}, map[string]any{"b": value}, capBinding, capRendered)
		assertRenderInvariants(t, "{{ inputs.a }}", map[string]any{"a": json.RawMessage("\"" + value + "\"")}, nil, capBinding, capRendered)
	})
}
