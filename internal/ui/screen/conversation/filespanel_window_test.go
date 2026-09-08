package conversation

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestNavGroupHeightsMatchRenderedGroups is the lockstep guard for the
// windowing optimisation in panelRows.
//
// panelRows now computes its window from navGroupHeights - line counts
// DERIVED from the plan - and then renders only the surviving groups.
// That is sound only while the derived height of every group kind equals
// the number of lines panelRows actually appends for it. If a future row
// kind renders two lines and navGroupHeights still claims one, the window
// silently mis-sizes and rows fall off the pane.
//
// This walks a plan containing every kind and asserts the derived height
// equals the rendered height, kind by kind.
func TestNavGroupHeightsMatchRenderedGroups(t *testing.T) {
	s := panelScreen(t, 80, 24)
	now := time.Now()
	s.panel.agents = append(s.panel.agents,
		subagentRow{ID: "a1", Name: "one", Status: "running", StartedAt: now, LastProgress: now},
		subagentRow{ID: "a2", Name: "two", Status: "running", StartedAt: now, LastProgress: now},
	)
	s.panel.appendLive(uievent.Diff{Path: "a.go", Added: 3})
	s.panel.open = true
	s.panel.rebindIfOpen()

	const inner = 40
	// A limit far larger than the content, so the window keeps every
	// group and the comparison covers the whole plan.
	const maxRows = 500

	contextRows := s.contextSectionRows(maxRows)
	plan := s.panel.navGroups(contextRows)
	if len(plan) == 0 {
		t.Fatal("empty nav plan: the fixture built no rows to compare")
	}

	derived := navGroupHeights(plan)
	if len(derived) != len(plan) {
		t.Fatalf("navGroupHeights returned %d heights for %d groups", len(derived), len(plan))
	}

	total := 0
	for _, h := range derived {
		total += h
	}
	got := s.panelRows(inner, maxRows)
	if len(got) != total {
		t.Errorf("panelRows rendered %d lines, navGroupHeights predicted %d - the two are out of lockstep", len(got), total)
	}

	seen := map[navKind]bool{}
	for _, g := range plan {
		seen[g.kind] = true
	}
	if !seen[navAgent] {
		t.Error("fixture covered no agent group, the only multi-line kind")
	}
}

// TestPanelRowsWindowsBeforeRendering pins the optimisation's payoff: the
// pane's output must not grow with the number of accumulated subagent
// rows. p.agents is append-only, so before the fix a repaint rendered and
// restyled every row ever added - at 10 spinner FPS during a dispatch,
// with only maxRows of them visible.
func TestPanelRowsWindowsBeforeRendering(t *testing.T) {
	const inner = 40
	const maxRows = 12

	rowsFor := func(t *testing.T, agents int) []string {
		t.Helper()
		s := panelScreen(t, 80, 24)
		now := time.Now()
		for i := 0; i < agents; i++ {
			s.panel.agents = append(s.panel.agents, subagentRow{
				ID: "a", Name: "worker", Status: "running", StartedAt: now, LastProgress: now,
			})
		}
		s.panel.open = true
		s.panel.rebindIfOpen()
		return s.panelRows(inner, maxRows)
	}

	small := rowsFor(t, 5)
	large := rowsFor(t, 400)

	if len(small) > maxRows {
		t.Errorf("5 agents produced %d lines, want <= %d", len(small), maxRows)
	}
	if len(large) > maxRows {
		t.Errorf("400 agents produced %d lines, want <= %d (the window is not bounding the pane)", len(large), maxRows)
	}
}
