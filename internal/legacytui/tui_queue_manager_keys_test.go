package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"testing"
)

// queueManagerKeysModel builds a chat model (live session + stub completer)
// with a two-item queue and the manager open.
func queueManagerKeysModel(t *testing.T) *TUIModel {
	t.Helper()
	m := sendQueueModel(t)
	m.pendingQueue = []string{"a", "b"}
	m.pendingQueueLabels = []string{"a", "b"}
	m.pendingSkillTurns = []*SkillSlashSpec{nil, nil}
	m.queueMgr = QueueMgrState{open: true, selected: 0}
	return m
}

func TestQueueManagerNavClampsSelection(t *testing.T) {
	m := queueManagerKeysModel(t)
	if handled, _, _ := m.handleQueueManagerKey("down"); !handled || m.queueMgr.selected != 1 {
		t.Fatalf("down: handled=%v selected=%d, want 1", handled, m.queueMgr.selected)
	}
	if _, _, _ = m.handleQueueManagerKey("down"); m.queueMgr.selected != 1 {
		t.Fatalf("down past end must clamp to 1, got %d", m.queueMgr.selected)
	}
	if _, _, _ = m.handleQueueManagerKey("up"); m.queueMgr.selected != 0 {
		t.Fatalf("up must move to 0, got %d", m.queueMgr.selected)
	}
	if _, _, _ = m.handleQueueManagerKey("up"); m.queueMgr.selected != 0 {
		t.Fatalf("up past start must clamp to 0, got %d", m.queueMgr.selected)
	}
	if _, _, _ = m.handleQueueManagerKey("ctrl+p"); m.queueMgr.selected != 0 {
		t.Fatalf("ctrl+p must behave like up, got %d", m.queueMgr.selected)
	}
}

func TestQueueManagerSteerSendsSelectedItem(t *testing.T) {
	m := queueManagerKeysModel(t)
	m.queueMgr.selected = 1
	_, _, cmds := m.handleQueueManagerKey("enter")
	if m.queueMgr.open {
		t.Fatalf("steer must close the manager")
	}
	if len(m.pendingQueue) != 1 || m.pendingQueue[0] != "a" {
		t.Fatalf("steer must remove only the selected item: %v", m.pendingQueue)
	}
	if !hasUserBlockText(m, "b") {
		t.Fatalf("steered item was not sent; blocks: %v", blockTexts(m.blocks))
	}
	if len(cmds) == 0 {
		t.Fatalf("steer must return the poll command")
	}
}

func TestQueueManagerSteerSlashItemRunsLocally(t *testing.T) {
	m := queueManagerKeysModel(t)
	m.pendingQueue = []string{"/select"}
	m.pendingQueueLabels = []string{"/select"}
	m.pendingSkillTurns = []*SkillSlashSpec{nil}
	m.queueMgr.selected = 0
	_, _, cmds := m.handleQueueManagerKey("enter")
	if len(m.pendingQueue) != 0 {
		t.Fatalf("slash steer must drain the queue: %v", m.pendingQueue)
	}
	if len(cmds) != 1 {
		t.Fatalf("slash steer must return the staged command, got %d cmds", len(cmds))
	}
	if len(m.queuedSlashCmds) != 0 {
		t.Fatalf("slash steer must drain the staged command into the returned cmds, got %d left", len(m.queuedSlashCmds))
	}
}

func TestQueueManagerSteerSkillItem(t *testing.T) {
	m := queueManagerKeysModel(t)
	m.pendingQueue = []string{""}
	m.pendingQueueLabels = []string{"/skill label"}
	m.pendingSkillTurns = []*SkillSlashSpec{{display: "/skill label"}}
	m.queueMgr.selected = 0
	_, _, _ = m.handleQueueManagerKey("enter")
	if len(m.pendingQueue) != 0 {
		t.Fatalf("skill steer must drain the queue")
	}
	if !hasUserBlockText(m, "/skill label") {
		t.Fatalf("steered skill did not start; blocks: %v", blockTexts(m.blocks))
	}
}

func TestQueueManagerSteerOutOfRangeIsNoop(t *testing.T) {
	m := queueManagerKeysModel(t)
	m.queueMgr.selected = 7
	handled, _, _ := m.handleQueueManagerKey("enter")
	if !handled {
		t.Fatalf("out-of-range steer must still consume the key")
	}
	if len(m.pendingQueue) != 2 {
		t.Fatalf("out-of-range steer must not mutate the queue: %v", m.pendingQueue)
	}
}

