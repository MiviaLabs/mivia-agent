package render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func TestFormatInspectRepositoryOutput_GroupsByFile(t *testing.T) {
	th := loadTheme(t)
	raw := `{"version":1,"provenance":{"workspace_root":"/repo"},"results":[
		{"path":"pkg/foo.go","line":10,"text":"func Hello()","context":["// comment"]},
		{"path":"pkg/foo.go","line":25,"text":"func World()"},
		{"path":"pkg/bar.go","line":5,"text":"type Bar struct"}
	],"result_count":3,"truncated":false}`
	summary, lines := FormatInspectRepositoryOutput(th, theme.TierTrueColor, raw, 80)
	if !strings.Contains(summary, "3 matches in 2 files") {
		t.Errorf("unexpected summary: %q", summary)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "pkg/foo.go") || !strings.Contains(plain, "pkg/bar.go") {
		t.Errorf("missing file groups in:\n%s", plain)
	}
	if !strings.Contains(plain, "L10") || !strings.Contains(plain, "L25") {
		t.Errorf("missing line numbers in:\n%s", plain)
	}
	if !strings.Contains(plain, "// comment") {
		t.Errorf("missing context line in:\n%s", plain)
	}
}

func TestFormatInspectRepositoryOutput_TruncatedFooter(t *testing.T) {
	th := loadTheme(t)
	raw := `{"results":[{"path":"a.go","line":1,"text":"x"}],"result_count":1,"truncated":true,"truncation_reason":"byte_limit"}`
	_, lines := FormatInspectRepositoryOutput(th, theme.TierTrueColor, raw, 80)
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "byte_limit") {
		t.Errorf("expected truncation reason in:\n%s", plain)
	}
}

func TestFormatInspectRepositoryOutput_NoResultsFallsBack(t *testing.T) {
	th := loadTheme(t)
	raw := `{"results":[],"result_count":0,"truncated":false}`
	summary, lines := FormatInspectRepositoryOutput(th, theme.TierTrueColor, raw, 80)
	if summary != "" {
		t.Errorf("expected empty summary for zero results, got %q", summary)
	}
	// The fallback is labelled, not naked: row one names the bytes as an
	// unparsed tool result (tool-output-polish.md R1).
	if len(lines) < 1 || !strings.Contains(ansi.Strip(lines[0]), "unparsed tool result") {
		t.Errorf("expected the unparsed label, got %v", lines)
	}
}

// TestFormatInspectRepositoryOutput_CapsHotFilesAndPaths pins R5: a file
// with many matches keeps five rows and a "+N more" line; a long path
// middle-truncates instead of pushing the row out; and a truncated
// envelope states its claim, not just the survivors.
func TestFormatInspectRepositoryOutput_CapsHotFilesAndPaths(t *testing.T) {
	th := loadTheme(t)
	results := make([]map[string]any, 0, 9)
	for i := 0; i < 9; i++ {
		results = append(results, map[string]any{
			"path": "web/internal/server/feature/deeply/nested/dir/component.go",
			"line": 10 + i,
			"text": fmt.Sprintf("match %d", i),
		})
	}
	raw := mustJSON(map[string]any{
		"result_count":      20,
		"results":           results,
		"truncated":         true,
		"truncation_reason": "byte cap",
	})
	summary, lines := FormatInspectRepositoryOutput(th, theme.TierTrueColor, raw, 100)
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(summary, "truncated, showing 9") {
		t.Errorf("summary must state the claim beyond the survivors: %q", summary)
	}
	if strings.Count(plain, "match ") != 5 {
		t.Errorf("expected 5 rendered rows for a hot file:\n%s", plain)
	}
	if !strings.Contains(plain, "+4 more in this file") {
		t.Errorf("expected the per-file cap hint:\n%s", plain)
	}
	if !strings.Contains(plain, "…") || !strings.Contains(plain, "component.go") {
		t.Errorf("expected the long path middle-truncated with both ends kept:\n%s", plain)
	}
}
