package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func TestFormatCommandOutput(t *testing.T) {
	th := loadTheme(t)
	// Short output
	short := "line 1\nline 2"
	lines, coll := FormatCommandOutput(th, theme.TierTrueColor, short, true, 80)
	if len(lines) != 2 || coll {
		t.Errorf("expected 2 lines and not collapsible, got len %d, coll %v", len(lines), coll)
	}

	// Long successful output tails with omitted notice
	var longLines []string
	for i := 0; i < 20; i++ {
		longLines = append(longLines, "log line")
	}
	longOutput := strings.Join(longLines, "\n")
	res, coll := FormatCommandOutput(th, theme.TierTrueColor, longOutput, true, 80)
	if !coll {
		t.Error("long output should be collapsible")
	}
	foundOmitted := false
	for _, l := range res {
		if strings.Contains(ansi.Strip(l), "omitted") {
			foundOmitted = true
			break
		}
	}
	if !foundOmitted {
		t.Errorf("expected omitted message in long output: %v", res)
	}
}

func TestFormatGrepOutput(t *testing.T) {
	th := loadTheme(t)
	rawJSON := `{"File":"pkg/foo.go","LineNumber":10,"LineContent":"func Hello()"}
{"File":"pkg/foo.go","LineNumber":25,"LineContent":"func World()"}
{"File":"pkg/bar.go","LineNumber":5,"LineContent":"type Bar struct"}`

	summary, lines := FormatGrepOutput(th, theme.TierTrueColor, rawJSON, 80)
	if !strings.Contains(summary, "3 matches in 2 files") {
		t.Errorf("unexpected summary: %q", summary)
	}

	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "pkg/foo.go (2 matches)") {
		t.Errorf("missing foo.go group header in:\n%s", plain)
	}
	if !strings.Contains(plain, "L10") || !strings.Contains(plain, "L25") {
		t.Errorf("missing line numbers in:\n%s", plain)
	}
}

func TestFormatFileReadOutput(t *testing.T) {
	th := loadTheme(t)
	var long []string
	for i := 0; i < 15; i++ {
		long = append(long, "code line")
	}
	res, coll := FormatFileReadOutput(th, theme.TierTrueColor, strings.Join(long, "\n"), 80)
	if !coll {
		t.Error("long file read should be collapsible")
	}
	plain := ansi.Strip(strings.Join(res, "\n"))
	if !strings.Contains(plain, "1 │ code line") {
		t.Errorf("expected line number prefix in:\n%s", plain)
	}
	if !strings.Contains(plain, "more lines") {
		t.Errorf("expected more lines notice in:\n%s", plain)
	}
}

func TestFormatJSONOutputSorted(t *testing.T) {
	th := loadTheme(t)
	raw := `{"zebra": 1, "apple": 2, "mango": 3}`
	lines := FormatJSONOutput(th, theme.TierTrueColor, raw, 80)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	plain0 := ansi.Strip(lines[0])
	plain1 := ansi.Strip(lines[1])
	plain2 := ansi.Strip(lines[2])
	if !strings.HasPrefix(plain0, "apple:") || !strings.HasPrefix(plain1, "mango:") || !strings.HasPrefix(plain2, "zebra:") {
		t.Errorf("keys not sorted: %v", lines)
	}
}

func TestFormatMemoryOutput(t *testing.T) {
	th := loadTheme(t)
	raw := `[{"created":"2026-08-12","id":"6ddfa4ccc1a537fe86eaa93943f16d47","scope":"project","summary":"Plans live in the sibling mivia-agent-plans repo","tags":["workflow","feature-delivery"]}]`
	summary, lines := FormatMemoryOutput(th, theme.TierTrueColor, raw, 80)
	if summary != "1 memory item" {
		t.Errorf("unexpected summary: %q", summary)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "• [project] 6ddfa4cc (2026-08-12)") {
		t.Errorf("missing header chip in:\n%s", plain)
	}
	if !strings.Contains(plain, "Plans live in the sibling") {
		t.Errorf("missing summary in:\n%s", plain)
	}
	if !strings.Contains(plain, "tags: workflow, feature-delivery") {
		t.Errorf("missing tags in:\n%s", plain)
	}
}

