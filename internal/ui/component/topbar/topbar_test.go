package topbar

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
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
	if m.Height() != 1 {
		t.Errorf("Height = %d, want 1", m.Height())
	}
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1:\n%s", len(lines), m.View())
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Morning Check-in") {
		t.Errorf("view = %q, want it to contain %q", view, "Morning Check-in")
	}
}

func TestSetBreadcrumbMultipleSegments(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor, ports.ModelInfo{Name: "mivia-fast", Provider: "demo"}, ports.Usage{}, 100)
	m.SetBreadcrumb([]string{"Morning Check-in", "Agent 1", "Task: implement feature X"})
	if m.Height() != 1 {
		t.Errorf("Height = %d, want 1", m.Height())
	}
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1:\n%s", len(lines), m.View())
	}
	view := ansi.Strip(m.View())
	want := "Morning Check-in › Agent 1 › Task: implement feature X"
	if !strings.Contains(view, want) {
		t.Errorf("view = %q, want it to contain %q", view, want)
	}
}

func TestBreadcrumbASCIITier(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, ports.ModelInfo{Name: "mivia-fast", Provider: "demo"}, ports.Usage{}, 100)
	m.SetBreadcrumb([]string{"Morning Check-in", "Agent 1", "Task: implement feature X"})
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	view := ansi.Strip(m.View())
	want := "Morning Check-in > Agent 1 > Task: implement feature X"
	if !strings.Contains(view, want) {
		t.Errorf("ASCII view = %q, want it to contain %q", view, want)
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

func TestHitsModel(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor, ports.ModelInfo{
		Name: "claude-3-7-sonnet", Provider: "anthropic", ContextWindow: 200_000,
	}, ports.Usage{InputTokens: 20_000, OutputTokens: 4_000}, 100)

	start, end, ok := m.ModelBounds()
	if !ok {
		t.Fatal("expected ok = true for ModelBounds")
	}
	if start <= 0 || end <= start {
		t.Fatalf("unexpected bounds [%d, %d)", start, end)
	}

	// Inside capsule
	if !m.HitsModel((start + end) / 2) {
		t.Errorf("expected HitsModel to return true for col %d", (start+end)/2)
	}
	// Before capsule
	if m.HitsModel(start - 1) {
		t.Errorf("expected HitsModel to return false before capsule")
	}
	// After capsule (in context badge)
	if m.HitsModel(end + 1) {
		t.Errorf("expected HitsModel to return false after capsule")
	}
}

// TestContextBadgeColorStops verifies the context badge follows the
// shared ContextRole color stops: subtle < 50, info 50-69, warning
// 70-89, danger >= 90. Each subtest renders the badge through View()
// and checks the raw ANSI output carries the expected role's styled
// pct string. Width 60 keeps the badge in its bar-less form so the
// styled pct string appears verbatim in the output.
func TestContextBadgeColorStops(t *testing.T) {
	th := loadTheme(t)
	info := ports.ModelInfo{Name: "x", Provider: "p", ContextWindow: 1000}

	tests := []struct {
		name string
		pct  int
		want theme.Role
	}{
		{"subtle-below-50", 49, theme.RoleFGSubtle},
		{"info-at-50", 50, theme.RoleInfo},
		{"info-below-70", 69, theme.RoleInfo},
		{"warning-at-70", 70, theme.RoleWarning},
		{"warning-below-90", 89, theme.RoleWarning},
		{"danger-at-90", 90, theme.RoleDanger},
		{"danger-overfull", 999, theme.RoleDanger},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := New(th, theme.TierTrueColor, info, ports.Usage{}, 60)
			m.SetSession(info, ports.Usage{InputTokens: int64(tc.pct * 10), OutputTokens: 0})

			styled := render.Role(th, theme.TierTrueColor, tc.want).Render(fmt.Sprintf("%d%%", tc.pct))
			if view := m.View(); !strings.Contains(view, styled) {
				t.Errorf("pct=%d: expected badge in role %q, view missing styled %q:\n%s",
					tc.pct, tc.want, styled, view)
			}
		})
	}
}

func TestHitsActivity(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor, ports.ModelInfo{
		Name: "claude-3-7-sonnet", Provider: "anthropic", ContextWindow: 200_000,
	}, ports.Usage{InputTokens: 20_000, OutputTokens: 4_000}, 100)

	// No activity badge
	if m.HitsActivity(10) {
		t.Error("expected HitsActivity to return false when no files or agents")
	}

	m.SetActivity(2, 1)
	start, end, ok := m.ActivityBounds()
	if !ok {
		t.Fatal("expected ok = true for ActivityBounds with counts set")
	}
	if start <= 0 || end <= start {
		t.Fatalf("unexpected activity bounds [%d, %d)", start, end)
	}

	// Inside activity badge
	if !m.HitsActivity((start + end) / 2) {
		t.Errorf("expected HitsActivity true inside badge at %d", (start+end)/2)
	}
	// Before badge
	if m.HitsActivity(start - 1) {
		t.Error("expected HitsActivity false before badge")
	}
	// After badge
	if m.HitsActivity(end + 1) {
		t.Error("expected HitsActivity false after badge")
	}
}
