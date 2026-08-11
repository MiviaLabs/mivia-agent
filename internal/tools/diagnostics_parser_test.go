package tools

// RED-phase (locked plan v2, task p2) contract tests for the get_diagnostics
// parser. They pin grammar v1 and the summary accounting that a later wave
// implements inside parseDiagnosticsOutput; the p1 skeleton returns an empty
// result, so every assertion below (except the empty-input boundary) fails
// until the parser lands. That is the intended RED state.
//
// Trust/security framing (locked plan v2 item 11): parseDiagnosticsOutput
// consumes raw captured subprocess output, which is untrusted data from the
// model's point of view. The contract therefore demands the parser never drop
// a line, strip C0 control characters (tab preserved) from line-mode text so
// hostile output cannot smuggle escape sequences into model-facing rows, and
// keep the summary an exact tally (total == errors+warnings+infos+raw) so a
// misbehaving or adversarial producer cannot make the counts lie. Nothing
// here grants execution authority: the parser is pure, and the tool's
// authority story is unchanged from run_command's allowlist.

import (
	"reflect"
	"strings"
	"testing"
)

// parseForTest runs the parser over input and fails the test on error; the
// parser is total (never drops a line), so every shape test expects nil error.
func parseForTest(t *testing.T, input, workspaceRoot string) diagnosticsOutput {
	t.Helper()
	out, err := parseDiagnosticsOutput([]byte(input), workspaceRoot)
	if err != nil {
		t.Fatalf("parseDiagnosticsOutput(%q, %q) error: %v", input, workspaceRoot, err)
	}
	return out
}

// requireRows pins the exact row sequence (fields compared individually via
// DeepEqual against the want struct for readable failure output).
func requireRows(t *testing.T, out diagnosticsOutput, want []diagnosticsRow) {
	t.Helper()
	if len(out.Rows) != len(want) {
		t.Fatalf("rows = %d, want %d\n got: %+v\nwant: %+v", len(out.Rows), len(want), out.Rows, want)
	}
	for i := range want {
		if !reflect.DeepEqual(out.Rows[i], want[i]) {
			t.Errorf("row %d:\n got: %+v\nwant: %+v", i, out.Rows[i], want[i])
		}
	}
}

// wantSummary pins each summary bucket plus the locked-plan invariants: total
// is exactly errors+warnings+infos+raw (never an independent tally), and the
// row count agrees with total because rows are never dropped. files counts
// distinct non-empty file paths after relativization.
func wantSummary(t *testing.T, out diagnosticsOutput, errors, warnings, infos, raw, files int) {
	t.Helper()
	got := out.Summary
	if got.Errors != errors {
		t.Errorf("summary.errors = %d, want %d", got.Errors, errors)
	}
	if got.Warnings != warnings {
		t.Errorf("summary.warnings = %d, want %d", got.Warnings, warnings)
	}
	if got.Infos != infos {
		t.Errorf("summary.infos = %d, want %d", got.Infos, infos)
	}
	if got.Raw != raw {
		t.Errorf("summary.raw = %d, want %d", got.Raw, raw)
	}
	if got.Files != files {
		t.Errorf("summary.files = %d, want %d", got.Files, files)
	}
	wantTotal := errors + warnings + infos + raw
	if got.Total != wantTotal {
		t.Errorf("summary.total = %d, want %d (= errors+warnings+infos+raw)", got.Total, wantTotal)
	}
	if got.Total != got.Errors+got.Warnings+got.Infos+got.Raw {
		t.Errorf("summary.total (%d) != errors+warnings+infos+raw (%d+%d+%d+%d)",
			got.Total, got.Errors, got.Warnings, got.Infos, got.Raw)
	}
	if len(out.Rows) != got.Total {
		t.Errorf("len(rows) = %d, summary.total = %d (rows must never be dropped)", len(out.Rows), got.Total)
	}
}

