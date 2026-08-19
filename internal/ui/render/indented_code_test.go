package render

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func TestTextStylesIndentedCodeBlock(t *testing.T) {
	th := loadTheme(t)
	got := Text(th, theme.TierTrueColor, "before\n\n    func f() {}\n    return 1\n\nafter")
	lines := strings.Split(got, "\n")
	if len(lines) != 6 {
		t.Fatalf("got %d lines, want 6: %q", len(lines), got)
	}
	for _, i := range []int{0, 1, 4, 5} {
		if strings.Contains(lines[i], "\x1b[") {
			t.Errorf("line %d should be unstyled: %q", i, lines[i])
		}
	}
	for _, i := range []int{2, 3} {
		if !strings.Contains(lines[i], "\x1b[") {
			t.Errorf("indented code line %d should be styled: %q", i, lines[i])
		}
	}
}

func TestTextTabIndentIsCode(t *testing.T) {
	th := loadTheme(t)
	got := Text(th, theme.TierTrueColor, "\tcode line")
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("expected a tab-indented line to be styled, got %q", got)
	}
}

func TestIsIndentedCode(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"    four spaces", true},
		{"   three spaces", false},
		{"\tone tab", true},
		{"", false},
		{"    ", false}, // whitespace-only: nothing to render as code
		{"not indented", false},
	}
	for _, c := range cases {
		if got := isIndentedCode(c.line); got != c.want {
			t.Errorf("isIndentedCode(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}
