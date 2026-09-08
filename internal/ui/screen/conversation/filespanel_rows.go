package conversation

import (
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// This file owns the sidebar's row production: how many lines a planned
// group will occupy, which slice of the plan the pane can show, and the
// rendering of exactly that slice.
//
// The ordering is the point. Rendering first and clipping afterwards made
// every repaint cost O(all rows ever added) - and p.agents is append-only,
// so with the spinner ticking at 10 FPS during a dispatch a long session
// restyled and re-measured hundreds of invisible subagent rows per second.

// navGroupHeights reports each planned group's rendered line count
// WITHOUT rendering it. Every group is one line except an agent group,
// which is a name line plus a metrics line (panelAgentRow).
//
// It must stay in lockstep with renderNavGroups' switch:
// TestNavGroupHeightsMatchRenderedGroups pins the two together, so a
// future multi-line row kind cannot silently mis-size the window.
func navGroupHeights(plan []navGroup) []int {
	heights := make([]int, len(plan))
	for i, g := range plan {
		if g.kind == navAgent {
			heights[i] = 2
			continue
		}
		heights[i] = 1
	}
	return heights
}

func (s Screen) panelRows(inner, maxRows int) []string {
	// The SAME lists navGroups indexed. g.at indexes the filtered rows,
	// so rendering from the unfiltered ones drew a different file than
	// the row selects - and put the unfiltered count in the header
	// beside it. Unreachable today (no key path sets a filter), but it
	// is the one place that could falsify this file's whole claim.
	visible, agents := s.panel.visibleRows()

	// The cursor's PICKER index identifies the highlighted row, never the
	// rendered label. Concurrent subagents commonly share a name and a
	// status (four "reviewer" rows all "running"), so matching by label
	// would paint the marker on every one of them.
	contextRows := s.contextSectionRows(maxRows)
	plan := s.panel.navGroups(contextRows)
	selGroup := navSelGroup(plan, s.panel.list.CursorRow())

	// Window FIRST, on heights derived from the plan, then render only
	// the groups that survive. The bounds come from exactly the line
	// counts the old code obtained by rendering everything, so the
	// visible output is unchanged - only the invisible work is gone.
	//
	// The limit is clamped to at least one row. transcriptHeight returns 0
	// when the chrome is taller than the terminal, and narrowPanelRows
	// hands that straight through; panelWindowGroupBounds reads a
	// non-positive limit as "no windowing" and returns every group, which
	// would restore the full O(all rows ever added) render on precisely
	// the smallest terminals. Nothing survives the caller's overlay clip
	// at that size either way, so clamping changes cost, never output.
	limit := maxRows
	if limit < 1 {
		limit = 1
	}
	startGroup, endGroup := panelWindowGroupBounds(navGroupHeights(plan), selGroup, limit, false)

	groups := s.renderNavGroups(plan, startGroup, endGroup, selGroup, inner, maxRows, visible, agents)
	return clipRowsToWidth(flattenGroups(groups), inner)
}

// renderNavGroups renders plan[startGroup:endGroup] into row-groups.
//
// The context block is a single rendered slice consumed in plan order, so
// groups BEFORE the window still have to be stepped over to keep the
// cursor into it aligned - stepping is a slice index, not a render.
func (s Screen) renderNavGroups(plan []navGroup, startGroup, endGroup, selGroup, inner, maxRows int, visible []fileEntry, agents []subagentRow) [][]string {
	subtle := render.Role(s.Theme, s.Tier, theme.RoleFGSubtle)
	marked := s.panel.focused

	ctx := s.panelContextRows(inner, maxRows)
	ctxAt := 0
	for gi := 0; gi < startGroup; gi++ {
		switch plan[gi].kind {
		case navContextHeader, navContextBody:
			ctxAt++
		}
	}
	// Written without a second return so there is no arm that never
	// runs: the plan and the rendered block always agree on the row count
	// (TestTheContextSectionDrawsExactlyTheRowsItClaims), and the bounds
	// check is here only so a future disagreement draws a blank row
	// instead of panicking in a renderer.
	nextCtx := func() string {
		row := ""
		if ctxAt < len(ctx) {
			row = ctx[ctxAt]
		}
		ctxAt++
		return row
	}

	groups := make([][]string, 0, endGroup-startGroup)
	for gi := startGroup; gi < endGroup; gi++ {
		g := plan[gi]
		sel := marked && gi == selGroup
		switch g.kind {
		case navContextHeader:
			groups = append(groups, []string{s.panelSectionHeader(inner, nextCtx(), sel)})
		case navContextBody:
			groups = append(groups, []string{nextCtx()})
		case navModelCaption:
			groups = append(groups, []string{subtle.Render("model")})
		case navModel:
			groups = append(groups, []string{s.panelModelRow(sel)})
		case navFilesHeader:
			groups = append(groups, []string{s.panelSectionHeader(inner,
				s.sectionCaption("files changed", len(visible), g.sel, s.panel.filesCollapsed), sel)})
		case navFile:
			groups = append(groups, []string{s.panelFileRow(visible[g.at], sel)})
		case navAgentsHeader:
			groups = append(groups, []string{s.panelSectionHeader(inner,
				s.sectionCaption("subagents", len(agents), g.sel, s.panel.agentsCollapsed), sel)})
		case navAgent:
			groups = append(groups, s.panelAgentRow(agents[g.at], inner, sel))
		}
	}
	return groups
}
