package legacytui

// Diff-coverage for the queue-manager commit (0503293): these tests exercise
// the previously uncovered branches of the queue routing, edit flow, cancel,
// tab cycling, run-dashboard key routing, and /queue refusals.

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"strings"
	"testing"
)

func hasSystemBlockText(m *TUIModel, want string) bool {
	for _, b := range m.blocks {
		if b.Kind == cli.ChatBlockSystem && strings.Contains(b.Text, want) {
			return true
		}
	}
	return false
}

func TestHandleQueueRoutingCtrlUpWhenOpenFails(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat // empty queue: openQueueManager refuses
	handled, consumed, _ := m.handleQueueRouting("ctrl+up")
	if handled || consumed {
		t.Fatalf("ctrl+up with an empty queue must not be handled or consumed, got %v/%v", handled, consumed)
	}
}

func TestHandleRunDashKeyNonArrowKeyNotConsumed(t *testing.T) {
	m := newSmokeModel(t)
	m.runDash = newRunDashboard()
	m.runDash.open = true
	m.runDash.runs["r1"] = &dashRunInfo{RunID: "r1", DisplayName: "d"}
	m.focus = cli.FocusScrollback
	handled, consumed, _ := m.handleRunDashKey("enter")
	if handled || consumed {
		t.Fatalf("a non-arrow key must not be consumed by the run dashboard, got %v/%v", handled, consumed)
	}
}

func TestHandleTabCycleSidebarCyclesFocus(t *testing.T) {
	m := newSmokeModel(t)
	m.sessionsSidebar = newSessionsSidebar()
	m.width = 240
	if !m.sidebarVisible() {
		t.Fatal("test setup: sessions sidebar must be visible at width 240")
	}
	handled, consumed, _ := m.handleTabCycle("tab")
	if !handled || !consumed {
		t.Fatalf("tab with a sidebar open must be handled and consumed, got %v/%v", handled, consumed)
	}
}

func TestHandleTabCycleChatFocusConsumesWithoutSidebar(t *testing.T) {
	m := newSmokeModel(t)
	m.focus = cli.FocusScrollback
	handled, consumed, _ := m.handleTabCycle("tab")
	if !handled || consumed {
		t.Fatalf("tab must always be consumed by chat-focus cycling, got %v/%v", handled, consumed)
	}
}

func TestHandleChatCancelRestoresQueueEdit(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newSmokeModel(t)
	m.mode = modeChat
	m.editingQueued = true
	m.editMemory = QueuedItem{index: 0, sent: "orig", display: "orig", savedDraft: "draft", savedRow: 0, savedCursor: 0}
	m.handleChatCancel()
	if m.editingQueued {
		t.Fatal("cancel must end the queue edit")
	}
	if len(m.pendingQueue) != 1 || m.pendingQueue[0] != "orig" {
		t.Fatalf("cancel must restore the edited item to the queue, got %v", m.pendingQueue)
	}
}

func TestQueueManagerEditOutOfRangeIsNoop(t *testing.T) {
	m := queueManagerKeysModel(t)
	m.queueMgr.selected = 7
	handled, _, _ := m.handleQueueManagerKey("e")
	if !handled {
		t.Fatal("out-of-range edit must still consume the key")
	}
	if len(m.pendingQueue) != 2 {
		t.Fatalf("out-of-range edit must not mutate the queue: %v", m.pendingQueue)
	}
	if !m.queueMgr.open {
		t.Fatal("out-of-range edit must leave the manager open")
	}
}

func TestQueueEditEnterEmptyRestoresItem(t *testing.T) {
	m := sendQueueModel(t)
	m.editingQueued = true
	m.editMemory = QueuedItem{index: 0, sent: "orig", display: "orig", savedDraft: "draft"}
	m.textarea.SetValue("")
	handled, consumed, _ := m.handleQueueEditEnter()
	if !handled || !consumed {
		t.Fatalf("empty-draft enter must be handled and consumed, got %v/%v", handled, consumed)
	}
	if m.editingQueued {
		t.Fatal("empty-draft enter must end the edit")
	}
	if len(m.pendingQueue) != 1 || m.pendingQueue[0] != "orig" {
		t.Fatalf("empty-draft enter must restore the original item, got %v", m.pendingQueue)
	}
}

func TestQueueEditEnterIdleStartsTurn(t *testing.T) {
	m := sendQueueModel(t)
	m.editingQueued = true
	m.editMemory = QueuedItem{index: 0, sent: "orig", display: "orig"}
	m.textarea.SetValue("hello")
	m.waiting = false
	handled, consumed, cmds := m.handleQueueEditEnter()
	if !handled || consumed {
		t.Fatalf("idle enter must be handled but leave the key unconsumed, got %v/%v", handled, consumed)
	}
	if m.editingQueued {
		t.Fatal("idle enter must clear the edit state")
	}
	if !hasUserBlockText(m, "hello") {
		t.Fatalf("idle enter must start the turn; blocks: %v", blockTexts(m.blocks))
	}
	if len(cmds) != 1 {
		t.Fatalf("idle enter must return the poll command, got %d cmds", len(cmds))
	}
}

func TestTuiQueueSlashRefusalChatModeOnly(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeWelcome
	if !m.handleTuiQueueSlash() {
		t.Fatal("/queue must be handled")
	}
	if !hasSystemBlockText(m, "chat mode only") {
		t.Fatalf("/queue in a non-chat mode must explain the refusal; blocks: %v", blockTexts(m.blocks))
	}
}

func TestTuiQueueSlashRefusalWhileEditing(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.editingQueued = true
	if !m.handleTuiQueueSlash() {
		t.Fatal("/queue must be handled")
	}
	if !hasSystemBlockText(m, "finish editing the queued message first") {
		t.Fatalf("/queue while editing must explain the refusal; blocks: %v", blockTexts(m.blocks))
	}
}

func TestTuiQueueSlashRefusalAlreadyOpen(t *testing.T) {
	m := queueManagerKeysModel(t) // manager already open with two items
	if !m.handleTuiQueueSlash() {
		t.Fatal("/queue must be handled")
	}
	if !hasSystemBlockText(m, "queue manager is already open") {
		t.Fatalf("/queue with the manager open must say so; blocks: %v", blockTexts(m.blocks))
	}
}

func TestRenderQueuePanelFallsBackToSentText(t *testing.T) {
	m := queueManagerRenderModel()
	m.pendingQueue[0] = "actual sent text"
	m.pendingQueueLabels[0] = ""
	panel, _ := m.renderQueuePanel(80, 24)
	if !strings.Contains(panel, "actual sent text") {
		t.Fatalf("panel must fall back to the sent text when display is empty:\n%s", panel)
	}
}
