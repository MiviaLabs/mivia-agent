package tools

// This file is the pure parser half of the get_diagnostics tool (locked plan
// v2). It owns the data shapes the tool's envelope is built from - row,
// summary, envelope - and the deterministic parse entry point that turns raw
// captured command output into structured rows. Purity is the point: nothing
// here spawns a process, touches the filesystem, or reads configuration, so
// the parser is unit-testable in isolation and the tool's Execute path stays a
// thin composition (capture -> redact -> parse -> marshal).
//
// Grammar v1 (locked plan v2 item 6): the parser detects JSON mode first. When
// the trimmed input starts with "[" or "{" and parses as JSON, every array
// element (or the rows array of an object) becomes a row. Any other input is
// split into lines and matched against the gcc/vet/tsc line shapes. Unmatched
// lines and unusable JSON objects become raw rows, so the parser never drops a
// line. File paths under the workspace root are relativized. Line-mode
// messages lose C0 control characters (tab preserved). The summary is an exact
// tally of the returned rows.
//
// Security framing (locked plan v2 item 11): get_diagnostics adds no
// execution authority beyond run_command - the command runs under the same
// effective allowlist and the same process helpers. The envelope discloses
// the per-call argv to the model, so operators who treat argv as sensitive
// should weigh that against the same disclosure run_command already makes in
// its result header.

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// diagnosticsRow is one parsed finding. File/line/column are positional facts
// when the source format carries them; Raw marks a line the parser could not
// match to a known shape and echoed verbatim (raw fallback rows always carry
// severity "info"). Message is the human-readable text, cleaned of C0 control
// characters (tab preserved) before it lands here.
type diagnosticsRow struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	Raw      bool   `json:"raw,omitempty"`
}

// diagnosticsSummary is the honest count contract (locked plan v2 item 6):
// total is exactly errors+warnings+infos+raw, never an independent tally, and
// files counts distinct relativized file paths across rows ("" excluded).
// max_rows truncation happens after the summary is derived and adjusts these
// counts down so they always describe the rows actually returned.
type diagnosticsSummary struct {
	Total    int `json:"total"`
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
	Infos    int `json:"infos"`
	Raw      int `json:"raw"`
	Files    int `json:"files"`
}

// diagnosticsOutput is what the parser produces: the rows plus their summary.
// The tool wraps this into diagnosticsEnvelope with the command/exit metadata.
type diagnosticsOutput struct {
	Rows    []diagnosticsRow   `json:"rows"`
	Summary diagnosticsSummary `json:"summary"`
}

// diagnosticsEnvelopeVersion is the envelope shape version. It is a static
// catalogue contract: bumping it signals a breaking shape change to consumers,
// not a per-run value.
const diagnosticsEnvelopeVersion = 1

// diagnosticsEnvelope is the model-facing JSON shape of the get_diagnostics
// tool result (locked plan v2 item 5). command names the executed argv
// (redacted, shell-safe formatted); command_name names the selected commands
// entry and is omitted on the legacy single-argv surface. exit_code is a
// pointer so it is omitted when the process never started; truncated reports
// that rows were dropped by max_rows (or that the envelope was
// budget-refused); error carries envelope-level failures (resolve/start/
// refusal) while per-run findings live in rows.
type diagnosticsEnvelope struct {
	Version     int                `json:"version"`
	Command     string             `json:"command,omitempty"`
	CommandName string             `json:"command_name,omitempty"`
	ExitCode    *int               `json:"exit_code,omitempty"`
	Rows        []diagnosticsRow   `json:"rows"`
	Summary     diagnosticsSummary `json:"summary"`
	Truncated   bool               `json:"truncated,omitempty"`
	Error       string             `json:"error,omitempty"`
}

// diagnosticsLineGCC matches the gcc/tsc shape "file:line:col: rest". The file
// part is lazy, so a Windows drive path ("C:\src\x.go") stays intact.
var diagnosticsLineGCC = regexp.MustCompile(`^(.+?):(\d+):(\d+):\s+(.*)$`)

// diagnosticsLineVet matches the vet shapes "file:line: rest".
var diagnosticsLineVet = regexp.MustCompile(`^(.+?):(\d+):\s+(.*)$`)

// diagnosticsJSONRowKeys are the object keys whose array value holds the rows.
var diagnosticsJSONRowKeys = []string{"diagnostics", "results", "issues"}

