package topbar

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func loadTheme(t *testing.T) theme.Theme {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if th.Name == "mivia-dark" {
			return th
		}
	}
	t.Fatal("mivia-dark theme not found")
	return theme.Theme{}
}

func TestViewShowsBrandModelAndContext(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor, ports.ModelInfo{
		Name: "mivia-fast", Provider: "demo", ContextWindow: 125_000,
	}, ports.Usage{InputTokens: 50_000, OutputTokens: 25_000}, 80)
	got := ansi.Strip(m.View())
	for _, want := range []string{"⬖", "mivia", "demo/mivia-fast", "60% ctx"} {
		if !strings.Contains(got, want) {
			t.Errorf("top bar %q missing %q", got, want)
		}
	}
}

// TestUnknownWindowOmitsTheShare: with no stated context window there
// is no honest percentage, so the bar must omit it, not guess.
func TestUnknownWindowOmitsTheShare(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, ports.ModelInfo{Name: "x", Provider: "p"},
		ports.Usage{InputTokens: 1000}, 80)
	if got := ansi.Strip(m.View()); strings.Contains(got, "ctx") {
		t.Errorf("unknown window printed a share: %q", got)
	}
}

// TestNarrowWidthTruncates: the bar is one row at every width.
func TestNarrowWidthTruncates(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, ports.ModelInfo{
		Name: "mivia-deep", Provider: "demo", ContextWindow: 125_000,
	}, ports.Usage{InputTokens: 1, OutputTokens: 1}, 20)
	if w := ansi.StringWidth(m.View()); w > 20 {
		t.Errorf("row width %d exceeds 20: %q", w, m.View())
	}
	if m.Height() != 1 {
		t.Errorf("Height = %d, want 1", m.Height())
	}
}
