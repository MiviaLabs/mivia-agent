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

func TestTabsRenderAndHitTest(t *testing.T) {
	th := loadTheme(t)
	info := ports.ModelInfo{Name: "claude-3-7-sonnet", Provider: "anthropic", ContextWindow: 200_000}
	m := New(th, theme.TierTrueColor, info, ports.Usage{}, 120)

	tabs := []SessionTab{
		{ID: "sess-1", Title: "auth-refactor", Index: 1, IsCurrent: true},
		{ID: "sess-2", Title: "perf-bench", Index: 2, Running: true},
		{ID: "sess-3", Title: "e2e-tests", Index: 3, NeedsAction: true},
	}
	m.SetTabs(tabs)

	view := ansi.Strip(m.View())
	if !strings.Contains(view, "1:auth-refactor") {
		t.Errorf("view missing current tab title: %q", view)
	}
	if !strings.Contains(view, "2:perf-bench") {
		t.Errorf("view missing running tab title: %q", view)
	}
	if !strings.Contains(view, "3:e2e-tests") {
		t.Errorf("view missing needs-action tab title: %q", view)
	}

	// Hit testing
	s1, e1, ok1 := m.TabBounds(0)
	if !ok1 || s1 >= e1 {
		t.Fatalf("expected valid bounds for tab 0, got [%d, %d)", s1, e1)
	}
	tab, hit := m.HitTab((s1 + e1) / 2)
	if !hit || tab.ID != "sess-1" {
		t.Errorf("expected hit on sess-1, got tab=%+v hit=%v", tab, hit)
	}

	s2, e2, ok2 := m.TabBounds(1)
	if !ok2 || s2 >= e2 {
		t.Fatalf("expected valid bounds for tab 1, got [%d, %d)", s2, e2)
	}
	tab2, hit2 := m.HitTab((s2 + e2) / 2)
	if !hit2 || tab2.ID != "sess-2" {
		t.Errorf("expected hit on sess-2, got tab=%+v hit=%v", tab2, hit2)
	}

	// ModelBounds still valid
	mStart, mEnd, mOk := m.ModelBounds()
	if !mOk || mStart <= 0 || mEnd <= mStart {
		t.Fatalf("expected valid ModelBounds with tabs present: [%d, %d)", mStart, mEnd)
	}
	if !m.HitsModel((mStart + mEnd) / 2) {
		t.Errorf("expected HitsModel to succeed with tabs present")
	}
}

func TestTabsSlidingWindow(t *testing.T) {
	th := loadTheme(t)
	info := ports.ModelInfo{Name: "claude", Provider: "anthropic", ContextWindow: 200_000}
	m := New(th, theme.TierTrueColor, info, ports.Usage{}, 70) // narrow terminal

	tabs := []SessionTab{
		{ID: "s1", Title: "session-one", Index: 1},
		{ID: "s2", Title: "session-two", Index: 2},
		{ID: "s3", Title: "session-three", Index: 3},
		{ID: "s4", Title: "session-four", Index: 4, IsCurrent: true},
		{ID: "s5", Title: "session-five", Index: 5},
		{ID: "s6", Title: "session-six", Index: 6},
	}
	m.SetTabs(tabs)

	view := ansi.Strip(m.View())
	// Active session MUST be visible
	if !strings.Contains(view, "4:session-four") {
		t.Errorf("sliding window must contain active tab 4:session-four, got view: %q", view)
	}
	// Verify overflow marker rendered
	if !strings.Contains(view, "◂") && !strings.Contains(view, "▸") {
		t.Errorf("expected overflow indicators for clipped tabs in narrow view: %q", view)
	}
}

func TestTabsEdgeCases(t *testing.T) {
	th := loadTheme(t)
	info := ports.ModelInfo{Name: "claude", Provider: "anthropic", ContextWindow: 200_000}

	// Empty tabs
	mEmpty := New(th, theme.TierTrueColor, info, ports.Usage{}, 80)
	if _, hit := mEmpty.HitTab(10); hit {
		t.Error("expected HitTab false when tabs are empty")
	}

	// ASCII tier rendering
	mAscii := New(th, theme.TierASCII, info, ports.Usage{}, 120)
	tabs := []SessionTab{
		{ID: "s1", Title: "", Index: 1}, // empty title -> fallback to ID
		{ID: "s2", Title: "very-long-session-title-that-needs-truncation", Index: 2, Running: true},
		{ID: "s3", Title: "sess-3", Index: 3, IsCurrent: true},
		{ID: "s4", Title: "sess-4", Index: 4},
		{ID: "s5", Title: "sess-5", Index: 5},
	}
	mAscii.SetTabs(tabs)

	if got := len(mAscii.Tabs()); got != 5 {
		t.Fatalf("expected 5 tabs, got %d", got)
	}

	viewAscii := ansi.Strip(mAscii.View())
	// Empty title fallback to ID
	if !strings.Contains(viewAscii, "1:s1") {
		t.Errorf("expected 1:s1 in ASCII view: %q", viewAscii)
	}
	// Long title truncation
	if !strings.Contains(viewAscii, "...") {
		t.Errorf("expected truncation ellipsis in ASCII view: %q", viewAscii)
	}
	// ASCII indicators
	if !strings.Contains(viewAscii, ">") && !strings.Contains(viewAscii, "<") {
		t.Errorf("expected ASCII overflow markers in view: %q", viewAscii)
	}

	// Bounds checks
	if _, _, ok := mAscii.TabBounds(-1); ok {
		t.Error("expected TabBounds(-1) = false")
	}
	if _, _, ok := mAscii.TabBounds(100); ok {
		t.Error("expected TabBounds(100) = false")
	}
	if _, hit := mAscii.HitTab(-5); hit {
		t.Error("expected HitTab(-5) = false")
	}
	if _, hit := mAscii.HitTab(500); hit {
		t.Error("expected HitTab(500) = false")
	}

	// SetTabs(nil) resets
	mAscii.SetTabs(nil)
	if mAscii.Tabs() != nil {
		t.Error("expected nil tabs after SetTabs(nil)")
	}
	if marker := mAscii.overflowRightMarker(0); marker != "" {
		t.Errorf("expected empty string for overflowRightMarker(0), got %q", marker)
	}
}