// TestDiagnosticsParseGCCStyle pins the gcc shape path:line:col: severity:
// message, including the fatal->error normalization.
func TestDiagnosticsParseGCCStyle(t *testing.T) {
	input := "internal/tools/diagnostics.go:42:5: error: undefined: foo\n" +
		"cmd/main.go:7:1: fatal: nil pointer dereference\n"
	out := parseForTest(t, input, "")
	requireRows(t, out, []diagnosticsRow{
		{Severity: "error", Message: "undefined: foo", File: "internal/tools/diagnostics.go", Line: 42, Column: 5},
		{Severity: "error", Message: "nil pointer dereference", File: "cmd/main.go", Line: 7, Column: 1},
	})
	wantSummary(t, out, 2, 0, 0, 0, 2)
}

// TestDiagnosticsParseVetShapes pins both vet shapes: path:line: severity:
// message (with severity) and path:line: message (severity absent -> info).
func TestDiagnosticsParseVetShapes(t *testing.T) {
	input := "main.go:12: warning: unused variable x\n" + // vet: path:line: severity: message
		"main.go:15: unreachable code\n" // vet: path:line: message
	out := parseForTest(t, input, "")
	requireRows(t, out, []diagnosticsRow{
		{Severity: "warning", Message: "unused variable x", File: "main.go", Line: 12},
		{Severity: "info", Message: "unreachable code", File: "main.go", Line: 15},
	})
	wantSummary(t, out, 0, 1, 1, 0, 1)
}

// TestDiagnosticsParseTSCStyle pins the tsc shape path:line:col: [error|warning]
// TSnnnn: message; the TS code is dropped from the message.
func TestDiagnosticsParseTSCStyle(t *testing.T) {
	input := "src/main.ts:12:5: error TS2322: Type 'string' is not assignable to type 'number'.\n" +
		"src/other.ts:3:1: warning TS6133: 'x' is declared but its value is never read.\n"
	out := parseForTest(t, input, "")
	requireRows(t, out, []diagnosticsRow{
		{Severity: "error", Message: "Type 'string' is not assignable to type 'number'.", File: "src/main.ts", Line: 12, Column: 5},
		{Severity: "warning", Message: "'x' is declared but its value is never read.", File: "src/other.ts", Line: 3, Column: 1},
	})
	wantSummary(t, out, 1, 1, 0, 0, 2)
}

// TestDiagnosticsParseJSONArray pins JSON array mode with mixed key aliases.
func TestDiagnosticsParseJSONArray(t *testing.T) {
	input := `[
  {"file":"a.go","line":1,"column":2,"severity":"error","message":"boom"},
  {"path":"b.go","line":3,"col":4,"level":"warning","msg":"careful"}
]`
	out := parseForTest(t, input, "")
	requireRows(t, out, []diagnosticsRow{
		{Severity: "error", Message: "boom", File: "a.go", Line: 1, Column: 2},
		{Severity: "warning", Message: "careful", File: "b.go", Line: 3, Column: 4},
	})
	wantSummary(t, out, 1, 1, 0, 0, 2)
}

// TestDiagnosticsParseJSONObjectArrays pins JSON object mode: the rows live in
// a diagnostics/results/issues array (all three aliases).
func TestDiagnosticsParseJSONObjectArrays(t *testing.T) {
	for _, tc := range []struct{ key, file string }{
		{"issues", "iss.go"},
		{"diagnostics", "diag.go"},
		{"results", "res.go"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			input := `{"` + tc.key + `":[{"file":"` + tc.file + `","line":9,"column":1,"severity":"error","message":"obj msg"}]}`
			out := parseForTest(t, input, "")
			requireRows(t, out, []diagnosticsRow{
				{Severity: "error", Message: "obj msg", File: tc.file, Line: 9, Column: 1},
			})
			wantSummary(t, out, 1, 0, 0, 0, 1)
		})
	}
}

// TestDiagnosticsParseJSONKeyAliases pins the case-insensitive alias sets:
// file|path|filename, line, column|col, severity|level|type, and
// message|msg|text|description.
func TestDiagnosticsParseJSONKeyAliases(t *testing.T) {
	input := `[
  {"file":"a.go","line":1,"column":2,"severity":"error","message":"m1"},
  {"path":"b.go","line":2,"col":3,"level":"warn","msg":"m2"},
  {"filename":"c.go","line":3,"column":4,"type":"note","text":"m3"},
  {"path":"d.go","line":4,"col":5,"severity":"fatal","description":"m4"},
  {"FILE":"e.go","LINE":5,"COL":6,"SEVERITY":"error","MESSAGE":"m5"}
]`
	out := parseForTest(t, input, "")
	requireRows(t, out, []diagnosticsRow{
		{Severity: "error", Message: "m1", File: "a.go", Line: 1, Column: 2},
		{Severity: "warning", Message: "m2", File: "b.go", Line: 2, Column: 3},
		{Severity: "info", Message: "m3", File: "c.go", Line: 3, Column: 4},
		{Severity: "error", Message: "m4", File: "d.go", Line: 4, Column: 5},
		{Severity: "error", Message: "m5", File: "e.go", Line: 5, Column: 6},
	})
	wantSummary(t, out, 3, 1, 1, 0, 5)
}

