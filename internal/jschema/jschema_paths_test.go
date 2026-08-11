package jschema

// Exercises the admission, formatting and helper paths the black-box test file
// cannot reach: defensive branches behind test seams, unexported helpers, and
// the bounded corrective/appendix output.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/textutil"
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

func TestFormatCorrectiveWithSchemaNeverTruncatesTheSchema(t *testing.T) {
	// A shipped-size schema plus a long error list must keep the ENTIRE
	// schema (the point of the restatement) and give up error detail instead.
	props := map[string]any{}
	for i := 0; i < 20; i++ {
		props[fmt.Sprintf("field_%02d", i)] = map[string]any{
			// Meta keywords (description) are stripped from the rendered
			// contract, so size the schema with instance-shape keywords.
			"type": "string", "minLength": 5, "pattern": "^[a-z]+$",
		}
	}
	schema := map[string]any{"type": "object", "properties": props}
	many := errors.New(strings.Repeat("line\n", MaxValidationErrors+3))
	msg := FormatCorrectiveWithSchema(many, schema, nil)
	if !strings.Contains(msg, `"field_19"`) || !strings.Contains(msg, `"type":"object"`) {
		t.Fatalf("schema tail truncated by corrective (len=%d): %.120s…", len(msg), msg)
	}
	// The error detail must give way before the schema does: with the full
	// schema the message necessarily exceeds the soft cap, but the errors
	// list must be cut far below its raw length.
	if strings.Contains(msg, "line\nline\nline\nline\nline") {
		t.Fatalf("error detail was not bounded: %.160s", msg)
	}
}

