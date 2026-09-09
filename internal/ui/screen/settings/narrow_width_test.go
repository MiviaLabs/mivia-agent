package settings

import "testing"

// TestAgentsSection_RenderListLines_NarrowWidthFallsBack pins the
// headerWidth<=0 fallback directly: a width too narrow to compute a
// positive header column must fall back to a fixed 36 rather than
// producing a degenerate (zero or negative width) render.
func TestAgentsSection_RenderListLines_NarrowWidthFallsBack(t *testing.T) {
	s := &agentsSection{width: 5, rows: []agentsRow{{isHeader: true, header: "Group"}}}
	lines := s.renderListLines(0)
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("renderListLines = %v, want one non-empty header line", lines)
	}
}

// TestMCPSection_RenderListLines_NarrowWidthFallsBack mirrors the agents
// case for mcpSection's own copy of the same guard.
func TestMCPSection_RenderListLines_NarrowWidthFallsBack(t *testing.T) {
	s := &mcpSection{width: 5, rows: []mcpRow{{isHeader: true, header: "Group"}}}
	lines := s.renderListLines(0)
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("renderListLines = %v, want one non-empty header line", lines)
	}
}

// TestSkillsSection_RenderListLines_NarrowWidthFallsBack mirrors the
// agents case for skillsSection's own copy of the same guard.
func TestSkillsSection_RenderListLines_NarrowWidthFallsBack(t *testing.T) {
	s := &skillsSection{width: 5, rows: []skillsRow{{isHeader: true, header: "Group"}}}
	lines := s.renderListLines(0)
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("renderListLines = %v, want one non-empty header line", lines)
	}
}

// TestModelsSection_RenderGroupHeader_NarrowWidthFallsBack mirrors the
// same headerWidth<=0 fallback for modelsSection's own copy.
func TestModelsSection_RenderGroupHeader_NarrowWidthFallsBack(t *testing.T) {
	s := &modelsSection{width: 3}
	if got := s.renderGroupHeader("Global"); got == "" {
		t.Fatal("renderGroupHeader returned empty for a narrow width")
	}
}

// TestRouteToOwningSection_AutomationsMsgWithNoSectionOwnerFallsThrough
// pins two branches at once: the automationsSavedMsg/automationsFailedMsg/
// automationsRunMsg/automationsWatchEndedMsg case itself, and the final
// not-found fallback - Screen's own constructor (New) never builds an
// *automationsSection, so a message this case owns by type still finds
// no matching section instance and must report routed=false rather than
// panicking or silently dropping the message somewhere else.
func TestRouteToOwningSection_AutomationsMsgWithNoSectionOwnerFallsThrough(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	_, _, routed := s.routeToOwningSection(automationsSavedMsg{})
	if routed {
		t.Fatal("routeToOwningSection reported routed=true with no *automationsSection in s.sections")
	}
}