// TestDiagnosticsParseJSONMissingMessageRaw pins that a JSON object without a
// usable message becomes a raw row (raw:true, severity info) rather than a
// structured row; an empty-string message is equally unusable.
func TestDiagnosticsParseJSONMissingMessageRaw(t *testing.T) {
	for _, tc := range []struct{ name, input, wantEcho string }{
		{
			name:     "no message key",
			input:    `{"file":"x.go","line":1,"severity":"error"}`,
			wantEcho: "x.go",
		},
		{
			name:     "empty message",
			input:    `{"file":"y.go","line":2,"message":""}`,
			wantEcho: "y.go",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := parseForTest(t, tc.input, "")
			if len(out.Rows) != 1 {
				t.Fatalf("rows = %d, want 1", len(out.Rows))
			}
			r := out.Rows[0]
			if !r.Raw {
				t.Errorf("row %+v: object without a usable message must become a raw row", r)
			}
			if r.Severity != "info" {
				t.Errorf("raw row severity = %q, want %q", r.Severity, "info")
			}
			if !strings.Contains(r.Message, tc.wantEcho) {
				t.Errorf("raw row message = %q, want it to echo the object (containing %q)", r.Message, tc.wantEcho)
			}
			wantSummary(t, out, 0, 0, 0, 1, 0)
		})
	}
}

// TestDiagnosticsParseMalformedJSONFallsBackToLines pins that malformed JSON
// falls through to the line parser; here that yields a single raw row echoing
// the text verbatim.
func TestDiagnosticsParseMalformedJSONFallsBackToLines(t *testing.T) {
	input := `{"file": "a.go", "line": 1,`
	out := parseForTest(t, input, "")
	requireRows(t, out, []diagnosticsRow{
		{Severity: "info", Message: input, Raw: true},
	})
	wantSummary(t, out, 0, 0, 0, 1, 0)
}

// TestDiagnosticsParseCRLF pins that trailing \r is stripped from every line
// (line mode and JSON mode) so messages never carry it.
func TestDiagnosticsParseCRLF(t *testing.T) {
	input := "a.go:1:2: error: first\r\nb.go:2:3: warning: second\r\n"
	out := parseForTest(t, input, "")
	requireRows(t, out, []diagnosticsRow{
		{Severity: "error", Message: "first", File: "a.go", Line: 1, Column: 2},
		{Severity: "warning", Message: "second", File: "b.go", Line: 2, Column: 3},
	})
	wantSummary(t, out, 1, 1, 0, 0, 2)

	jsonOut := parseForTest(t, "[{\"file\":\"a.go\",\"line\":1,\"message\":\"m\"}]\r\n", "")
	requireRows(t, jsonOut, []diagnosticsRow{
		{Severity: "info", Message: "m", File: "a.go", Line: 1},
	})
	wantSummary(t, jsonOut, 0, 0, 1, 0, 1)
}

// TestDiagnosticsParseWindowsDrivePath pins that a Windows drive-letter path
// parses as the file position with the drive colon intact.
func TestDiagnosticsParseWindowsDrivePath(t *testing.T) {
	input := "C:\\src\\x.go:12:5: error: boom\n"
	out := parseForTest(t, input, "")
	requireRows(t, out, []diagnosticsRow{
		{Severity: "error", Message: "boom", File: `C:\src\x.go`, Line: 12, Column: 5},
	})
	wantSummary(t, out, 1, 0, 0, 0, 1)
}