func TestStripOneCodeFenceHandlesFourBacktickFences(t *testing.T) {
	in := "````json\n{\"a\":1,\"code\":\"```\"}\n````"
	if got := StripOneCodeFence(in); got != "{\"a\":1,\"code\":\"```\"}" {
		t.Fatalf("4-backtick fence not stripped: %q", got)
	}
	// Mismatched fence lengths are not a well-formed wrap: leave untouched.
	if got := StripOneCodeFence("```json\n{\"a\":1}\n````"); got != "```json\n{\"a\":1}\n````" {
		t.Fatalf("mismatched fence stripped: %q", got)
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

func TestStripOneCodeFenceAllowsThreeBacktickContentInFourBacktickFence(t *testing.T) {
	// A 4-backtick fence whose body contains a line starting with exactly 3
	// backticks is valid CommonMark (§4.5): a fenced code block opened with N
	// backticks can contain lines of N-1 or fewer backticks. The function's doc
	// comment advertises 4-backtick support but the hardcoded triple-backtick
	// guard at line ~160 incorrectly rejects this input.
	in := "````\nregular line\n``` not a fence, 3 backticks < 4\nalso ok\n````"
	want := "regular line\n``` not a fence, 3 backticks < 4\nalso ok"
	if got := StripOneCodeFence(in); got != want {
		t.Fatalf("4-backtick fence with 3-backtick body line was not stripped: got %q, want %q", got, want)
	}
}

func TestStripOneCodeFenceRejectsNBacktickContentInNBacktickFence(t *testing.T) {
	// A body line starting with N backticks inside an N-backtick fence is
	// ambiguous and must not be stripped.  N=4 case.
	in := "````\nregular\n````ambiguous — 4 backticks = fence width\n````"
	if got := StripOneCodeFence(in); got != in {
		t.Fatalf("4-backtick fence with 4-backtick body line was stripped: %q", got)
	}
	// 5-backtick fence: body lines with 3 or 4 backticks must strip, body line
	// with 5 backticks must reject.
	in5 := "`````\n```` four backticks < 5, ok\n``` three backticks < 5, ok\n````` five backticks == 5, ambiguous\n`````"
	if got := StripOneCodeFence(in5); got != in5 {
		t.Fatalf("5-backtick fence with 5-backtick body line was stripped: %q", got)
	}
	// 5-backtick fence with only safe body lines: must strip.
	in5safe := "`````\n```` four, ok\n``` three, ok\n`````"
	want5safe := "```` four, ok\n``` three, ok"
	if got := StripOneCodeFence(in5safe); got != want5safe {
		t.Fatalf("5-backtick fence with safe body lines was not stripped: got %q, want %q", got, want5safe)
	}
}

// correctiveRuneMix returns a string of exactly n bytes mixing multi-byte é
// runes with ASCII fillers (at least one multi-byte rune when n >= 2), so a
// raw byte cut at any offset can land inside a rune.
func correctiveRuneMix(n int) string {
	s := strings.Repeat("é", n/2)
	if pad := n - len(s); pad > 0 {
		s += strings.Repeat("x", pad)
	}
	return s
}

// TestCorrectiveMessagesNeverSplitsRunes pins the DC-6 bound/truncation
// invariant: every corrective message built for the model must be valid UTF-8
// and stay within MaxCorrectiveBytes. The regression (RED before the fix):
// the three formatters cut their detail at raw byte offsets, so a detail whose
// cut offset lands inside a multi-byte rune carried a dangling lead byte into
// the retry turn.
func TestCorrectiveMessagesNeverSplitsRunes(t *testing.T) {
	const capBytes = MaxCorrectiveBytes
	smallSchema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
		"required":   []any{"ok"},
	}

	// Both é-alignment variants: "x" puts the é runes at odd offsets, "xx" at
	// even offsets, so whichever parity the raw cut has, one variant splits.
	oddAligned := "x" + strings.Repeat("é", capBytes/2)   // 1025 bytes
	evenAligned := "xx" + strings.Repeat("é", capBytes/2) // 1026 bytes

	assertCorrectiveParityValidity(t, oddAligned, evenAligned, smallSchema)
	assertCorrectiveBoundaryTable(t, capBytes, smallSchema)

	// Redaction must still apply after rune-safe truncation: a rune-splitting
	// detail with a fixed redactor returns exactly the redactor output.
	if got := FormatCorrective(errors.New(oddAligned), func(string) string { return "[redacted]" }); got != "[redacted]" {
		t.Fatalf("redactor not applied after rune-safe truncation: %q", got)
	}

	// Pin the fix to the shared helper: on a plain (non-jsonschema) error,
	// formatValidationErr must equal the rune-safe truncation of the error
	// text exactly. RED before the fix: the raw cut leaves the dangling lead
	// byte, TruncateRuneSafe backs off to the rune start.
	if got := formatValidationErr(errors.New(oddAligned)); got != textutil.TruncateRuneSafe(oddAligned, capBytes) {
		t.Fatalf("formatValidationErr = %q, want TruncateRuneSafe output %q", got, textutil.TruncateRuneSafe(oddAligned, capBytes))
	}
}

// assertCorrectiveParityValidity runs both é-alignment variants through all
// three corrective formatters. "x" puts the é runes at odd byte offsets, "xx"
// at even offsets, so whichever parity the raw cut has, one variant splits on
// the pre-fix code; every output must be valid UTF-8 and stay within the cap.
func assertCorrectiveParityValidity(t *testing.T, oddAligned, evenAligned string, smallSchema map[string]any) {
	t.Helper()
	const capBytes = MaxCorrectiveBytes

	// formatValidationErr must never emit invalid UTF-8. Guaranteed RED: the
	// raw cut at byte 1024 of oddAligned ends on an é continuation byte.
	for _, detail := range []string{oddAligned, evenAligned} {
		m := formatValidationErr(errors.New(detail))
		if !utf8.ValidString(m) {
			t.Fatalf("formatValidationErr emitted invalid UTF-8 for %d-byte detail", len(detail))
		}
		if len(m) > capBytes {
			t.Fatalf("formatValidationErr = %d bytes, cap %d", len(m), capBytes)
		}
	}

	// FormatCorrective with both parities.
	for _, detail := range []string{oddAligned, evenAligned} {
		m := FormatCorrective(errors.New(detail), nil)
		if !utf8.ValidString(m) {
			t.Fatalf("FormatCorrective emitted invalid UTF-8 for %d-byte detail", len(detail))
		}
		if len(m) > capBytes {
			t.Fatalf("FormatCorrective = %d bytes, cap %d", len(m), capBytes)
		}
	}

	// FormatCorrectiveWithSchema: the cut at room (budget minus prefix and
	// schema section) must also land on a rune boundary for either parity.
	// The small schema leaves room, so the cap bound holds here too.
	for _, detail := range []string{oddAligned, evenAligned} {
		m := FormatCorrectiveWithSchema(errors.New(detail), smallSchema, nil)
		if !utf8.ValidString(m) {
			t.Fatalf("FormatCorrectiveWithSchema emitted invalid UTF-8 for %d-byte detail", len(detail))
		}
		if len(m) > capBytes {
			t.Fatalf("FormatCorrectiveWithSchema = %d bytes, cap %d", len(m), capBytes)
		}
	}
}

// assertCorrectiveBoundaryTable runs the DC-6 probes (0, max-1, max, max+1,
// 2*max) with pure multi-byte and ASCII+multi-byte mixes across all three
// corrective formatters: every output must be valid UTF-8 and stay bounded.
func assertCorrectiveBoundaryTable(t *testing.T, capBytes int, smallSchema map[string]any) {
	t.Helper()
	boundaries := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"max-1", correctiveRuneMix(capBytes - 1)},
		{"max", correctiveRuneMix(capBytes)},
		{"max+1", correctiveRuneMix(capBytes + 1)},
		{"2*max", correctiveRuneMix(capBytes * 2)},
		{"pure multi-byte max", strings.Repeat("é", capBytes/2)},
		{"pure multi-byte 2*max", strings.Repeat("é", capBytes)},
	}
	for _, bc := range boundaries {
		for _, formatter := range []string{"FormatCorrective", "FormatCorrectiveWithSchema", "formatValidationErr"} {
			var m string
			switch formatter {
			case "FormatCorrective":
				m = FormatCorrective(errors.New(bc.in), nil)
			case "FormatCorrectiveWithSchema":
				m = FormatCorrectiveWithSchema(errors.New(bc.in), smallSchema, nil)
			default:
				m = formatValidationErr(errors.New(bc.in))
			}
			if !utf8.ValidString(m) {
				t.Fatalf("%s(%q) emitted invalid UTF-8", formatter, bc.name)
			}
			if len(m) > capBytes {
				t.Fatalf("%s(%q) = %d bytes, cap %d", formatter, bc.name, len(m), capBytes)
			}
		}
	}
}

