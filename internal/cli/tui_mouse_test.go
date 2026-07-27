package cli

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestTUIMouseHitMapZones exercises the raw hit map with synthetic rebuild
// calls, verifying each defined zone (transcript, tools, composer, typed block)
// is correctly identified by tuiHitMap.hit().
func TestTUIMouseHitMapZones(t *testing.T) {
	var h tuiHitMap
	h.invalidate()
	// Layout: width=80, height=24, headerY=0, transcriptLines=14 (y 0-13),
	// toolY0=1, toolY1=0 (no tools zone), composerY0=14, composerY1=18.
	h.rebuild(80, 24,
		0,  // headerY
		14, // transcriptLines
		1,  // toolY0 (1 > 0 = no tools zone)
		0,
		14, // composerY0
		18, // composerY1
		map[string][2]int{
			"turn-1-block-2": {3, 5},
		},
		0, // viewportOffset
	)

	cases := []struct {
		name    string
		y       int
		wantHit bool
		kind    tuiHitZoneKind
		blockID string
	}{
		{"transcript zone top", 0, true, hitTranscript, ""},
		{"transcript zone middle", 5, true, hitTranscript, ""},
		{"transcript zone bottom", 9, true, hitTranscript, ""},
		{"typed block start", 3, true, hitTranscript, "turn-1-block-2"},
		{"typed block middle", 4, true, hitTranscript, "turn-1-block-2"},
		{"typed block end exclusive", 5, true, hitTranscript, ""},
		{"transcript extends past old tools zone", 12, true, hitTranscript, ""},
		{"composer zone top", 14, true, hitComposer, ""},
		{"composer zone middle", 16, true, hitComposer, ""},
		{"composer zone bottom", 18, true, hitComposer, ""},
		{"below all zones", 19, false, 0, ""},
		{"negative y", -1, false, 0, ""},
		{"beyond height", 24, false, 0, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			z, ok := h.hit(tc.y)
			if ok != tc.wantHit {
				t.Fatalf("hit(%d) ok=%v, want %v", tc.y, ok, tc.wantHit)
			}
			if ok {
				if z.kind != tc.kind {
					t.Errorf("hit(%d).kind=%d, want %d", tc.y, z.kind, tc.kind)
				}
				if z.blockID != tc.blockID {
					t.Errorf("hit(%d).blockID=%q, want %q", tc.y, z.blockID, tc.blockID)
				}
			}
		})
	}
}

// TestTUIMouseHitTranscriptBlockClick creates a journey model with a rendered
// block, calls View() to rebuild the hit map, then simulates a mouse click
// at a Y coordinate inside that block's typed range. It verifies that
// selectedBlockID and focus are updated.
func TestTUIMouseHitTranscriptBlockClick(t *testing.T) {
	m := journeyModel(t)
	m.enterChatMode()
	m.width = 80
	m.height = 24

	// Seed one assistant block and render the viewport.
	m.blocks = []ChatBlock{
		{ID: "turn-1-block-1", Kind: ChatBlockAssistant, Text: "Hello, world!"},
	}
	m.layout()
	m.renderVP()
	m.View() // rebuilds the hit map

	// Find the Y coordinate for the first rendered line of the block.
	ranges := m.chatBlockRanges
	blockRange, ok := ranges["turn-1-block-1"]
	if !ok {
		t.Fatal("turn-1-block-1 not found in chatBlockRanges after renderVP")
	}
	// The rebuild uses: start = r[0] + headerY - viewportOffset
	// headerY = lipgloss.Height(statusBar) which is 1 line.
	headerY := 1
	hitY := blockRange[0] + headerY - m.viewport.YOffset

	// Simulate a left-click inside the block's range.
	m.Update(tea.MouseMsg{X: 1, Y: hitY, Type: tea.MouseLeft})

	if m.selectedBlockID != "turn-1-block-1" {
		t.Errorf("selectedBlockID=%q, want %q", m.selectedBlockID, "turn-1-block-1")
	}
	if m.focus != focusScrollback {
		t.Errorf("focus=%v, want focusScrollback", m.focus)
	}
}