// TestDiagnosticsParseTrailingColonArrowMessage pins that the message is
// everything after the position/severity tokens: embedded colons and "-->"
// arrow text survive verbatim instead of being re-parsed as position markers.
func TestDiagnosticsParseTrailingColonArrowMessage(t *testing.T) {
	input := "cmd/run.go:8:2: error: compile failed: see --> below\n" +
		"main.go:12: --> points here\n"
	out := parseForTest(t, input, "")
	requireRows(t, out, []diagnosticsRow{
		{Severity: "error", Message: "compile failed: see --> below", File: "cmd/run.go", Line: 8, Column: 2},
		{Severity: "info", Message: "--> points here", File: "main.go", Line: 12},
	})
	wantSummary(t, out, 1, 0, 1, 0, 2)
}

// TestDiagnosticsParseSeverityNormalization pins the severity map for known
// tokens plus the absent->info rule of the vet path:line: message shape.
func TestDiagnosticsParseSeverityNormalization(t *testing.T) {
	lines := []string{
		"s.go:1:1: error: e1",       // error -> error
		"s.go:2:1: fatal: e2",       // fatal -> error
		"s.go:3:1: warning: w1",     // warning -> warning
		"s.go:4:1: warn: w2",        // warn -> warning
		"s.go:5:1: note: i1",        // note -> info
		"s.go:6:1: info: i2",        // info -> info
		"s.go:7:1: information: i3", // information -> info
		"s.go:8:1: hint: i4",        // hint -> info
		"a.go:9: absent severity",   // vet path:line: message -> absent -> info
	}
	out := parseForTest(t, strings.Join(lines, "\n")+"\n", "")
	requireRows(t, out, []diagnosticsRow{
		{Severity: "error", Message: "e1", File: "s.go", Line: 1, Column: 1},
		{Severity: "error", Message: "e2", File: "s.go", Line: 2, Column: 1},
		{Severity: "warning", Message: "w1", File: "s.go", Line: 3, Column: 1},
		{Severity: "warning", Message: "w2", File: "s.go", Line: 4, Column: 1},
		{Severity: "info", Message: "i1", File: "s.go", Line: 5, Column: 1},
		{Severity: "info", Message: "i2", File: "s.go", Line: 6, Column: 1},
		{Severity: "info", Message: "i3", File: "s.go", Line: 7, Column: 1},
		{Severity: "info", Message: "i4", File: "s.go", Line: 8, Column: 1},
		{Severity: "info", Message: "absent severity", File: "a.go", Line: 9},
	})
	wantSummary(t, out, 2, 2, 5, 0, 2)
}

// TestDiagnosticsParseUnknownSeverityVerbatim pins the audit-corrected
// grammar (finding P1): a 3-segment line whose first word is not a known
// severity is a bare message with a colon (go vet emits "p:l:c: msg"), so it
// must NOT be fabricated into a structured row with a made-up severity and a
// truncated message. It falls to the raw fallback with the whole line
// preserved verbatim.
func TestDiagnosticsParseUnknownSeverityVerbatim(t *testing.T) {
	out := parseForTest(t, "u.go:1:1: weird: odd message\n", "")
	requireRows(t, out, []diagnosticsRow{
		{Severity: "info", Message: "u.go:1:1: weird: odd message", Raw: true},
	})
	s := out.Summary
	if s.Total != 1 {
		t.Errorf("summary.total = %d, want 1", s.Total)
	}
	if s.Raw != 1 {
		t.Errorf("summary.raw = %d, want 1", s.Raw)
	}
	if s.Total != s.Errors+s.Warnings+s.Infos+s.Raw {
		t.Errorf("summary.total (%d) != errors+warnings+infos+raw (%d+%d+%d+%d)",
			s.Total, s.Errors, s.Warnings, s.Infos, s.Raw)
	}
	if len(out.Rows) != s.Total {
		t.Errorf("len(rows) = %d, summary.total = %d", len(out.Rows), s.Total)
	}
}

