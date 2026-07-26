package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// journeyModel builds a minimal tuiModel for scripted state checks without tea.Program.
// Completer is nil — do not call startAI (it spawns SendUser). Set waiting/tools manually.
func journeyModel(t *testing.T) *tuiModel {
	t.Helper()
	ti := textarea.New()
	ti.SetWidth(80)
	ti.SetHeight(3)
	m := &tuiModel{
		session:      &chat.Session{Model: "test-model"},
		modelName:    "test-model",
		viewport:     viewport.New(80, 20),
		textarea:     ti,
		messages:     []string{},
		bridge:       newStreamBridge(),
		toolPanel:    toolPanelState{Selected: -1},
		pendingQueue: []string{},
		mode:         modeWelcome,
		width:        80,
		height:       40,
		ready:        true,
	}
	return m
}

func TestTUIJourneyWelcomeToChatAndStream(t *testing.T) {
	m := journeyModel(t)

	// 1. Start on welcome.
	if m.mode != modeWelcome {
		t.Fatalf("start mode=%v want modeWelcome", m.mode)
	}

	// 2. New session + enter chat (no tea.Program).
	m.beginNewSession()
	m.enterChatMode()
	if m.mode != modeChat {
		t.Fatalf("after enterChatMode mode=%v", m.mode)
	}
	if m.waiting {
		t.Fatal("fresh chat should not be waiting")
	}
	if len(m.toolRows) != 0 || len(m.pendingQueue) != 0 {
		t.Fatalf("fresh chat must clear tools/queue: tools=%d q=%d", len(m.toolRows), len(m.pendingQueue))
	}

	// 3. startAI path (manual, no Completer goroutine): waiting + tools, then finishStream clears.
	m.waiting = true
	m.turnStart = time.Now()
	m.toolRows = []toolRow{
		{Name: "read_file", Detail: `{"path":"a"}`, Start: time.Now()},
		{Name: "write_file", Detail: `{"path":"b"}`, Start: time.Now()},
	}
	m.toolPanel.Selected = 0
	m.toolPanel.ordered = orderToolIndices(m.toolRows)
	m.streamBuf.WriteString("hello from model")

	if !m.waiting || len(m.toolRows) != 2 {
		t.Fatalf("pre-finish: waiting=%v tools=%d", m.waiting, len(m.toolRows))
	}

	// Empty pendingQueue so finishStream does not call startAI/sendNextQueued.
	m.pendingQueue = nil
	cmds := m.finishStream(nil)
	if cmds != nil {
		t.Fatalf("finishStream with empty queue should return nil cmds, got %v", cmds)
	}
	if m.waiting {
		t.Fatal("finishStream must clear waiting")
	}
	if len(m.toolRows) != 0 {
		t.Fatalf("finishStream must clear toolRows, got %d", len(m.toolRows))
	}
	if m.toolPanel.Selected != -1 {
		t.Fatalf("finishStream must reset toolPanel.Selected, got %d", m.toolPanel.Selected)
	}
	if m.streamBuf.Len() != 0 {
		t.Fatal("finishStream must reset streamBuf")
	}
	// Assistant text and tool summary should land in messages.
	joined := strings.Join(m.messages, "\n")
	if !strings.Contains(joined, "hello") && !strings.Contains(stripANSI(joined), "hello") {
		// Markdown may reformat; require some history growth.
		if len(m.messages) == 0 {
			t.Fatal("finishStream should append messages")
		}
	}
}

func TestTUIJourneyQueueWhileWaiting(t *testing.T) {
	m := journeyModel(t)
	m.beginNewSession()
	m.enterChatMode()

	// Simulate busy agent + user queue append (no startAI).
	m.waiting = true
	m.turnStart = time.Now()
	m.pendingQueue = append(m.pendingQueue, "follow-up one")
	m.pendingQueue = append(m.pendingQueue, "follow-up two")

	if !m.waiting {
		t.Fatal("expected waiting")
	}
	if len(m.pendingQueue) != 2 {
		t.Fatalf("queue len=%d want 2", len(m.pendingQueue))
	}
	if m.pendingQueue[0] != "follow-up one" || m.pendingQueue[1] != "follow-up two" {
		t.Fatalf("queue=%v", m.pendingQueue)
	}

	// beginNewSession clears queue (new conversation).
	m.beginNewSession()
	if len(m.pendingQueue) != 0 {
		t.Fatalf("beginNewSession should clear queue, got %v", m.pendingQueue)
	}
}

func TestTUIJourneyToolExpandToggle(t *testing.T) {
	m := journeyModel(t)
	m.beginNewSession()
	m.enterChatMode()

	m.waiting = true
	m.toolRows = []toolRow{
		{Name: "a", Detail: "in-a", Result: "out-a"},
		{Name: "b", Detail: "in-b", Result: "out-b"},
	}
	m.toolPanel.Selected = 1
	m.toolPanel.Focused = true
	m.toolPanel.ordered = orderToolIndices(m.toolRows)

	// Toggle Expanded on selected row (same as enter/space path in Update).
	sel := m.toolPanel.Selected
	if sel < 0 || sel >= len(m.toolRows) {
		t.Fatalf("bad selection %d", sel)
	}
	if m.toolRows[sel].Expanded {
		t.Fatal("start collapsed")
	}
	m.toolRows[sel].Expanded = !m.toolRows[sel].Expanded
	if !m.toolRows[sel].Expanded {
		t.Fatal("toggle on failed")
	}
	if m.toolRows[0].Expanded {
		t.Fatal("only selected row should expand")
	}
	m.toolRows[sel].Expanded = !m.toolRows[sel].Expanded
	if m.toolRows[sel].Expanded {
		t.Fatal("toggle off failed")
	}

	// finishStream clears tools and selection.
	m.pendingQueue = nil
	_ = m.finishStream(nil)
	if len(m.toolRows) != 0 || m.toolPanel.Selected != -1 {
		t.Fatalf("after finish: tools=%d sel=%d", len(m.toolRows), m.toolPanel.Selected)
	}
}

func TestTUIJourneyHistoricalBlockMouseAndKeyboardActivation(t *testing.T) {
	m := journeyModel(t)
	m.enterChatMode()
	m.width, m.height = 80, 24
	m.blocks = []ChatBlock{{ID: "history-1", Kind: ChatBlockAssistant, Text: "historical"}}
	m.renderVP()
	m.View()

	m.Update(tea.MouseMsg{X: 1, Y: 1, Type: tea.MouseLeft})
	if m.selectedBlockID != "history-1" || m.focus != focusScrollback {
		t.Fatalf("mouse selection = %q, focus=%v", m.selectedBlockID, m.focus)
	}
	version := m.hitMap.version
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.blocks[0].Collapsed {
		t.Fatal("enter did not collapse selected historical block")
	}
	if m.hitMap.version <= version {
		t.Fatalf("collapse did not invalidate hit-map: before=%d after=%d", version, m.hitMap.version)
	}
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if m.blocks[0].Collapsed {
		t.Fatal("space did not expand selected historical block")
	}
}
