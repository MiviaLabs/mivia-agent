package jschema

// Exercises the admission, formatting and helper paths the black-box test file
// cannot reach: defensive branches behind test seams, unexported helpers, and
// the bounded corrective/appendix output.

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestCompileRejectsNilSchema(t *testing.T) {
	_, err := Compile(nil)
	if !errors.Is(err, ErrAdmission) {
		t.Fatalf("err = %v, want admission rejection", err)
	}
	if !strings.Contains(err.Error(), "empty schema") {
		t.Fatalf("err = %v, want it to name the empty schema", err)
	}
}

func TestCompileRejectsUnmarshalableSchema(t *testing.T) {
	_, err := Compile(map[string]any{"type": make(chan int)})
	if !errors.Is(err, ErrAdmission) {
		t.Fatalf("err = %v, want admission rejection", err)
	}
	if !strings.Contains(err.Error(), "marshal") {
		t.Fatalf("err = %v, want it to name the marshal failure", err)
	}
}

func TestCompileRejectsAnUnparseableDocument(t *testing.T) {
	restore := unmarshalSchemaJSON
	unmarshalSchemaJSON = func(io.Reader) (any, error) { return nil, errors.New("boom") }
	defer func() { unmarshalSchemaJSON = restore }()

	_, err := Compile(map[string]any{"type": "object"})
	if !errors.Is(err, ErrAdmission) || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("err = %v, want a parse admission rejection", err)
	}
}

func TestCompileRejectsAnUnaddableResource(t *testing.T) {
	restore := addSchemaResource
	addSchemaResource = func(*jsonschema.Compiler, string, any) error { return errors.New("boom") }
	defer func() { addSchemaResource = restore }()

	_, err := Compile(map[string]any{"type": "object"})
	if !errors.Is(err, ErrAdmission) || !strings.Contains(err.Error(), "add resource") {
		t.Fatalf("err = %v, want an add-resource admission rejection", err)
	}
}

func TestCompileRejectsAnUncloneableSchema(t *testing.T) {
	restore := cloneSchemaJSON
	cloneSchemaJSON = func([]byte, any) error { return errors.New("boom") }
	defer func() { cloneSchemaJSON = restore }()

	_, err := Compile(map[string]any{"type": "object"})
	if !errors.Is(err, ErrAdmission) || !strings.Contains(err.Error(), "clone") {
		t.Fatalf("err = %v, want a clone admission rejection", err)
	}
}

func TestCompileRejectsASchemaTheCompilerRefuses(t *testing.T) {
	_, err := Compile(map[string]any{"type": "definitely-not-a-json-type"})
	if !errors.Is(err, ErrAdmission) || !strings.Contains(err.Error(), "compile") {
		t.Fatalf("err = %v, want a compile admission rejection", err)
	}
}

func TestNilCompiledIsInertRatherThanPanicking(t *testing.T) {
	var c *Compiled
	if raw := c.Raw(); raw != nil {
		t.Fatalf("Raw() = %v, want nil", raw)
	}
	if raw := (&Compiled{}).Raw(); raw != nil {
		t.Fatalf("Raw() on an empty Compiled = %v, want nil", raw)
	}
	if err := c.Validate(map[string]any{}); !errors.Is(err, ErrValidation) {
		t.Fatalf("Validate() = %v, want a validation failure", err)
	}
}