// FuzzCorrectiveNeverSplitsRunes pins the validity/bound property across
// arbitrary input: the formatters never panic, always emit valid UTF-8, and
// (where the soft cap applies) stay within MaxCorrectiveBytes.
func FuzzCorrectiveNeverSplitsRunes(f *testing.F) {
	seeds := []string{
		"",
		strings.Repeat("x", MaxCorrectiveBytes*2),
		"x" + strings.Repeat("é", MaxCorrectiveBytes/2),
		"xx" + strings.Repeat("é", MaxCorrectiveBytes/2),
		strings.Repeat("🙂", 300),
		strings.Repeat("日本語", 400),
		strings.Repeat("x", 1000) + strings.Repeat("é", 600),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	smallSchema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
		"required":   []any{"ok"},
	}
	f.Fuzz(func(t *testing.T, s string) {
		if m := FormatCorrective(errors.New(s), nil); !utf8.ValidString(m) {
			t.Fatalf("FormatCorrective emitted invalid UTF-8: %q", m)
		} else if len(m) > MaxCorrectiveBytes {
			t.Fatalf("FormatCorrective = %d bytes, cap %d", len(m), MaxCorrectiveBytes)
		}
		// FormatCorrectiveWithSchema may legitimately exceed the soft cap to
		// preserve the never-truncated schema contract; assert validity only.
		if m := FormatCorrectiveWithSchema(errors.New(s), smallSchema, nil); !utf8.ValidString(m) {
			t.Fatalf("FormatCorrectiveWithSchema emitted invalid UTF-8: %q", m)
		}
		if m := formatValidationErr(errors.New(s)); !utf8.ValidString(m) {
			t.Fatalf("formatValidationErr emitted invalid UTF-8: %q", m)
		} else if len(m) > MaxCorrectiveBytes {
			t.Fatalf("formatValidationErr = %d bytes, cap %d", len(m), MaxCorrectiveBytes)
		}
	})
}

func FuzzStripOneCodeFence(f *testing.F) {
	// Seed corpus with key shapes.
	f.Add("```json\n{\"a\":1}\n```")
	f.Add("````json\n{\"a\":1,\"code\":\"```\"}\n````")
	f.Add("````\nregular line\n``` not a fence\n````")
	f.Add("````\nregular\n````ambiguous\n````")
	f.Add("```\n```inner```\n```")
	f.Add("plain body")
	f.Add("```only")
	f.Add("```\nunterminated")
	f.Fuzz(func(t *testing.T, s string) {
		// Invariant: the function never panics.
		got := StripOneCodeFence(s)
		// Output is either the original string or a proper substring of the
		// trimmed input.
		trimmed := strings.TrimSpace(s)
		if got != s && got != trimmed {
			// When output differs from input, it must be the body content
			// stripped of a well-formed fence.
			lines := strings.Split(trimmed, "\n")
			if len(lines) < 2 {
				t.Fatalf("output differs but trimmed has < 2 lines: %q → %q", s, got)
			}
			open := lines[0]
			backticks := 0
			for backticks < len(open) && open[backticks] == '`' {
				backticks++
			}
			if backticks < 3 {
				t.Fatalf("stripped but opening fence has %d backticks: %q → %q", backticks, s, got)
			}
			// Verify no body line starts with N or more backticks.
			repeat := strings.Repeat("`", backticks)
			for _, line := range strings.Split(got, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), repeat) {
					t.Fatalf("stripped body contains a line starting with %d backticks: %q in %q", backticks, line, got)
				}
			}
		}
	})
}
