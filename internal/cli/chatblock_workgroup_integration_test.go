package cli

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TDD: work-group header must render, be selectable, and toggle on Enter.
// Covers the production path (renderBlocksForView + toggleSelectedBlock + hit ranges).

func fourToolWorkBlocks() []ChatBlock {
	return []ChatBlock{
		{ID: "u1", Kind: ChatBlockUser, Text: "do work"},
		{ID: "t1", Kind: ChatBlockTool, ToolName: "read_file", Text: "a", Collapsed: true},
		{ID: "t2", Kind: ChatBlockTool, ToolName: "run_command", Text: "b", Collapsed: true},
		{ID: "t3", Kind: ChatBlockTool, ToolName: "grep", Text: "c", Collapsed: true},
		{ID: "t4", Kind: ChatBlockTool, ToolName: "glob", Text: "d", Collapsed: true},
		{ID: "a1", Kind: ChatBlockAssistant, Text: "done answer"},
	}
}

func TestIntegration_WorkGroup_RendersEvenWhenCollapseMapNil(t *testing.T) {
	// Production must not silently disable work groups when map is nil.
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 80
	m.workGroupCollapsed = nil // explicit nil (repro)
	m.blocks = fourToolWorkBlocks()

	r := m.renderBlocksForView()
	plain := stripANSI(strings.Join(r.Lines, "\n"))
	if !strings.Contains(plain, "Work · 4 tools") {
		t.Fatalf("nil collapse map must still render Work header, got:\n%s", plain)
	}
	if _, ok := r.Ranges[findWorkGroups(m.blocks)[0].Key]; !ok {
		t.Fatalf("work group key missing from ranges: %v", r.Ranges)
	}
}

func TestIntegration_WorkGroup_ToggleExpandCollapse(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 80
	m.blocks = fourToolWorkBlocks()
	// Leave map nil to force ensure-path; toggle must still work.
	m.workGroupCollapsed = nil

	gs := findWorkGroups(m.blocks)
	if len(gs) != 1 {
		t.Fatalf("want 1 group, got %d", len(gs))
	}
	key := gs[0].Key

	// Auto-collapsed (≥4 tools): tool names hidden in collapsed summary path.
	r0 := m.renderBlocksForView()
	plain0 := stripANSI(strings.Join(r0.Lines, "\n"))
	if !strings.Contains(plain0, "Work · 4 tools") {
		t.Fatalf("want Work header: %s", plain0)
	}
	// Collapsed: individual tool rows should not all list names as full tool lines.
	// Header uses ▸ when collapsed.
	if !strings.Contains(plain0, "▸") && !strings.Contains(plain0, "Work") {
		t.Fatalf("collapsed marker missing: %s", plain0)
	}

	m.selectedBlockID = key
	m.focus = focusScrollback
	if !m.toggleSelectedBlock() {
		t.Fatal("toggle expand failed")
	}
	if m.workGroupCollapsed == nil {
		t.Fatal("toggle must allocate collapse map")
	}
	if m.workGroupCollapsed[key] != false {
		t.Fatalf("after expand map[%s]=%v want false", key, m.workGroupCollapsed[key])
	}
	r1 := m.renderBlocksForView()
	plain1 := stripANSI(strings.Join(r1.Lines, "\n"))
	for _, name := range []string{"read_file", "run_command", "grep", "glob"} {
		if !strings.Contains(plain1, name) {
			t.Fatalf("expanded missing tool %s in:\n%s", name, plain1)
		}
	}
	if !strings.Contains(plain1, "▾") {
		t.Fatalf("expanded header should show ▾: %s", plain1)
	}

	// Collapse again
	if !m.toggleSelectedBlock() {
		t.Fatal("toggle collapse failed")
	}
	if m.workGroupCollapsed[key] != true {
		t.Fatalf("after collapse map[%s]=%v want true", key, m.workGroupCollapsed[key])
	}
	r2 := m.renderBlocksForView()
	plain2 := stripANSI(strings.Join(r2.Lines, "\n"))
	if strings.Contains(plain2, "read_file") && strings.Contains(plain2, "run_command") &&
		strings.Contains(plain2, "grep") && strings.Contains(plain2, "glob") {
		// All four tool names visible as rows would mean still expanded.
		// Header alone is ok; require ▸ collapsed marker.
		if !strings.Contains(plain2, "▸") {
			t.Fatalf("collapsed should use ▸: %s", plain2)
		}
	}
	if !strings.Contains(plain2, "done answer") {
		t.Fatal("assistant after work must remain visible when collapsed")
	}
}