func TestValidateReportsAFailingInstance(t *testing.T) {
	c, err := Compile(map[string]any{
		"type":       "object",
		"properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
		"required":   []any{"ok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(map[string]any{}); !errors.Is(err, ErrValidation) {
		t.Fatalf("Validate() = %v, want a validation failure", err)
	}
	if _, err := c.ValidateJSONBytes([]byte(`{"ok":"no"}`)); !errors.Is(err, ErrValidation) {
		t.Fatalf("ValidateJSONBytes() = %v, want a validation failure", err)
	}
	if _, err := c.ValidateJSONBytes([]byte(`{`)); !errors.Is(err, ErrValidation) {
		t.Fatalf("ValidateJSONBytes(malformed) = %v, want a validation failure", err)
	}
	raw := c.Raw()
	if raw["type"] != "object" {
		t.Fatalf("Raw() = %#v, want the admitted schema", raw)
	}
	raw["type"] = "mutated"
	if again := c.Raw(); again["type"] != "object" {
		t.Fatal("Raw() handed out a reference to the admitted schema")
	}
}

func TestStripOneCodeFenceLeavesEverythingItCannotSafelyStrip(t *testing.T) {
	cases := map[string]string{
		"plain body":          "plain body",
		"```only":             "```only",
		"```\nunterminated":   "```\nunterminated",
		"```\n```\nbody\n```": "```\n```\nbody\n```",
	}
	for input, want := range cases {
		if got := StripOneCodeFence(input); got != want {
			t.Errorf("StripOneCodeFence(%q) = %q, want %q", input, got, want)
		}
	}
	if got := StripOneCodeFence("```json\n{\"a\":1}\n```"); got != `{"a":1}` {
		t.Errorf("well-formed fence not stripped: %q", got)
	}
}

func TestFormatCorrectiveWithSchemaRestatesSchema(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"required":   []any{"verdict", "findings", "inspected"},
		"properties": map[string]any{"verdict": map[string]any{"type": "string"}},
	}
	msg := FormatCorrectiveWithSchema(errors.New("missing properties 'verdict'"), schema, nil)
	if !strings.Contains(msg, `"required"`) || !strings.Contains(msg, "verdict") {
		t.Fatalf("corrective message should restate the schema: %s", msg)
	}
	if !strings.Contains(msg, "did not match") {
		t.Fatalf("corrective message should keep the validation detail: %s", msg)
	}
	if len(msg) > MaxCorrectiveBytes {
		t.Fatalf("corrective message %d bytes exceeds %d", len(msg), MaxCorrectiveBytes)
	}
	if got := FormatCorrectiveWithSchema(errors.New("secret"), schema, func(string) string { return "[r]" }); !strings.Contains(got, "[r]") {
		t.Fatalf("redaction not applied: %s", got)
	}
}

func TestFormatCorrectiveIsBoundedAndRedactable(t *testing.T) {
	many := errors.New(strings.Repeat("line\n", MaxValidationErrors+3))
	msg := FormatCorrective(many, nil)
	if !strings.Contains(msg, "…") {
		t.Fatalf("over-long error list was not elided: %q", msg)
	}
	long := errors.New(strings.Repeat("x", MaxCorrectiveBytes*2))
	if got := len(FormatCorrective(long, nil)); got != MaxCorrectiveBytes {
		t.Fatalf("corrective length = %d, want %d", got, MaxCorrectiveBytes)
	}
	redacted := FormatCorrective(errors.New("secret"), func(string) string { return "[redacted]" })
	if redacted != "[redacted]" {
		t.Fatalf("redactor was not applied: %q", redacted)
	}
	if msg := FormatCorrective(nil, nil); !strings.Contains(msg, "does not match the required schema") {
		t.Fatalf("nil error corrective = %q", msg)
	}
}

func TestPromptAppendixFallsBackWhenTheSchemaCannotBeMarshaled(t *testing.T) {
	appendix := PromptAppendix(map[string]any{"type": make(chan int)})
	if strings.Contains(appendix, "this schema") {
		t.Fatalf("appendix leaked an unmarshalable schema: %q", appendix)
	}
	if !strings.Contains(appendix, "required output schema") {
		t.Fatalf("appendix fallback = %q", appendix)
	}
	if got := PromptAppendix(map[string]any{"type": "object"}); !strings.Contains(got, `"type":"object"`) {
		t.Fatalf("appendix did not carry the schema: %q", got)
	}
}

func TestRejectAllLoaderRefusesEveryURL(t *testing.T) {
	// Unreachable through Compile - rejectRemoteRefs refuses the $ref first -
	// so the last line of defence is tested where it lives.
	if _, err := (rejectAllLoader{}).Load("https://example.com/s.json"); err == nil {
		t.Fatal("loader accepted a remote URL")
	}
}

func TestRejectRemoteRefsWalksNestedContainers(t *testing.T) {
	nestedMap := map[string]any{
		"properties": map[string]any{"a": map[string]any{"$ref": "https://example.com/s.json"}},
	}
	if err := rejectRemoteRefs(nestedMap, ""); err == nil {
		t.Fatal("nested remote $ref accepted")
	} else if !strings.Contains(err.Error(), "/properties/a") {
		t.Fatalf("error does not name the path: %v", err)
	}
	nestedList := map[string]any{"anyOf": []any{map[string]any{"$ref": "s.json"}}}
	if err := rejectRemoteRefs(nestedList, ""); err == nil {
		t.Fatal("remote $ref inside a list accepted")
	}
	if err := rejectRemoteRefs(map[string]any{"$ref": "#/$defs/a"}, ""); err != nil {
		t.Fatalf("in-document $ref refused: %v", err)
	}
}

func TestIsRemoteRefClassifiesEveryForm(t *testing.T) {
	cases := map[string]bool{
		"":                             false,
		"#":                            false,
		"#/$defs/a":                    false,
		"https://example.com/s.json":   true,
		"//example.com/s.json":         true,
		"sibling.json":                 true,
		"../parent/schema.json#/$defs": true,
	}
	for ref, want := range cases {
		if got := isRemoteRef(ref); got != want {
			t.Errorf("isRemoteRef(%q) = %v, want %v", ref, got, want)
		}
	}
}

func TestPathOrRootNamesTheRoot(t *testing.T) {
	if got := pathOrRoot(""); got != "/" {
		t.Fatalf("pathOrRoot(\"\") = %q, want %q", got, "/")
	}
	if got := pathOrRoot("/properties/a"); got != "/properties/a" {
		t.Fatalf("pathOrRoot did not pass a real path through: %q", got)
	}
}

func TestFormatValidationErrPrefersBasicOutputAndStaysBounded(t *testing.T) {
	if got := formatValidationErr(nil); got != "" {
		t.Fatalf("formatValidationErr(nil) = %q", got)
	}
	long := errors.New(strings.Repeat("x", MaxCorrectiveBytes*2))
	if got := len(formatValidationErr(long)); got != MaxCorrectiveBytes {
		t.Fatalf("length = %d, want %d", got, MaxCorrectiveBytes)
	}
	if got := formatValidationErr(errors.New("short")); got != "short" {
		t.Fatalf("short error rewritten: %q", got)
	}

	c, err := Compile(map[string]any{
		"type":       "object",
		"properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	verr := c.sch.Validate(map[string]any{"ok": "no"})
	if verr == nil {
		t.Fatal("instance unexpectedly valid")
	}
	got := formatValidationErr(verr)
	if !json.Valid([]byte(got)) {
		t.Fatalf("a jsonschema error did not format as its basic output: %q", got)
	}

	// A validation error whose basic output exceeds the bound falls back to the
	// plain, truncated error text.
	wide := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	instance := map[string]any{}
	for i := 0; i < 60; i++ {
		instance[strings.Repeat("k", 20)+string(rune('a'+i%26))+string(rune('a'+i/26))] = i
	}
	wc, err := Compile(wide)
	if err != nil {
		t.Fatal(err)
	}
	wideErr := wc.sch.Validate(instance)
	if wideErr == nil {
		t.Fatal("wide instance unexpectedly valid")
	}
	if got := len(formatValidationErr(wideErr)); got > MaxCorrectiveBytes {
		t.Fatalf("oversized validation error not bounded: %d bytes", got)
	}
}
