package topbar

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func TestModelBounds_MatchesViewWithTabs(t *testing.T) {
	th := loadTheme(t)
	info := ports.ModelInfo{Name: "claude-3-7-sonnet", Provider: "anthropic", ContextWindow: 200_000}
	usage := ports.Usage{InputTokens: 100_000, OutputTokens: 10_000}

	tabs := []SessionTab{
		{ID: "s1", Title: "work-1", Index: 1, IsCurrent: true},
		{ID: "s2", Title: "work-2", Index: 2},
		{ID: "s3", Title: "work-3", Index: 3},
	}

	for _, w := range []int{61, 75, 90, 110} {
		m := New(th, theme.TierTrueColor, info, usage, w)
		m.SetActivity(3, 1)
		m.SetTabs(tabs)

		view := m.View()
		startCol, endCol, ok := m.ModelBounds()
		if !ok {
			t.Fatalf("width %d: expected ModelBounds ok=true", w)
		}

		capsule := m.modelCapsule(m.planLayout().withProvider)
		capsuleW := ansi.StringWidth(capsule)
		if endCol-startCol != capsuleW {
			t.Errorf("width %d: model bounds width %d != capsule width %d", w, endCol-startCol, capsuleW)
		}

		viewStart := strings.Index(view, capsule)
		if viewStart >= 0 {
			viewCol := ansi.StringWidth(view[:viewStart])
			if viewCol != startCol {
				t.Errorf("width %d: visual startCol %d != ModelBounds startCol %d (view: %q)", w, viewCol, startCol, ansi.Strip(view))
			}
		}
	}
}

func TestActivityBounds_FalseWhenDroppedByTabs(t *testing.T) {
	th := loadTheme(t)
	info := ports.ModelInfo{Name: "claude-3-7-sonnet", Provider: "anthropic", ContextWindow: 200_000}
	usage := ports.Usage{InputTokens: 100_000, OutputTokens: 10_000}

	tabs := []SessionTab{
		{ID: "s1", Title: "feature-delivery-long-tab", Index: 1, IsCurrent: true},
		{ID: "s2", Title: "bug-fix-review-workflow", Index: 2},
	}

	m := New(th, theme.TierTrueColor, info, usage, 90)
	m.SetActivity(3, 1)
	m.SetTabs(tabs)

	plan := m.planLayout()
	startCol, endCol, ok := m.ActivityBounds()
	if !plan.withActivity && ok {
		t.Errorf("expected ActivityBounds ok=false when dropped by tabs, got ok=true [%d, %d)", startCol, endCol)
	}
	if plan.withActivity && !ok {
		t.Errorf("expected ActivityBounds ok=true when present in plan")
	}
}

func TestRenderTab_StripsAnsiEscapeSequences(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor, ports.ModelInfo{}, ports.Usage{}, 80)

	tab := SessionTab{
		ID:        "s1",
		Title:     "Clean\x1b[2JTitle\x1b[31mRed\x1b[0m",
		Index:     1,
		IsCurrent: true,
	}

	rendered := m.renderTab(tab, 30)
	if strings.Contains(rendered, "\x1b[2J") {
		t.Errorf("rendered tab contains raw escape sequence \\x1b[2J: %q", rendered)
	}
	stripped := ansi.Strip(rendered)
	if !strings.Contains(stripped, "CleanTitleRed") {
		t.Errorf("expected title 'CleanTitleRed' in stripped output, got: %q", stripped)
	}
}

func TestRenderTab_AsciiTierDistinctGlyphs(t *testing.T) {
	th := loadTheme(t)
	mAscii := New(th, theme.TierASCII, ports.ModelInfo{}, ports.Usage{}, 80)

	tabRunning := SessionTab{ID: "s1", Title: "run", Index: 1, Running: true}
	tabCurrent := SessionTab{ID: "s2", Title: "curr", Index: 2, IsCurrent: true}
	tabAction := SessionTab{ID: "s3", Title: "act", Index: 3, NeedsAction: true}
	tabIdle := SessionTab{ID: "s4", Title: "idle", Index: 4}

	rRunning := ansi.Strip(mAscii.renderTab(tabRunning, 20))
	rCurrent := ansi.Strip(mAscii.renderTab(tabCurrent, 20))
	rAction := ansi.Strip(mAscii.renderTab(tabAction, 20))
	rIdle := ansi.Strip(mAscii.renderTab(tabIdle, 20))

	if !strings.Contains(rRunning, "~") {
		t.Errorf("running ASCII tab must contain '~', got %q", rRunning)
	}
	if !strings.Contains(rCurrent, ">") {
		t.Errorf("current ASCII tab must contain '>', got %q", rCurrent)
	}
	if !strings.Contains(rAction, "!") {
		t.Errorf("action ASCII tab must contain '!', got %q", rAction)
	}
	if !strings.Contains(rIdle, "-") {
		t.Errorf("idle ASCII tab must contain '-', got %q", rIdle)
	}
}

func TestComputeTabWindow_DoesNotExceedAvailWidthWithOverhead(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor, ports.ModelInfo{}, ports.Usage{}, 80)
	m.SetTabs([]SessionTab{
		{ID: "s1", Title: "tab-one", Index: 1},
		{ID: "s2", Title: "tab-two", Index: 2, IsCurrent: true},
		{ID: "s3", Title: "tab-three", Index: 3},
	})

	// Width 15 is too narrow to display tab 2 along with both left and right overflow markers
	start, end, rendered, _, _ := m.computeTabWindow(15, 0)
	if len(rendered) > 0 {
		totalW := 0
		for i, str := range rendered {
			totalW += ansi.StringWidth(str)
			if i > 0 {
				totalW++
			}
		}
		if start > 0 {
			totalW += ansi.StringWidth(m.overflowLeftMarker())
		}
		if end < len(m.tabs) {
			totalW += ansi.StringWidth(m.overflowRightMarker(len(m.tabs) - end))
		}
		if totalW > 15 {
			t.Errorf("rendered tab strip width %d exceeds available width 15", totalW)
		}
	}
}