func TestFormatDiagnosticsOutput(t *testing.T) {
	th := loadTheme(t)
	raw := "internal/ui/app.go:10:5: syntax error\ninternal/ui/app.go:15:2: warning: unused variable"
	lines := FormatDiagnosticsOutput(th, theme.TierTrueColor, raw, 80)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	plain0 := ansi.Strip(lines[0])
	plain1 := ansi.Strip(lines[1])
	if !strings.HasPrefix(plain0, "✖ ") {
		t.Errorf("expected error marker in line 0: %q", plain0)
	}
	if !strings.HasPrefix(plain1, "⚠ ") {
		t.Errorf("expected warning marker in line 1: %q", plain1)
	}
}

func TestFormatWorkflowOutput(t *testing.T) {
	th := loadTheme(t)
	raw := `{"workflow":"feature-delivery","status":"completed","steps":["plan","build","verify"]}`
	summary, lines := FormatWorkflowOutput(th, theme.TierTrueColor, raw, 80)
	if summary != "workflow completed" {
		t.Errorf("unexpected summary: %q", summary)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "• workflow: feature-delivery") {
		t.Errorf("missing workflow header in:\n%s", plain)
	}
	if !strings.Contains(plain, "- plan") {
		t.Errorf("missing step plan in:\n%s", plain)
	}
}

func TestFormatLedgerOutput(t *testing.T) {
	th := loadTheme(t)
	raw := `{"status":"ok","ref":"ref:output:94f588477ee962db522a4e0b01d0dedf3bd3b85b8dab125d51b07577eab7b21e","kind":"output","bytes":6650,"offset":0,"limit":8192,"content":"{\"output\":\"This review evaluates the OS-enforced sandboxing plan\"}"}`
	summary, lines := FormatLedgerOutput(th, theme.TierTrueColor, raw, 80)
	if !strings.Contains(summary, "ref:output:94f58847") || !strings.Contains(summary, "6.5 KB") {
		t.Errorf("unexpected summary: %q", summary)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "This review evaluates the OS-enforced sandboxing plan") {
		t.Errorf("missing unescaped content in:\n%s", plain)
	}
	if strings.Contains(plain, "content_is_data") || strings.Contains(plain, "returned_bytes") {
		t.Errorf("raw envelope metadata leaked into output:\n%s", plain)
	}
}

func TestFormatLedgerOutput_NotFound(t *testing.T) {
	th := loadTheme(t)
	raw := `{"status":"not_found","ref":"ref:output:deadbeefcafefeed"}`
	summary, lines := FormatLedgerOutput(th, theme.TierTrueColor, raw, 80)
	if !strings.Contains(summary, "not found") {
		t.Errorf("unexpected summary: %q", summary)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "ref:output:deadbeef") {
		t.Errorf("expected shortened ref in body:\n%s", plain)
	}
}

func TestFormatLedgerOutput_ErrorKind(t *testing.T) {
	th := loadTheme(t)
	raw := `{"status":"ok","ref":"ref:error:94f588477ee962db522a4e0b01d0dedf3bd3b85b8dab125d51b07577eab7b21e","kind":"error","bytes":42,"content":"boom: something failed"}`
	summary, lines := FormatLedgerOutput(th, theme.TierTrueColor, raw, 80)
	plainSummary := ansi.Strip(summary)
	if !strings.Contains(plainSummary, "recorded error") {
		t.Errorf("expected error-kind label in summary: %q", plainSummary)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "boom: something failed") {
		t.Errorf("missing content in:\n%s", plain)
	}
}

func TestFormatLedgerOutput_MalformedRefShape(t *testing.T) {
	th := loadTheme(t)
	raw := `{"error":"malformed reference","detail":"expected kind:sub:digest"}`
	summary, lines := FormatLedgerOutput(th, theme.TierTrueColor, raw, 80)
	plainSummary := ansi.Strip(summary)
	if !strings.Contains(plainSummary, "malformed reference") {
		t.Errorf("unexpected summary: %q", plainSummary)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "expected kind:sub:digest") {
		t.Errorf("missing detail in:\n%s", plain)
	}
}

func TestFormatLedgerOutput_MissingRefParam(t *testing.T) {
	th := loadTheme(t)
	raw := `{"error":"ref is required"}`
	summary, _ := FormatLedgerOutput(th, theme.TierTrueColor, raw, 80)
	if !strings.Contains(ansi.Strip(summary), "ref is required") {
		t.Errorf("unexpected summary: %q", summary)
	}
}