func TestQueueManagerDeleteClampsAndAutoCloses(t *testing.T) {
	m := queueManagerKeysModel(t)
	m.queueMgr.selected = 1
	_, _, _ = m.handleQueueManagerKey("d")
	if len(m.pendingQueue) != 1 || m.pendingQueue[0] != "a" {
		t.Fatalf("delete must remove only the selected item: %v", m.pendingQueue)
	}
	if !m.queueMgr.open || m.queueMgr.selected != 0 {
		t.Fatalf("selection must clamp to 0 after delete, got open=%v selected=%d", m.queueMgr.open, m.queueMgr.selected)
	}
	_, _, _ = m.handleQueueManagerKey("d")
	if len(m.pendingQueue) != 0 || m.queueMgr.open {
		t.Fatalf("deleting the last item must auto-close the manager")
	}
}

func TestQueueManagerEditSkillRefusedBeforeMutation(t *testing.T) {
	m := queueManagerKeysModel(t)
	m.pendingQueue = []string{""}
	m.pendingQueueLabels = []string{"/skill label"}
	m.pendingSkillTurns = []*SkillSlashSpec{{display: "/skill label"}}
	m.queueMgr.selected = 0
	handled, _, _ := m.handleQueueManagerKey("e")
	if !handled {
		t.Fatalf("edit on a skill must still consume the key")
	}
	if len(m.pendingQueue) != 1 {
		t.Fatalf("edit on a skill must not remove the item: %v", m.pendingQueue)
	}
	if m.editingQueued {
		t.Fatalf("edit on a skill must not enter edit mode")
	}
	if !m.queueMgr.open {
		t.Fatalf("edit on a skill must not close the manager")
	}
}

func TestQueueManagerEditPlainLoadsComposer(t *testing.T) {
	m := queueManagerKeysModel(t)
	m.textarea.SetValue("the draft")
	m.pendingQueue = []string{"queued text"}
	m.pendingQueueLabels = []string{"queued text"}
	m.pendingSkillTurns = []*SkillSlashSpec{nil}
	m.queueMgr.selected = 0
	_, _, _ = m.handleQueueManagerKey("e")
	if !m.editingQueued {
		t.Fatalf("edit must enter edit mode")
	}
	if m.queueMgr.open || len(m.pendingQueue) != 0 {
		t.Fatalf("edit must remove the item and close the manager")
	}
	if m.textarea.Value() != "queued text" {
		t.Fatalf("composer must hold the queued text, got %q", m.textarea.Value())
	}
	if m.editMemory.savedDraft != "the draft" {
		t.Fatalf("edit must snapshot the draft, got %q", m.editMemory.savedDraft)
	}
}

func TestQueueManagerUnboundKeyConsumedWithoutEditingDraft(t *testing.T) {
	m := queueManagerKeysModel(t)
	m.textarea.SetValue("draft")
	handled, skipTextarea, _ := m.handleQueueManagerKey("x")
	if !handled || !skipTextarea {
		t.Fatalf("unbound key must be consumed with skipTextarea, got handled=%v skip=%v", handled, skipTextarea)
	}
	if m.textarea.Value() != "draft" {
		t.Fatalf("unbound key must not edit the draft behind the popup: %q", m.textarea.Value())
	}
}

func TestQueueManagerCloseKeys(t *testing.T) {
	for _, key := range []string{"esc", "q"} {
		m := queueManagerKeysModel(t)
		if handled, _, _ := m.handleQueueManagerKey(key); !handled || m.queueMgr.open {
			t.Fatalf("%s must close the manager (handled=%v open=%v)", key, handled, m.queueMgr.open)
		}
	}
}

func TestQueueManagerDeclinesWhenClosed(t *testing.T) {
	m := queueManagerKeysModel(t)
	m.closeQueueManager()
	if handled, _, _ := m.handleQueueManagerKey("d"); handled {
		t.Fatalf("closed manager must decline keys so d stays typable in the composer")
	}
}

