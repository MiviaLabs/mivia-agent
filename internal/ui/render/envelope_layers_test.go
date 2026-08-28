package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// TestUnwrapJSONStringPeelsThreeLayers pins the depth bound's last rung:
// a payload wrapped in three JSON string layers comes out as the inner
// string, because the loop runs the full maxEnvelopeUnwrapDepth before
// giving the value back.
func TestUnwrapJSONStringPeelsThreeLayers(t *testing.T) {
	inner := `{"status":"ok","content":"deep"}`
	one := mustJSON(inner)
	two := mustJSON(one)
	three := mustJSON(two)
	if got := UnwrapJSONString(three); got != inner {
		t.Errorf("three layers: got %q, want %q", got, inner)
	}
}

// TestFormatInspectRepositoryOutput_TruncatesWideContextLines pins the
// context-row clip: a context line wider than the terminal is cut with
// an ellipsis instead of pushing the row out.
func TestFormatInspectRepositoryOutput_TruncatesWideContextLines(t *testing.T) {
	th := loadTheme(t)
	longCtx := strings.Repeat("ctx ", 40) // 160 columns, well past width-9
	longText := strings.Repeat("hit ", 30)
	raw := mustJSON(map[string]any{
		"result_count": 1,
		"results": []map[string]any{{
			"path":    "pkg/foo.go",
			"line":    10,
			"text":    longText,
			"context": []string{longCtx},
		}},
	})
	const width = 40
	_, lines := FormatInspectRepositoryOutput(th, theme.TierTrueColor, raw, width)
	var ctxRow string
	for _, l := range lines {
		if strings.Contains(ansi.Strip(l), "ctx") {
			ctxRow = l
			break
		}
	}
	if ctxRow == "" {
		t.Fatalf("no context row in output:\n%s", strings.Join(lines, "\n"))
	}
	stripped := ansi.Strip(ctxRow)
	if !strings.Contains(stripped, "…") {
		t.Errorf("wide context line was not truncated: %q", stripped)
	}
	if w := ansi.StringWidth(stripped); w > width {
		t.Errorf("context row is %d columns wide at width %d: %q", w, width, stripped)
	}
}

// TestFormatMemoryOutput_TruncatesLongTitleAndSummary pins the memory
// card clips: a title wider than width-4 and a summary wider than
// width-6 are both cut with an ellipsis, and a multi-item payload
// reports a plural count.
func TestFormatMemoryOutput_TruncatesLongTitleAndSummary(t *testing.T) {
	th := loadTheme(t)
	items := []map[string]any{
		{
			"id":      "abcdef1234567890",
			"scope":   "project",
			"title":   strings.Repeat("a very long memory title ", 6),
			"summary": strings.Repeat("matched summary text ", 6),
			"tags":    []string{"go"},
		},
		{
			"id":      "1234567890abcdef",
			"scope":   "project",
			"title":   strings.Repeat("another long title ", 6),
			"summary": strings.Repeat("more matched text ", 6),
		},
	}
	const width = 40
	summary, lines := FormatMemoryOutput(th, theme.TierTrueColor, mustJSON(items), width)
	if summary != "2 memory items" {
		t.Errorf("summary = %q, want the plural form", summary)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if strings.Count(plain, "…") < 4 {
		t.Errorf("expected the title and summary of both cards to truncate:\n%s", plain)
	}
	for _, row := range lines {
		if w := ansi.StringWidth(ansi.Strip(row)); w > width {
			t.Errorf("row is %d columns wide at width %d: %q", w, width, ansi.Strip(row))
		}
	}
}
