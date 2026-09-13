package transcript

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func TestUsageFooterShapeAndWallTime(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	clock := time.Unix(1700000000, 0)
	m.Now = func() time.Time { return clock }
	m.SetModel("claude-opus-5")
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "hi"}})
	clock = clock.Add(102 * time.Second)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindUsage, Body: uievent.UsageBody{InputTokens: 1284, OutputTokens: 2940, CostUSD: 0.04}})
	b := m.Blocks()[len(m.Blocks())-1]
	if b.Prose || b.Collapsible || b.Usage == nil {
		t.Fatalf("usage block shape = prose:%v collapsible:%v usage:%v", b.Prose, b.Collapsible, b.Usage != nil)
	}
	for _, width := range []int{40, 80, 120} {
		row := ansi.Strip(b.Render(m.Theme, m.Tier, width))
		if b.Height(width) != 1 || strings.Contains(row, "\n") {
			t.Errorf("width %d: height=%d row=%q", width, b.Height(width), row)
		}
		if !strings.Contains(row, "claude-opus-5") || !strings.Contains(row, "1m 42s") {
			t.Errorf("width %d: footer=%q", width, row)
		}
		if ansi.StringWidth(row) != width {
			t.Errorf("width %d: rendered width=%d", width, ansi.StringWidth(row))
		}
	}
}