// parseDiagnosticsOutput parses raw captured command output into structured
// rows and their summary. It is pure and total: every non-empty input line is
// accounted for (never dropped) - unmatched lines become raw rows. The
// workspaceRoot is used only to relativize file paths against the workspace
// root; an empty root leaves paths verbatim.
func parseDiagnosticsOutput(stdout []byte, workspaceRoot string) (diagnosticsOutput, error) {
	input := string(stdout)
	if trimmed := strings.TrimSpace(input); looksLikeJSON(trimmed) {
		var doc any
		if err := json.Unmarshal([]byte(trimmed), &doc); err == nil {
			return finalizeDiagnostics(parseJSONRows(doc, workspaceRoot)), nil
		}
	}
	return finalizeDiagnostics(parseLineRows(input, workspaceRoot)), nil
}

// looksLikeJSON reports whether the trimmed input could be a JSON document.
func looksLikeJSON(trimmed string) bool {
	if trimmed == "" {
		return false
	}
	return trimmed[0] == '[' || trimmed[0] == '{'
}

// parseLineRows splits the input into lines and parses each one. Empty lines
// are dropped; every other line becomes a structured or a raw row.
func parseLineRows(input, workspaceRoot string) []diagnosticsRow {
	var rows []diagnosticsRow
	for _, rawLine := range strings.Split(input, "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if row, ok := parseLineRow(line, workspaceRoot); ok {
			rows = append(rows, row)
		} else {
			rows = append(rows, diagnosticsRow{
				Severity: "info",
				Message:  stripControlChars(line),
				Raw:      true,
			})
		}
	}
	return rows
}

// parseLineRow parses one line against the gcc/vet/tsc shapes. ok is false
// when the line matches no shape or carries no usable message; the caller then
// emits a raw row.
func parseLineRow(line, workspaceRoot string) (diagnosticsRow, bool) {
	if m := diagnosticsLineGCC.FindStringSubmatch(line); m != nil {
		return parsePositionedRow(m[1], m[2], m[3], m[4], workspaceRoot, true)
	}
	if m := diagnosticsLineVet.FindStringSubmatch(line); m != nil {
		return parsePositionedRow(m[1], m[2], "", m[3], workspaceRoot, false)
	}
	return diagnosticsRow{}, false
}

// parsePositionedRow builds a structured row from the position fields and the
// text after them. requireSeverity is true for the gcc/tsc shape, where the
// text must be "<severity>: <message>"; it is false for the vet shape, where a
// bare message defaults to severity info. ok is false when the text carries no
// usable message or the shape would corrupt it (audit findings P1).
func parsePositionedRow(file, lineStr, colStr, rest, workspaceRoot string, requireSeverity bool) (diagnosticsRow, bool) {
	sevTok, msg, hasSplit := strings.Cut(rest, ": ")
	severity := "info"
	if !hasSplit {
		if requireSeverity {
			return diagnosticsRow{}, false
		}
		msg = rest
	} else if requireSeverity && !knownSeverityToken(sevTok) {
		// A 3-segment line whose first word is not a known severity is more
		// likely a bare message with a colon (go vet emits "p:l:c: msg"):
		// fabricating a severity truncates the real message. Reject this
		// shape so the line falls to the vet attempt or the raw fallback.
		return diagnosticsRow{}, false
	} else if knownSeverityToken(sevTok) {
		severity = normalizeSeverityToken(sevTok)
	} else {
		msg = rest
	}
	if !requireSeverity && hasSplit && looksLikePositionMarker(sevTok) {
		// The vet shape swallowed a third segment ("p:l:c: msg" parsed as
		// "p:l:" + "c: msg"): the column would corrupt the message. Reject
		// so the line becomes a raw row.
		return diagnosticsRow{}, false
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return diagnosticsRow{}, false
	}
	line, lineOK := parsePositionNumber(lineStr)
	if !lineOK {
		// A digits-only position that does not fit int would degrade to a
		// fabricated line of 0 the producer never emitted (finding PC-1):
		// demote to a raw row, matching the JSON path's demotion rule.
		return diagnosticsRow{}, false
	}
	column := 0
	if colStr != "" {
		var colOK bool
		column, colOK = parsePositionNumber(colStr)
		if !colOK {
			return diagnosticsRow{}, false
		}
	}
	return diagnosticsRow{
		Severity: severity,
		Message:  stripControlChars(msg),
		File:     relativizeDiagnosticsPath(file, workspaceRoot),
		Line:     line,
		Column:   column,
	}, true
}

