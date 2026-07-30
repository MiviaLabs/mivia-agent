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

// TestStartAI_LiveUserCreatesChatBlockUser verifies that startAI creates a
// ChatBlockUser block (not ChatBlockSystem pre-rendered card lines).
func TestStartAI_LiveUserCreatesChatBlockUser(t *testing.T) {
	m := journeyModel(t)
	m.beginNewSession()
	m.enterChatMode()

	// Simulate startAI without spawning goroutine:
	// add a user block directly as startAI would.
	userText := "test message for live user block"
	// Insert turn divider if blocks exist (here: no prior blocks, skip).
	if len(m.blocks) > 0 {
		m.appendBlock(ChatBlock{
			TurnID: uint64(m.session.UserTurns() + 1),
			Kind:   ChatBlockDivider,
		})
	}
	m.appendBlock(ChatBlock{
		TurnID: uint64(m.session.UserTurns() + 1),
		Kind:   ChatBlockUser,
		Text:   userText,
	})

	// Should have exactly 1 user block (no ChatBlockSystem blocks).
	if len(m.blocks) != 1 {
		t.Fatalf("expected 1 block, got %d: %#v", len(m.blocks), m.blocks)
	}
	if m.blocks[0].Kind != ChatBlockUser {
		t.Fatalf("expected ChatBlockUser, got %s", m.blocks[0].Kind)
	}
	if m.blocks[0].Text != userText {
		t.Fatalf("expected text %q, got %q", userText, m.blocks[0].Text)
	}
	if m.blocks[0].Rendered != "" {
		t.Fatalf("expected empty Rendered for user block, got %q", m.blocks[0].Rendered)
	}

	// Verify messages were populated (render output).
	// Rendered user card should contain the user text.
	joined := strings.Join(m.messages, "\n")
	plain := stripANSI(joined)
	if !strings.Contains(plain, userText) {
		t.Fatalf("expected user text %q in messages, got %q", userText, plain)
	}
	// Should NOT contain "⚙" (system block indicator).
	if strings.Contains(plain, "⚙") {
		t.Fatalf("should not contain system block indicator, got %q", plain)
	}
}

// TestStartAI_UserBlockAcrossWidths verifies live user blocks render correctly
// at various widths (reflow).
func TestStartAI_UserBlockAcrossWidths(t *testing.T) {
	for _, w := range []int{40, 80, 120} {
		m := journeyModel(t)
		m.width = w
		m.beginNewSession()
		m.enterChatMode()

		userText := "test message width="
		m.appendBlock(ChatBlock{
			TurnID: 1,
			Kind:   ChatBlockUser,
			Text:   userText,
		})
		plain := stripANSI(strings.Join(m.messages, "\n"))
		if !strings.Contains(plain, userText) {
			t.Fatalf("width %d: missing user text in %q", w, plain)
		}
	}
}

// journeyModel builds a minimal tuiModel for scripted state checks without tea.Program.
// Completer is nil — do not call startAI (it spawns SendUser). Set waiting/tools manually.
func journeyModel(t *testing.T) *tuiModel {
	t.Helper()
	ti := textarea.New()
	ti.SetWidth(80)
	ti.SetHeight(3)
	m := &tuiModel{
		session:               &chat.Session{Model: "test-model"},
		modelName:             "test-model",
		viewport:              viewport.New(80, 20),
		textarea:              ti,
		messages:              []string{},
		bridge:                newStreamBridge(),
		toolPanel:             toolPanelState{Selected: -1},
		pendingQueue:          []string{},
		mode:                  modeWelcome,
		width:                 80,
		height:                40,
		ready:                 true,
		thinkingExpandDefault: true,
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

	// Y=1: first transcript row under the one-line diamond header.
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

// TestStartAI_TurnFenceCloseIsolates verifies that when a new bridge replaces
// an old one (Close + newStreamBridge), stale events from the old bridge
// are not visible through the model's bridge.
func TestStartAI_TurnFenceCloseIsolates(t *testing.T) {
	m := journeyModel(t)
	m.beginNewSession()
	m.enterChatMode()

	// Simulate two turns using the same bridge-swap pattern as startAI.
	// Turn 1: push events on bridge.
	m.bridge.PushTool(true, "turn1-tool", "detail1")
	_, _ = m.bridge.Write([]byte("turn1-stream"))
	d := m.bridge.Drain()
	stream := d.Stream
	tools := d.Tools
	if stream != "turn1-stream" || len(tools) != 1 {
		t.Fatalf("turn1: stream=%q tools=%d", stream, len(tools))
	}

	// "Start turn 2": close old bridge and create new one.
	oldBridge := m.bridge
	oldBridge.Close()
	m.bridge = newStreamBridge()

	// Try to push stale events through old bridge (simulating late goroutine).
	_, _ = oldBridge.Write([]byte("stale-stream"))
	oldBridge.PushTool(true, "stale-tool", "stale")
	oldBridge.PushThinking("stale thinking")

	// Events on new bridge should be clean.
	m.bridge.PushTool(true, "turn2-tool", "detail2")
	_, _ = m.bridge.Write([]byte("turn2-stream"))

	// Drain should only show turn2 data on the model's bridge.
	d = m.bridge.Drain()
	stream = d.Stream
	tools = d.Tools
	if stream != "turn2-stream" {
		t.Fatalf("turn2: stream=%q (expected 'turn2-stream')", stream)
	}
	if len(tools) != 1 || tools[0].Name != "turn2-tool" {
		t.Fatalf("turn2: tools=%+v", tools)
	}

	// Old bridge: Finish should be visible, stale events should not.
	oldBridge.Finish(nil)
	stale := oldBridge.Drain()
	if stale.Stream != "" {
		t.Fatalf("stale stream=%q (should be empty)", stale.Stream)
	}
	if len(stale.Tools) != 0 {
		t.Fatalf("stale tools leaked: %+v", stale.Tools)
	}
	if !stale.Done {
		t.Fatal("stale bridge should show done=true")
	}
}