func TestFormatLedgerOutput_NestedStructuredJSON(t *testing.T) {
	th := loadTheme(t)
	raw := `{"status":"ok","ref":"ref:output:94f588477ee962db522a4e0b01d0dedf3bd3b85b8dab125d51b07577eab7b21e","kind":"output","bytes":50,"content":"{\"output\":\"{\\\"summary\\\":\\\"ok\\\",\\\"count\\\":3}\"}"}`
	_, lines := FormatLedgerOutput(th, theme.TierTrueColor, raw, 80)
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, `"summary"`) || !strings.Contains(plain, `"count"`) {
		t.Errorf("expected pretty-printed nested JSON in:\n%s", plain)
	}
}

func TestFormatLedgerOutput_TruncatedTailSalvaged(t *testing.T) {
	th := loadTheme(t)
	full := `{"status":"ok","ref":"ref:output:94f588477ee962db522a4e0b01d0dedf3bd3b85b8dab125d51b07577eab7b21e","kind":"output","bytes":6650,"offset":0,"limit":8192,"has_more":false,"content":"{\"output\":\"This review evaluates the OS-enforced sandboxing plan in detail across every subsystem\"}"}`
	// Simulate a byte-level cap slicing the envelope mid-content, as
	// capToolResult/remainder.CapWithSpoolRef would.
	cut := full[:len(full)-30]
	summary, lines := FormatLedgerOutput(th, theme.TierTrueColor, cut, 80)
	if !strings.Contains(summary, "ref:output:94f58847") {
		t.Errorf("expected salvaged ref in summary: %q", summary)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "This review evaluates the OS-enforced sandboxing plan") {
		t.Errorf("expected partial content salvaged in:\n%s", plain)
	}
	if !strings.Contains(plain, "truncated") {
		t.Errorf("expected truncation notice in:\n%s", plain)
	}
}

func TestFormatLedgerOutput_UnrecoverableGarbageStillFallsBackRaw(t *testing.T) {
	th := loadTheme(t)
	raw := "not json at all, just noise"
	summary, lines := FormatLedgerOutput(th, theme.TierTrueColor, raw, 80)
	if summary != "" {
		t.Errorf("expected empty summary for unrecoverable input, got %q", summary)
	}
	if len(lines) != 1 || lines[0] != raw {
		t.Errorf("expected raw passthrough, got %v", lines)
	}
}

func TestFormatFileReadOutputWithContext_SyntaxAndLineNumbers(t *testing.T) {
	th := loadTheme(t)
	code := `package main

import "fmt"

func main() {
	fmt.Println("test")
}`
	lines, coll := FormatFileReadOutputWithContext(th, theme.TierTrueColor, "main.go", 10, code, 80)
	if !coll {
		t.Error("7 lines should be collapsible")
	}
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines (4 + omitted), got %d", len(lines))
	}
	plain0 := ansi.Strip(lines[0])
	if !strings.Contains(plain0, " 10 │ package main") {
		t.Errorf("expected line number 10 in line 0: %q", plain0)
	}
}

func TestFormatGrepOutput_StandardRipgrep(t *testing.T) {
	th := loadTheme(t)
	raw := "internal/ui/app.go:12:func New() Model {\ninternal/ui/app.go:45:return m\ninternal/ui/render/syntax.go:8:func HighlightCode("
	summary, lines := FormatGrepOutputWithContext(th, theme.TierTrueColor, "func", raw, 80)
	if !strings.Contains(summary, "3 matches in 2 files") {
		t.Errorf("unexpected summary: %q", summary)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "internal/ui/app.go (2 matches)") {
		t.Errorf("missing group header in:\n%s", plain)
	}
	if !strings.Contains(plain, "L12") || !strings.Contains(plain, "L45") {
		t.Errorf("missing line numbers in:\n%s", plain)
	}
}

func TestFormatListDirOutput(t *testing.T) {
	th := loadTheme(t)
	raw := "src/\ninternal/\npackage.json\nREADME.md"
	summary, lines := FormatListDirOutput(th, theme.TierTrueColor, raw, 80)
	if summary != "4 entries" {
		t.Errorf("unexpected summary: %q", summary)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "📁 src/") || !strings.Contains(plain, "• package.json") {
		t.Errorf("missing directory entries in:\n%s", plain)
	}
}

func TestFormatToolOutputWithContext_Dispatch(t *testing.T) {
	th := loadTheme(t)
	args := map[string]any{"path": "package.json", "StartLine": 1}
	code := `{\n  "name": "mivia"\n}`
	_, body, _ := FormatToolOutputWithContext(th, theme.TierTrueColor, "read_file", args, code, true, 80)
	if len(body) == 0 {
		t.Errorf("expected formatted read_file body")
	}
}