// looksLikePositionMarker reports whether s is a bare run of digits, the shape
// of a swallowed column marker in the vet path.
func looksLikePositionMarker(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// normalizeSeverity maps a severity word

// normalizeSeverity maps a severity word to the canonical error/warning/info
// set. It reports known=false for words outside the map so callers can keep
// them verbatim.
func normalizeSeverity(word string) (string, bool) {
	switch strings.ToLower(word) {
	case "error", "fatal":
		return "error", true
	case "warning", "warn":
		return "warning", true
	case "note", "info", "information", "hint":
		return "info", true
	}
	return word, false
}

// normalizeSeverityToken normalizes a severity token that may carry a trailing
// TS code, as in "error TS2322". Unknown tokens keep their leading word
// verbatim.
func normalizeSeverityToken(token string) string {
	word := token
	if i := strings.IndexByte(token, ' '); i >= 0 {
		word = token[:i]
	}
	sev, known := normalizeSeverity(word)
	if !known {
		return word
	}
	return sev
}

// knownSeverityToken reports whether the token's leading word is a known
// severity.
func knownSeverityToken(token string) bool {
	word := token
	if i := strings.IndexByte(token, ' '); i >= 0 {
		word = token[:i]
	}
	_, known := normalizeSeverity(word)
	return known
}

// parsePositionNumber parses a position number. The caller's regex guarantees
// digits; ok is false when the digits do not fit in int, which would
// otherwise fabricate a line/column of 0 the producer never emitted (finding
// PC-1). The empty string is the vet shape's absent column, so it parses to 0
// with ok true rather than being a malformed value.
func parsePositionNumber(s string) (int, bool) {
	if s == "" {
		return 0, true
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// relativizeDiagnosticsPath rewrites an absolute path under the workspace root
// to a workspace-relative path. An empty root, a relative path, or a path
// outside the root stays verbatim.
//
// The prefix comparison runs on slash-normalized forms: captured output and
// fixtures may carry POSIX-style synthetic roots ("/home/me/proj") even on
// Windows hosts, where filepath.IsAbs("/x") is false and filepath.Rel would
// refuse, silently skipping the rewrite for every row. Relative results use
// forward slashes uniformly.
func relativizeDiagnosticsPath(p, workspaceRoot string) string {
	if p == "" || workspaceRoot == "" {
		return p
	}
	slashPath := filepath.ToSlash(p)
	slashRoot := strings.TrimRight(filepath.ToSlash(workspaceRoot), "/")
	if slashPath == "" || slashRoot == "" {
		return p
	}
	// A root is usable when it is POSIX-rooted ("/x") or carries a Windows
	// volume ("C:/x"); anything else stays verbatim.
	rooted := strings.HasPrefix(slashRoot, "/") ||
		(len(slashRoot) >= 2 && slashRoot[1] == ':')
	if !rooted {
		return p
	}
	absLike := strings.HasPrefix(slashPath, "/") ||
		(len(slashPath) >= 2 && slashPath[1] == ':')
	if !absLike {
		return p
	}
	switch {
	case slashPath == slashRoot:
		return "."
	case strings.HasPrefix(slashPath, slashRoot+"/"):
		return slashPath[len(slashRoot)+1:]
	default:
		return p
	}
}

// stripControlChars removes C0 control characters (tab preserved) so hostile
// captured output cannot smuggle escape sequences into model-facing rows.
func stripControlChars(s string) string {
	hasControl := false
	for _, r := range s {
		if isControlChar(r) {
			hasControl = true
			break
		}
	}
	if !hasControl {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isControlChar(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isControlChar reports whether r is a C0 control character. Tab is allowed.
func isControlChar(r rune) bool {
	return (r < 0x20 && r != '\t') || r == 0x7f
}

// finalizeDiagnostics derives the summary from the rows. total is exactly the
// row count; the buckets partition the rows (raw rows count in the raw bucket,
// never the info bucket); files counts distinct non-empty relativized paths.
func finalizeDiagnostics(rows []diagnosticsRow) diagnosticsOutput {
	var summary diagnosticsSummary
	files := make(map[string]bool)
	for _, row := range rows {
		switch {
		case row.Raw:
			summary.Raw++
		case row.Severity == "error":
			summary.Errors++
		case row.Severity == "warning":
			summary.Warnings++
		default:
			summary.Infos++
		}
		if row.File != "" {
			files[row.File] = true
		}
	}
	summary.Files = len(files)
	summary.Total = len(rows)
	return diagnosticsOutput{Rows: rows, Summary: summary}
}