// TestDiagnosticsParseGoVetThreeSegment pins finding P1: the real go vet /
// golangci-lint shape "path:line:col: message" (no severity token) must not
// be swallowed by the vet shape with the column leaking into the message, and
// a message that starts with a colon-word ("undefined: bar") must not be
// mislabeled with a fabricated severity. Both become raw rows with the full
// line preserved.
func TestDiagnosticsParseGoVetThreeSegment(t *testing.T) {
	out := parseForTest(t, "internal/foo.go:42:5: unused variable x\ninternal/foo.go:42:5: undefined: bar\n", "")
	requireRows(t, out, []diagnosticsRow{
		{Severity: "info", Message: "internal/foo.go:42:5: unused variable x", Raw: true},
		{Severity: "info", Message: "internal/foo.go:42:5: undefined: bar", Raw: true},
	})
	// Raw rows carry no file field, so the files count is 0 (raw rows do not
	// claim a file).
	wantSummary(t, out, 0, 0, 0, 2, 0)
}

// TestDiagnosticsParseJSONStringLine pins finding P2: a JSON position field
// with a string value ("line":"12") is present but unusable; the element must
// become a raw row, never a structured row pointing at line 0.
func TestDiagnosticsParseJSONStringLine(t *testing.T) {
	out := parseForTest(t, `[{"file":"a.go","line":"12","message":"m"}]`, "")
	requireRows(t, out, []diagnosticsRow{
		{Severity: "info", Message: `{"file":"a.go","line":"12","message":"m"}`, Raw: true},
	})
	wantSummary(t, out, 0, 0, 0, 1, 0)
}

// TestDiagnosticsParseJSONAliasTypeFallback pins finding P3: a
// present-but-wrong-typed higher-priority alias must not suppress a usable
// lower-priority alias.
func TestDiagnosticsParseJSONAliasTypeFallback(t *testing.T) {
	out := parseForTest(t, `[{"message":42,"msg":"real","file":"a.go","line":1}]`, "")
	requireRows(t, out, []diagnosticsRow{
		{Severity: "info", Message: "real", File: "a.go", Line: 1},
	})
	out2 := parseForTest(t, `{"diagnostics":"oops","results":[{"file":"b.go","line":3,"message":"m"}]}`, "")
	requireRows(t, out2, []diagnosticsRow{
		{Severity: "info", Message: "m", File: "b.go", Line: 3},
	})
	out3 := parseForTest(t, `[{"column":"5","col":6,"message":"m"}]`, "")
	requireRows(t, out3, []diagnosticsRow{
		{Severity: "info", Message: "m", Column: 6},
	})
}

// TestDiagnosticsParseJSONDuplicateCaseKeys pins finding P4: duplicate
// case-variant keys must resolve deterministically, so identical input yields
// identical rows on every parse (Go map iteration order must not decide a
// row).
func TestDiagnosticsParseJSONDuplicateCaseKeys(t *testing.T) {
	input := `[{"line":1,"LINE":2,"message":"m"}]`
	first := parseForTest(t, input, "")
	second := parseForTest(t, input, "")
	if len(first.Rows) != 1 || len(second.Rows) != 1 {
		t.Fatalf("rows = %d and %d, want 1 each", len(first.Rows), len(second.Rows))
	}
	if first.Rows[0] != second.Rows[0] {
		t.Fatalf("duplicate case-variant keys resolved nondeterministically: %+v vs %+v", first.Rows[0], second.Rows[0])
	}
	if first.Rows[0].Line == 0 {
		t.Fatalf("row %+v: a usable line key must resolve", first.Rows[0])
	}
}

// TestDiagnosticsParsePathRelativization pins that absolute paths under the
// workspace root are relativized (line mode and JSON mode) while an empty
// root leaves paths verbatim.
func TestDiagnosticsParsePathRelativization(t *testing.T) {
	root := "/home/me/proj"
	out := parseForTest(t,
		"/home/me/proj/cmd/app.go:5:1: error: boom\n"+
			"/home/me/proj/internal/x.go:2:3: warning: y\n",
		root)
	requireRows(t, out, []diagnosticsRow{
		{Severity: "error", Message: "boom", File: "cmd/app.go", Line: 5, Column: 1},
		{Severity: "warning", Message: "y", File: "internal/x.go", Line: 2, Column: 3},
	})
	wantSummary(t, out, 1, 1, 0, 0, 2)

	jsonOut := parseForTest(t,
		`{"file":"/home/me/proj/src/a.go","line":1,"column":1,"severity":"error","message":"m"}`,
		root)
	requireRows(t, jsonOut, []diagnosticsRow{
		{Severity: "error", Message: "m", File: "src/a.go", Line: 1, Column: 1},
	})
	wantSummary(t, jsonOut, 1, 0, 0, 0, 1)

	verbatim := parseForTest(t, "/home/me/proj/cmd/app.go:5:1: error: boom\n", "")
	requireRows(t, verbatim, []diagnosticsRow{
		{Severity: "error", Message: "boom", File: "/home/me/proj/cmd/app.go", Line: 5, Column: 1},
	})
	wantSummary(t, verbatim, 1, 0, 0, 0, 1)
}

