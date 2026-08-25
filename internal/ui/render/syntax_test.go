package render

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func TestHighlightCode_Basic(t *testing.T) {
	th := loadTheme(t)
	code := `package main

import "fmt"

func main() {
	fmt.Println("hello world")
}`

	lines := HighlightCode(th, theme.TierTrueColor, "main.go", code)
	if len(lines) != 7 {
		t.Fatalf("expected 7 lines, got %d", len(lines))
	}

	// In TierTrueColor, there should be ANSI color escapes
	hasANSI := false
	for _, l := range lines {
		if strings.Contains(l, "\x1b[") {
			hasANSI = true
			break
		}
	}
	if !hasANSI {
		t.Errorf("expected ANSI escapes in TrueColor tier syntax highlighting")
	}

	// In TierASCII, there should be no ANSI escapes
	asciiLines := HighlightCode(th, theme.TierASCII, "main.go", code)
	for _, l := range asciiLines {
		if strings.Contains(l, "\x1b[") {
			t.Errorf("expected no ANSI escapes in TierASCII, got %q", l)
		}
	}
}

func TestHighlightCode_JSON(t *testing.T) {
	th := loadTheme(t)
	code := `{
  "type": "module",
  "dependencies": {
    "archiver": "^5.3.0"
  }
}`

	lines := HighlightCode(th, theme.TierTrueColor, "package.json", code)
	if len(lines) != 6 {
		t.Fatalf("expected 6 lines, got %d", len(lines))
	}
}

func TestHighlightCode_Empty(t *testing.T) {
	th := loadTheme(t)
	lines := HighlightCode(th, theme.TierTrueColor, "test.py", "")
	if len(lines) != 0 {
		t.Errorf("expected empty lines for empty code, got %d", len(lines))
	}
}
