package cli

import (
	"strings"
	"testing"
)

// queueManagerRenderModel builds a model with a three-item queue (two plain,
// one skill) and the manager open on the middle item.
func queueManagerRenderModel() *tuiModel {
	m := &tuiModel{}
	m.pendingQueue = []string{"first message", "second message", ""}
	m.pendingQueueLabels = []string{"first message", "second message", "/skill label"}
	m.pendingSkillTurns = []*skillSlashSpec{nil, nil, {display: "/skill label"}}
	m.queueMgr = queueMgrState{open: true, selected: 1}
	m.width = 100
	return m
}

func TestRenderQueuePanelShowsQueue(t *testing.T) {
	m := queueManagerRenderModel()
	panel, size := m.renderQueuePanel(80, 24)
	if panel == "" || size.W == 0 || size.H == 0 {
		t.Fatalf("renderQueuePanel = %q size %+v, want a drawn panel", panel, size)
	}
	for _, want := range []string{"queue (3)", "first message", "second message", "/skill label", "enter send now", "esc close"} {
		if !strings.Contains(panel, want) {
			t.Errorf("panel missing %q:\n%s", want, panel)
		}
	}
}

func TestRenderQueuePanelMarksNextItem(t *testing.T) {
	m := queueManagerRenderModel()
	panel, _ := m.renderQueuePanel(80, 24)
	if !strings.Contains(panel, "next") {
		t.Fatalf("head item must carry the next badge:\n%s", panel)
	}
}

func TestRenderQueuePanelSkillFooterWhenSkillSelected(t *testing.T) {
	m := queueManagerRenderModel()
	m.queueMgr.selected = 2
	panel, _ := m.renderQueuePanel(80, 24)
	if !strings.Contains(panel, "skills: no edit") {
		t.Fatalf("skill selection must swap the footer:\n%s", panel)
	}
	if strings.Contains(panel, "e edit") {
		t.Fatalf("skill selection must not advertise edit:\n%s", panel)
	}
}

func TestRenderQueuePanelClosedOrEmpty(t *testing.T) {
	m := queueManagerRenderModel()
	m.queueMgr = queueMgrState{}
	if panel, _ := m.renderQueuePanel(80, 24); panel != "" {
		t.Fatalf("closed manager must render nothing, got %q", panel)
	}
	m.queueMgr = queueMgrState{open: true}
	m.pendingQueue = nil
	m.pendingQueueLabels = nil
	m.pendingSkillTurns = nil
	if panel, _ := m.renderQueuePanel(80, 24); panel != "" {
		t.Fatalf("empty queue must render nothing, got %q", panel)
	}
}

func TestRenderQueuePanelNarrowTerminalFallback(t *testing.T) {
	m := queueManagerRenderModel()
	// maxH 2 is below the framed-popup floor (4 with a footer): the engine
	// falls back to a single selected row.
	panel, size := m.renderQueuePanel(80, 2)
	if panel == "" || size.H != 1 {
		t.Fatalf("narrow fallback = %q size %+v, want a single-row panel", panel, size)
	}
	if !strings.Contains(panel, "second message") || !strings.Contains(panel, "+2") {
		t.Fatalf("narrow row must show the selection and remaining count: %q", panel)
	}
}

func TestRenderQueuePanelSanitizesEscapeSequences(t *testing.T) {
	m := queueManagerRenderModel()
	m.pendingQueue[0] = "bad \x1b[31mred\x1b[0m and\nnewline"
	m.pendingQueueLabels[0] = m.pendingQueue[0]
	panel, _ := m.renderQueuePanel(80, 24)
	if strings.Contains(panel, "\x1b[31m") {
		t.Fatalf("panel must strip user-content CSI escapes:\n%s", panel)
	}
	if !strings.Contains(panel, "red") {
		t.Fatalf("sanitized text must survive: %s", panel)
	}
	if !strings.Contains(panel, "⏎") {
		t.Fatalf("panel must render newlines as ⏎:\n%s", panel)
	}
}

func TestOverlayWindowWidthArgWidens(t *testing.T) {
	rows := []string{"a very long queued message that needs more than seventy two columns to breathe"}
	_, defaultSize := renderOverlayWindow(rows, 0, 8, 120, 24, "t", "f")
	_, wideSize := renderOverlayWindow(rows, 0, 8, 120, 24, "t", "f", 90)
	if defaultSize.W > 72 {
		t.Fatalf("default width = %d, want the 72-col cap", defaultSize.W)
	}
	if wideSize.W <= defaultSize.W {
		t.Fatalf("explicit width = %d, want wider than default %d", wideSize.W, defaultSize.W)
	}
}

// TestOverlayWindowSingleRowFallbackClampsSelected pins the render guard: a
// stale selection (queue drained after delete) must not panic the single-row
// fallback on a short terminal.
func TestOverlayWindowSingleRowFallbackClampsSelected(t *testing.T) {
	panel, size := renderOverlayWindow([]string{"a"}, 5, 8, 80, 2, "t", "f")
	if panel == "" || size.H != 1 {
		t.Fatalf("fallback with stale selection = %q size %+v, want a single row", panel, size)
	}
}
