package conversation

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestPanelRowsWindowsOnADegenerateHeight pins that the sidebar's render
// work stays bounded even when the pane has collapsed to nothing.
//
// transcriptHeight() returns 0 whenever the chrome is taller than the
// terminal ("if th < 0 { return 0 }"), and narrowPanelRows passes that
// straight into panelRows as maxRows. panelWindowGroupBounds treats a
// non-positive limit as "no windowing" ("if limit <= 0 || len(groupLens)
// == 0 { return 0, len(groupLens) }"), so every accumulated file and
// subagent group is fully styled and measured, then thrown away by the
// overlay clip.
//
// That is exactly the O(all rows ever added) per-repaint cost the
// windowing change removed, returning on the smallest terminals - and at
// 10 spinner FPS during a dispatch, on the machine least able to afford
// it.
func TestPanelRowsWindowsOnADegenerateHeight(t *testing.T) {
	s := panelScreen(t, 80, 24)
	now := time.Now()
	for i := 0; i < 400; i++ {
		s.panel.agents = append(s.panel.agents, subagentRow{
			ID: "a", Name: "worker", Status: "running", StartedAt: now, LastProgress: now,
		})
	}
	s.panel.appendLive(uievent.Diff{Path: "a.go", Added: 3})
	s.panel.open = true
	s.panel.rebindIfOpen()

	// The degenerate pane transcriptHeight() hands to panelRows when the
	// chrome does not fit.
	got := s.panelRows(40, 0)

	// A pane of zero rows can draw nothing useful; whatever is produced
	// must not scale with the 400 accumulated agents.
	if len(got) > 8 {
		t.Errorf("panelRows produced %d lines for a zero-height pane; render work still scales with accumulated rows", len(got))
	}
}
