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
	for _, want := range []string{"⬖", "mivia", "demo/mivia-fast", "60%"} {
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

// TestNarrowWidthTruncates: the bar is one row at every width without breadcrumbs.
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

func TestSplitWidthPreservesModelAndContext(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor, ports.ModelInfo{
		Name: "claude-3-7-sonnet", Provider: "anthropic", ContextWindow: 200_000,
	}, ports.Usage{InputTokens: 20_000, OutputTokens: 4_000}, 60)
	got := ansi.Strip(m.View())
	if !strings.Contains(got, "claude-3-7-sonnet") {
		t.Errorf("expected model name preserved at width 60, got: %q", got)
	}
	if !strings.Contains(got, "12%") {
		t.Errorf("expected context percentage preserved at width 60, got: %q", got)
	}
}

func TestEmptyBreadcrumbSingleRow(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor, ports.ModelInfo{Name: "mivia-fast", Provider: "demo"}, ports.Usage{}, 80)
	if m.Height() != 1 {
		t.Errorf("Height = %d, want 1", m.Height())
	}
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 1 {
		t.Errorf("got %d lines, want 1", len(lines))
	}
}

func TestSetBreadcrumbSingleSegment(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor, ports.ModelInfo{Name: "mivia-fast", Provider: "demo"}, ports.Usage{}, 80)
	m.SetBreadcrumb([]string{"Morning Check-in"})
	if m.Height() != 2 {
		t.Errorf("Height = %d, want 2", m.Height())
	}
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), m.View())
	}
	bRow := ansi.Strip(lines[1])
	if bRow != "Morning Check-in" {
		t.Errorf("breadcrumb row = %q, want %q", bRow, "Morning Check-in")
	}
}

func TestSetBreadcrumbMultipleSegments(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor, ports.ModelInfo{Name: "mivia-fast", Provider: "demo"}, ports.Usage{}, 80)
	m.SetBreadcrumb([]string{"Morning Check-in", "Agent 1", "Task: implement feature X"})
	if m.Height() != 2 {
		t.Errorf("Height = %d, want 2", m.Height())
	}
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), m.View())
	}
	bRow := ansi.Strip(lines[1])
	want := "Morning Check-in › Agent 1 › Task: implement feature X"
	if bRow != want {
		t.Errorf("breadcrumb row = %q, want %q", bRow, want)
	}
}

func TestBreadcrumbNarrowWidthTruncates(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor, ports.ModelInfo{Name: "mivia-fast", Provider: "demo"}, ports.Usage{}, 30)
	m.SetBreadcrumb([]string{"Morning Check-in", "Agent 1", "Task: implement feature X"})
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	bRow := lines[1]
	if w := ansi.StringWidth(bRow); w > 30 {
		t.Errorf("breadcrumb width %d exceeds 30: %q", w, bRow)
	}
	stripped := ansi.Strip(bRow)
	if !strings.Contains(stripped, "Task: implement feature X") {
		t.Errorf("truncated breadcrumb %q does not contain the active segment", stripped)
	}
	if !strings.HasPrefix(stripped, "…") {
		t.Errorf("truncated breadcrumb %q missing ellipsis prefix", stripped)
	}
}

func TestBreadcrumbASCIITier(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, ports.ModelInfo{Name: "mivia-fast", Provider: "demo"}, ports.Usage{}, 80)
	m.SetBreadcrumb([]string{"Morning Check-in", "Agent 1", "Task: implement feature X"})
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	bRow := ansi.Strip(lines[1])
	want := "Morning Check-in > Agent 1 > Task: implement feature X"
	if bRow != want {
		t.Errorf("ASCII breadcrumb row = %q, want %q", bRow, want)
	}

	// Narrow ASCII truncation
	m.SetWidth(25)
	lines = strings.Split(m.View(), "\n")
	bRowNarrow := ansi.Strip(lines[1])
	if !strings.HasPrefix(bRowNarrow, "...") {
		t.Errorf("ASCII truncated breadcrumb %q missing '...' prefix", bRowNarrow)
	}
	if w := ansi.StringWidth(lines[1]); w > 25 {
		t.Errorf("width %d exceeds 25: %q", w, lines[1])
	}
}

func TestActivityBadge(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor, ports.ModelInfo{Name: "mivia-fast", Provider: "demo"}, ports.Usage{}, 100)
	if m.activityBadge() != "" {
		t.Errorf("expected empty activity badge initially, got %q", m.activityBadge())
	}

	m.SetActivity(1, 0)
	if got := ansi.Strip(m.activityBadge()); got != "[ 1 file ]" {
		t.Errorf("got %q, want %q", got, "[ 1 file ]")
	}

	m.SetActivity(3, 2)
	if got := ansi.Strip(m.activityBadge()); got != "[ 3 files | ⚡ 2 agents ]" {
		t.Errorf("got %q, want %q", got, "[ 3 files | ⚡ 2 agents ]")
	}

	view := ansi.Strip(m.View())
	if !strings.Contains(view, "[ 3 files | ⚡ 2 agents ]") {
		t.Errorf("topbar View() missing activity badge: %q", view)
	}

	// Narrow width omits activity badge
	m.SetWidth(50)
	narrowView := ansi.Strip(m.View())
	if strings.Contains(narrowView, "3 files") {
		t.Errorf("narrow topbar should omit activity badge, got: %q", narrowView)
	}
}