// TestDiagnosticsParseRawFallbackKeepsEveryLine pins that unmatched non-empty
// lines become raw rows (raw:true, severity info, message echoed verbatim) so
// no line is ever dropped, and that raw rows are tallied in the raw bucket
// (not the info bucket) so the buckets partition rows and total stays exact.
func TestDiagnosticsParseRawFallbackKeepsEveryLine(t *testing.T) {
	input := "this is not a diagnostic\n" +
		"neither is this\n" +
		"a.go:1:1: error: real one\n"
	out := parseForTest(t, input, "")
	requireRows(t, out, []diagnosticsRow{
		{Severity: "info", Message: "this is not a diagnostic", Raw: true},
		{Severity: "info", Message: "neither is this", Raw: true},
		{Severity: "error", Message: "real one", File: "a.go", Line: 1, Column: 1},
	})
	wantSummary(t, out, 1, 0, 0, 2, 1)
}

// TestDiagnosticsParseEmptyInput pins the boundary: no input means no rows and
// an all-zero summary.
func TestDiagnosticsParseEmptyInput(t *testing.T) {
	for _, input := range []string{"", "\n", "\r\n"} {
		out := parseForTest(t, input, "")
		if len(out.Rows) != 0 {
			t.Errorf("rows = %d, want 0 for input %q", len(out.Rows), input)
		}
		s := out.Summary
		if s.Total != 0 || s.Errors != 0 || s.Warnings != 0 || s.Infos != 0 || s.Raw != 0 || s.Files != 0 {
			t.Errorf("summary = %+v, want all zeros for input %q", s, input)
		}
	}
	if out, err := parseDiagnosticsOutput(nil, ""); err != nil || len(out.Rows) != 0 {
		t.Errorf("nil input: rows = %d, err = %v; want 0 rows, nil error", len(out.Rows), err)
	}
}

// TestDiagnosticsParseJSONMultiLineMessage pins that a JSON string may carry an
// embedded newline and the parser must not split on it: JSON detection happens
// before line splitting, so the row's message keeps the newline. Control-char
// hygiene is scoped to line-mode text; \n can only reach a message through a
// JSON string, where it is structural content, not a separator.
func TestDiagnosticsParseJSONMultiLineMessage(t *testing.T) {
	input := `{"file":"a.go","line":1,"column":2,"severity":"error","message":"line one\nline two"}`
	out := parseForTest(t, input, "")
	requireRows(t, out, []diagnosticsRow{
		{Severity: "error", Message: "line one\nline two", File: "a.go", Line: 1, Column: 2},
	})
	wantSummary(t, out, 1, 0, 0, 0, 1)
}

// TestDiagnosticsParseControlCharHygiene pins that C0 control characters (tab
// excepted) are stripped from line-mode messages so hostile captured output
// cannot smuggle escape sequences into model-facing rows; \t survives.
func TestDiagnosticsParseControlCharHygiene(t *testing.T) {
	input := "x.go:1:1: error: bad\x00byte\n" +
		"x.go:2:1: warning: \x1b[33myellow\x1b[0m\n" +
		"x.go:3:1: error: a\tb\n" +
		"noise\x00line\n"
	out := parseForTest(t, input, "")
	requireRows(t, out, []diagnosticsRow{
		{Severity: "error", Message: "badbyte", File: "x.go", Line: 1, Column: 1},
		{Severity: "warning", Message: "[33myellow[0m", File: "x.go", Line: 2, Column: 1},
		{Severity: "error", Message: "a\tb", File: "x.go", Line: 3, Column: 1},
		{Severity: "info", Message: "noiseline", Raw: true},
	})
	wantSummary(t, out, 2, 1, 0, 1, 1)
}
