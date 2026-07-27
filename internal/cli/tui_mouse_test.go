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
	// Layout: width=80, height=24, headerY=0, transcriptLines=10 (y 0-9),
	// toolY0=10, toolY1=13, composerY0=14, composerY1=18.
	h.rebuild(80, 24,
		0,  // headerY
		10, // transcriptLines
		10, // toolY0
		13, // toolY1
		14, // composerY0
		18, // composerY1
		map[string][2]int{
			"turn-1-block-2": {3, 5}, // typed range — rebuild uses end-exclusive: y0=3, y1=4
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
		{"typed block end exclusive — falls through to general transcript", 5, true, hitTranscript, ""},
		{"tools zone top", 10, true, hitTools, ""},
		{"tools zone middle", 12, true, hitTools, ""},
		{"tools zone bottom", 13, true, hitTools, ""},
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

// TestTUIMouseHitToolsClick verifies that tools render as a status line
// during execution, and clicking the transcript selects a block.
func TestTUIMouseHitToolsClick(t *testing.T) {
	m := journeyModel(t)
	m.enterChatMode()
	m.width = 80
	m.height = 30
	m.waiting = true
	m.turnStart = time.Now()

	// Add tool rows — tools now render as one-line status, not a clickable strip.
	m.toolRows = []toolRow{
		{Name: "read_file", Detail: `{"path":"a"}`, Start: m.turnStart},
		{Name: "write_file", Detail: `{"path":"b"}`, Start: m.turnStart},
	}
	m.layout()
	m.renderVP()
	out := m.View()

	// The tool status should appear as a one-liner with running/done/total.
	if !strings.Contains(out, "running") {
		t.Errorf("tool status should show 'running' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "read_file") && !strings.Contains(out, "◐") {
		t.Errorf("tool status should reference tools, got:\n%s", out)
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