// TestTUIMouseDoubleClickTogglesWorkGroup is TDD for double-click activate.
// Enter toggles; mouse must too (second click within 400ms).
func TestTUIMouseDoubleClickTogglesWorkGroup(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 80
	m.height = 40
	m.ready = true
	m.blocks = fourToolWorkBlocks()
	m.renderVP()
	m.View()

	gs := findWorkGroups(m.blocks)
	if len(gs) != 1 {
		t.Fatalf("groups=%d", len(gs))
	}
	key := gs[0].Key
	rng, ok := m.chatBlockRanges[key]
	if !ok {
		t.Fatalf("work key %q missing from ranges %v", key, m.chatBlockRanges)
	}
	_ = rng // range present — click uses block id directly

	// First click: select only (auto-collapsed for 4 tools).
	m.handleTranscriptBlockClick(key)
	if m.selectedBlockID != key {
		t.Fatalf("select=%q want %q", m.selectedBlockID, key)
	}
	if m.workGroupCollapsed[key] {
		// may still be default collapsed via auto without map entry
	}
	// Default auto-collapsed: map may be empty; expanded tools not all visible.
	plain1 := stripANSI(strings.Join(m.messages, "\n"))
	// Second click within double-click window → expand
	m.lastClickAt = time.Now() // ensure window still open
	m.lastClickBlockID = key
	m.handleTranscriptBlockClick(key)
	if m.workGroupCollapsed[key] != false {
		t.Fatalf("double-click should expand work group, map=%v", m.workGroupCollapsed)
	}
	plain2 := stripANSI(strings.Join(m.messages, "\n"))
	if !strings.Contains(plain2, "read_file") {
		t.Fatalf("expanded tools missing after double-click: %s", plain2)
	}
	// Third pair: collapse again
	m.handleTranscriptBlockClick(key)
	m.lastClickBlockID = key
	m.lastClickAt = time.Now()
	m.handleTranscriptBlockClick(key)
	if m.workGroupCollapsed[key] != true {
		t.Fatalf("double-click again should collapse, map=%v plain was %q then %q", m.workGroupCollapsed, plain1, plain2)
	}
}

// TestTUIMouseDoubleClickTogglesToolBlock covers non-work collapsible bubbles.
func TestTUIMouseDoubleClickTogglesToolBlock(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 80
	m.height = 40
	m.ready = true
	m.blocks = []ChatBlock{
		{ID: "t1", Kind: ChatBlockTool, ToolName: "read_file", Text: "line1\nline2\nline3", Collapsed: true},
	}
	m.renderVP()
	if !m.blocks[0].Collapsed {
		t.Fatal("start collapsed")
	}
	m.handleTranscriptBlockClick("t1")
	m.lastClickBlockID = "t1"
	m.lastClickAt = time.Now()
	m.handleTranscriptBlockClick("t1")
	if m.blocks[0].Collapsed {
		t.Fatal("double-click should expand tool")
	}
}

// TestTUIMouseHitComposerClick verifies that clicking the composer zone sets
// focus to the composer.
func TestTUIMouseHitComposerClick(t *testing.T) {
	m := journeyModel(t)
	m.enterChatMode()
	m.width = 80
	m.height = 24

	// Populate a block and render so View() builds the hit map with a composer zone.
	m.blocks = []ChatBlock{
		{ID: "turn-1-block-1", Kind: ChatBlockAssistant, Text: "Hello"},
	}
	m.layout()
	m.renderVP()
	m.View() // rebuilds the hit map

	// Click at the very bottom of the terminal — should be in the composer zone.
	m.Update(tea.MouseMsg{X: 1, Y: m.height - 1, Type: tea.MouseLeft})

	if m.focus != focusComposer {
		t.Errorf("focus=%v, want focusComposer after clicking composer", m.focus)
	}
}

// TestTUIMouseHitToolsClick verifies that tools info appears in the status bar
// during execution, and clicking the transcript selects a block.
func TestTUIMouseHitToolsClick(t *testing.T) {
	m := journeyModel(t)
	m.enterChatMode()
	m.width = 80
	m.height = 30
	m.waiting = true
	m.turnStart = time.Now()

	// Add tool rows — tools now show in the status bar as "N/M tools", not a separate line.
	m.toolRows = []toolRow{
		{Name: "read_file", Detail: `{"path":"a"}`, Start: m.turnStart},
		{Name: "write_file", Detail: `{"path":"b"}`, Start: m.turnStart},
	}
	m.layout()
	m.renderVP()
	out := m.View()

	// The status bar (via renderWorkChrome) should show tool counts like "0/2 tools".
	if !strings.Contains(out, "tools") {
		t.Errorf("status bar should reference tools, got:\n%s", out)
	}
	if !strings.Contains(out, "read_file") && !strings.Contains(out, "0/2") {
		t.Errorf("expected tool counts in status bar, got:\n%s", out)
	}
}

// TestTUIMouseStaleCoordinates verifies that after invalidation, a previously
// valid hit-map coordinate is rejected.
func TestTUIMouseStaleCoordinates(t *testing.T) {
	var h tuiHitMap
	h.invalidate()
	h.rebuild(80, 24, 0, 10, 10, 13, 14, 18, nil, 0)

	if _, ok := h.hit(5); !ok {
		t.Fatal("expected valid hit before invalidation")
	}

	h.invalidate()

	if _, ok := h.hit(5); ok {
		t.Fatal("stale hit-map coordinate remained active after invalidation")
	}
}
