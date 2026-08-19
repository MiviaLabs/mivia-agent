package render

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func TestTextPassesThroughPlainLinesAtNoColour(t *testing.T) {
	th := loadTheme(t)
	got := Text(th, theme.TierASCII, "hello\nworld")
	if got != "hello\nworld" {
		t.Errorf("got %q, want verbatim passthrough at the no-colour tier", got)
	}
}

func TestTextStylesFencedCodeBlock(t *testing.T) {
	th := loadTheme(t)
	got := Text(th, theme.TierTrueColor, "before\n```\ncode line\n```\nafter")
	lines := strings.Split(got, "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want 5: %q", len(lines), got)
	}
	if strings.Contains(lines[0], "\x1b[") || strings.Contains(lines[4], "\x1b[") {
		t.Errorf("non-fence lines should be unstyled: %q", got)
	}
	for _, i := range []int{1, 2, 3} {
		if !strings.Contains(lines[i], "\x1b[") {
			t.Errorf("fence line %d should be styled: %q", i, lines[i])
		}
	}
}
