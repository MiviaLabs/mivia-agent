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