func TestOpenQueueManagerGuards(t *testing.T) {
	m := sendQueueModel(t)
	// Empty queue.
	if m.openQueueManager() {
		t.Fatalf("must refuse to open with an empty queue")
	}
	// Wrong mode.
	m.pendingQueue = []string{"x"}
	m.pendingQueueLabels = []string{"x"}
	m.pendingSkillTurns = []*SkillSlashSpec{nil}
	m.mode = modeWelcome
	if m.openQueueManager() {
		t.Fatalf("must refuse to open outside chat mode")
	}
	// Happy path: chat mode with a queue.
	m.mode = modeChat
	if !m.openQueueManager() || !m.queueMgr.open || m.queueMgr.selected != 0 {
		t.Fatalf("must open with a non-empty queue in chat mode")
	}
	// Already open (modalOpen includes the manager itself).
	if m.openQueueManager() {
		t.Fatalf("must refuse to open while already open")
	}
	// While editing.
	m.closeQueueManager()
	m.editingQueued = true
	if m.openQueueManager() {
		t.Fatalf("must refuse to open while an edit is in progress")
	}
}

func TestOpenQueueManagerClosesComposerPopups(t *testing.T) {
	m := sendQueueModel(t)
	m.pendingQueue = []string{"x"}
	m.pendingQueueLabels = []string{"x"}
	m.pendingSkillTurns = []*SkillSlashSpec{nil}
	m.history.Open = true
	if !m.openQueueManager() {
		t.Fatalf("open must succeed")
	}
	if m.history.Open {
		t.Fatalf("open must close the history picker")
	}
}

// TestQueueManagerModalOwnsKeysWithSidebarFocused pins the routing order: the
// manager is a modal routed before the sidebars, so a focused sidebar can
// never steal its keys (a side effect would be a session-delete confirm armed
// by the same d the popup advertises as "delete message").
func TestQueueManagerModalOwnsKeysWithSidebarFocused(t *testing.T) {
	m := sendQueueModel(t)
	m.sessionsSidebar = newSessionsSidebar()
	m.focus = cli.FocusSidebar
	m.pendingQueue = []string{"a", "b"}
	m.pendingQueueLabels = []string{"a", "b"}
	m.pendingSkillTurns = []*SkillSlashSpec{nil, nil}
	m.queueMgr = QueueMgrState{open: true, selected: 1}

	if handled, _, _ := m.handleChatKey("d", false); !handled {
		t.Fatalf("d must be consumed by the manager")
	}
	if len(m.pendingQueue) != 1 || m.pendingQueue[0] != "a" {
		t.Fatalf("d must delete the queued item, not touch the sidebar: %v", m.pendingQueue)
	}
	if m.sessionsSidebar.confirm != 0 {
		t.Fatalf("sidebar must not arm a delete confirm behind the manager")
	}

	if _, _, _ = m.handleChatKey("esc", false); m.queueMgr.open {
		t.Fatalf("esc must close the manager, not the sidebar")
	}
}

// TestQueueManagerCtrlUpOpensFromSidebarFocus verifies the open chord works
// regardless of chat focus; the modal then owns every key.
func TestQueueManagerCtrlUpOpensFromSidebarFocus(t *testing.T) {
	m := sendQueueModel(t)
	m.sessionsSidebar = newSessionsSidebar()
	m.focus = cli.FocusSidebar
	m.pendingQueue = []string{"a"}
	m.pendingQueueLabels = []string{"a"}
	m.pendingSkillTurns = []*SkillSlashSpec{nil}
	handled, _, _ := m.handleChatKey("ctrl+up", false)
	if !handled || !m.queueMgr.open {
		t.Fatalf("ctrl+up must open the manager from sidebar focus (handled=%v open=%v)", handled, m.queueMgr.open)
	}
}

// TestQueueManagerCtrlUpConsumedWhileOpen pins the modal contract for the open
// chord itself: ctrl+up with the manager already open routes to the manager
// and never falls through to the composer.
func TestQueueManagerCtrlUpConsumedWhileOpen(t *testing.T) {
	m := queueManagerKeysModel(t)
	handled, skipTextarea, _ := m.handleChatKey("ctrl+up", false)
	if !handled || !skipTextarea {
		t.Fatalf("ctrl+up with the manager open must be consumed (handled=%v skip=%v)", handled, skipTextarea)
	}
	if !m.queueMgr.open {
		t.Fatalf("the manager must stay open")
	}
}