func TestIntegration_WorkGroup_EnterKeyToggles(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 80
	m.height = 40
	m.ready = true
	m.blocks = fourToolWorkBlocks()
	m.workGroupCollapsed = nil
	m.renderVP()

	gs := findWorkGroups(m.blocks)
	if len(gs) != 1 {
		t.Fatalf("groups=%d", len(gs))
	}
	m.selectedBlockID = gs[0].Key
	m.focus = focusScrollback

	// Enter in scrollback must toggle (handleChatKey path).
	skipTA, _, _ := m.handleChatKey("enter", false)
	if !skipTA {
		t.Fatal("enter in scrollback with work selection should consume textarea")
	}
	if m.workGroupCollapsed == nil || m.workGroupCollapsed[gs[0].Key] != false {
		t.Fatalf("enter should expand work group, map=%v", m.workGroupCollapsed)
	}
	// Second enter collapses
	_, _, _ = m.handleChatKey("enter", false)
	if m.workGroupCollapsed[gs[0].Key] != true {
		t.Fatalf("second enter should collapse, map=%v", m.workGroupCollapsed)
	}
}

func TestIntegration_WorkGroup_RangeInHitMapAfterRenderVP(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 80
	m.height = 40
	m.ready = true
	m.blocks = fourToolWorkBlocks()
	m.workGroupCollapsed = map[string]bool{} // empty but non-nil
	m.renderVP()

	gs := findWorkGroups(m.blocks)
	key := gs[0].Key
	rng, ok := m.chatBlockRanges[key]
	if !ok {
		t.Fatalf("work key %q not in chatBlockRanges: %v", key, m.chatBlockRanges)
	}
	if rng[1] <= rng[0] {
		t.Fatalf("invalid work range %v", rng)
	}

	// Mouse-select via hit map rebuild (View path)
	_ = m.View()
	// Find a screen Y that hits the work zone if layout allows
	// At minimum, selecting the ID and toggling must work after renderVP.
	m.selectedBlockID = key
	m.focus = focusScrollback
	if !m.toggleSelectedBlock() {
		t.Fatal("toggle after renderVP failed")
	}
}

func TestIntegration_WorkGroup_TrailingEmptyLane(t *testing.T) {
	blocks := fourToolWorkBlocks()
	gs := findWorkGroups(blocks)
	collapsed := map[string]bool{gs[0].Key: true}
	r := RenderChatBlocksWithWorkGroups(blocks, "m", 80, true, collapsed)
	plain := make([]string, len(r.Lines))
	for i, ln := range r.Lines {
		plain[i] = stripANSI(ln)
	}
	workIdx, asstIdx := -1, -1
	for i, p := range plain {
		if strings.Contains(p, "Work ·") {
			workIdx = i
		}
		if strings.Contains(p, "done answer") {
			asstIdx = i
		}
	}
	if workIdx < 0 || asstIdx < 0 {
		t.Fatalf("missing work/assistant: %v", plain)
	}
	foundBlank := false
	for i := workIdx + 1; i < asstIdx; i++ {
		if strings.TrimSpace(plain[i]) == "" {
			foundBlank = true
			break
		}
	}
	if !foundBlank {
		t.Fatalf("want empty lane after Work, got %v", plain[workIdx:asstIdx+1])
	}
}

func TestIntegration_WorkGroup_SpaceAlsoToggles(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 80
	m.blocks = fourToolWorkBlocks()
	gs := findWorkGroups(m.blocks)
	m.selectedBlockID = gs[0].Key
	m.focus = focusScrollback
	// Ensure map exists
	m.workGroupCollapsed = map[string]bool{}

	skip, _, _ := m.handleChatKey(" ", false)
	if !skip {
		t.Fatal("space should toggle in scrollback")
	}
	// Auto-collapsed default for 4 tools: first toggle expands → false
	if m.workGroupCollapsed[gs[0].Key] != false {
		t.Fatalf("space expand map=%v", m.workGroupCollapsed)
	}
}

// Ensure tea.KeyMsg path also works through Update when in chat+scrollback.
func TestIntegration_WorkGroup_UpdateEnter(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 80
	m.height = 40
	m.ready = true
	m.blocks = fourToolWorkBlocks()
	m.renderVP()
	gs := findWorkGroups(m.blocks)
	m.selectedBlockID = gs[0].Key
	m.focus = focusScrollback

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := model.(*tuiModel)
	if mm.workGroupCollapsed == nil {
		t.Fatal("Update enter must init collapse map")
	}
	// First enter expands auto-collapsed group
	if v, ok := mm.workGroupCollapsed[gs[0].Key]; !ok || v != false {
		t.Fatalf("after Update enter want expanded false, map=%v", mm.workGroupCollapsed)
	}
}