func TestTabsOverflowAndBounds(t *testing.T) {
	th := loadTheme(t)
	info := ports.ModelInfo{Name: "claude", Provider: "anthropic", ContextWindow: 200_000}

	// ASCII tier rendering with sliding window and left/right overflow
	mAsciiNarrow := New(th, theme.TierASCII, info, ports.Usage{}, 88)
	mAsciiNarrow.SetActivity(2, 1)
	tabsExpanded := []SessionTab{
		{ID: "s1", Title: "t1", Index: 1},
		{ID: "s2", Title: "t2", Index: 2},
		{ID: "s3", Title: "t3", Index: 3},
		{ID: "s4", Title: "t4", Index: 4, IsCurrent: true},
		{ID: "s5", Title: "t5", Index: 5},
		{ID: "s6", Title: "t6", Index: 6},
		{ID: "s7", Title: "t7", Index: 7},
		{ID: "s8", Title: "t8", Index: 8},
	}
	mAsciiNarrow.SetTabs(tabsExpanded)
	viewNarrow := ansi.Strip(mAsciiNarrow.View())
	// Should render both < and > in ASCII tier
	if !strings.Contains(viewNarrow, "<") || !strings.Contains(viewNarrow, ">") {
		t.Errorf("expected < and > in viewNarrow: %q", viewNarrow)
	}

	// Tab outside window returns false in TabBounds
	if _, _, ok := mAsciiNarrow.TabBounds(0); ok {
		t.Error("expected tab 0 to be clipped outside sliding window")
	}
	// HitTab with activity and width >= 90
	mWideAct := New(th, theme.TierTrueColor, info, ports.Usage{}, 95)
	mWideAct.SetActivity(2, 1)
	mWideAct.SetTabs(tabsExpanded)
	_, _ = mWideAct.HitTab(5)

	// Session hidden with tabs
	mHidden := New(th, theme.TierTrueColor, info, ports.Usage{}, 100)
	mHidden.SetTabs(tabsExpanded)
	mHidden.SetSessionHidden(true)
	if v := mHidden.View(); strings.Contains(v, "anthropic") {
		t.Errorf("expected model info hidden: %q", v)
	}
	_, _ = mHidden.HitTab(5)

	// Tiny width
	mTiny := New(th, theme.TierTrueColor, info, ports.Usage{}, 5)
	mTiny.SetTabs(tabsExpanded)
	_ = mTiny.View()
	_, _ = mTiny.HitTab(5)
}

func TestTabsDegradation(t *testing.T) {
	th := loadTheme(t)
	info := ports.ModelInfo{Name: "claude-3-7-sonnet", Provider: "anthropic", ContextWindow: 200_000}
	usage := ports.Usage{InputTokens: 100_000, OutputTokens: 10_000}

	tabs := []SessionTab{
		{ID: "s1", Title: "work-1", Index: 1, IsCurrent: true},
		{ID: "s2", Title: "work-2", Index: 2},
	}

	// Test descending widths to exercise degradation steps
	for width := 110; width >= 30; width-- {
		m := New(th, theme.TierTrueColor, info, usage, width)
		m.SetActivity(3, 1)
		m.SetTabs(tabs)
		v := m.View()
		w := ansi.StringWidth(v)
		if w > width {
			t.Errorf("width %d exceeded: actual %d\nview: %q", width, w, v)
		}
	}

	// Exercise dropping withBar when long model name pushes width over budget
	longInfo := ports.ModelInfo{
		Name:          "very-long-custom-fine-tuned-model-name-that-takes-up-lots-of-space",
		Provider:      "custom-provider",
		ContextWindow: 200_000,
	}
	mLong := New(th, theme.TierTrueColor, longInfo, usage, 85)
	mLong.SetActivity(3, 1)
	mLong.SetTabs(tabs)
	_ = mLong.View()
}

func TestTabsHitTabSynchronizedWithView(t *testing.T) {
	th := loadTheme(t)
	info := ports.ModelInfo{Name: "claude-3-7-sonnet", Provider: "anthropic", ContextWindow: 200_000}
	usage := ports.Usage{InputTokens: 110_000, OutputTokens: 0}

	tabs := []SessionTab{
		{ID: "sess-1", Title: "tab1", Index: 1, IsCurrent: true},
		{ID: "sess-2", Title: "tab2", Index: 2},
	}

	for _, w := range []int{57, 90, 100, 120} {
		m := New(th, theme.TierTrueColor, info, usage, w)
		m.SetActivity(2, 1)
		m.SetTabs(tabs)

		view := ansi.Strip(m.View())
		s0, e0, ok := m.TabBounds(0)
		if ok {
			mid := (s0 + e0) / 2
			tab, hit := m.HitTab(mid)
			if !hit || tab.ID != "sess-1" {
				t.Errorf("width %d: expected HitTab at %d to hit sess-1, got %+v (view: %q)", w, mid, tab, view)
			}
		}
	}
}
