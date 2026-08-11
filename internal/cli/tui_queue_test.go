package cli

import (
	"strings"
	"testing"
)

// TestSendNextQueuedSlashItemRunsLocally pins the canonical dispatch: a
// "/"-prefixed queued item runs as a slash command, never as a user turn, and
// any terminal command it staged (/select) is drained into queuedSlashCmds.
func TestSendNextQueuedSlashItemRunsLocally(t *testing.T) {
	m := sendQueueModel(t)
	m.pendingQueue = []string{"/select"}
	m.pendingQueueLabels = []string{"/select"}
	m.pendingSkillTurns = []*skillSlashSpec{nil}
	m.sendNextQueued()
	if len(m.pendingQueue) != 0 {
		t.Fatalf("queue not drained: %v", m.pendingQueue)
	}
	if len(m.queuedSlashCmds) != 1 {
		t.Fatalf("queuedSlashCmds = %d entries, want the /select terminal command staged", len(m.queuedSlashCmds))
	}
}

// sendQueueModel builds a chat model with a live session and stub completer,
// the same shape the force-send tests use, so a plain or skill dispatch really
// starts a turn instead of panicking on a nil session.
func sendQueueModel(t *testing.T) *tuiModel {
	t.Helper()
	m := newSmokeModel(t)
	m.mode = modeChat
	m.session = newTestSessionForModel("test-model")
	m.session.Completer = welcomeStubCompleter{}
	m.layout()
	m.renderVP()
	return m
}

func hasUserBlockText(m *tuiModel, want string) bool {
	for _, b := range m.blocks {
		if b.Kind == ChatBlockUser && strings.Contains(b.Text, want) {
			return true
		}
	}
	return false
}

// TestSendQueuedItemPlainStartsTurn verifies a plain queued item is dispatched
// through startAIWithDisplay (a user block appears) and the queue empties.
func TestSendQueuedItemPlainStartsTurn(t *testing.T) {
	m := sendQueueModel(t)
	m.queueTurn("second question", "second question")
	m.sendNextQueued()
	if len(m.pendingQueue) != 0 {
		t.Fatalf("queue not drained: %v", m.pendingQueue)
	}
	if !hasUserBlockText(m, "second question") {
		t.Fatalf("plain item did not start a turn; blocks: %v", blockTexts(m.blocks))
	}
}

// TestSendQueuedItemSkillStartsSkillTurn verifies a queued skill entry is
// dispatched through startSkillAI (its display label becomes the user block)
// and the queue empties with all three slices aligned.
func TestSendQueuedItemSkillStartsSkillTurn(t *testing.T) {
	m := sendQueueModel(t)
	m.queueSkillTurn(skillSlashSpec{display: "/skill label"})
	m.sendNextQueued()
	if len(m.pendingQueue) != 0 || len(m.pendingQueueLabels) != 0 || len(m.pendingSkillTurns) != 0 {
		t.Fatalf("queue not fully drained: %d/%d/%d",
			len(m.pendingQueue), len(m.pendingQueueLabels), len(m.pendingSkillTurns))
	}
	if !hasUserBlockText(m, "/skill label") {
		t.Fatalf("skill item did not start its turn; blocks: %v", blockTexts(m.blocks))
	}
}

// TestSendQueuedItemStopsAtFirstTurn verifies the drain sends exactly one
// plain item per call - the remaining queue survives for the next drain.
func TestSendQueuedItemStopsAtFirstTurn(t *testing.T) {
	m := sendQueueModel(t)
	m.pendingQueue = []string{"one", "two"}
	m.pendingQueueLabels = []string{"one", "two"}
	m.pendingSkillTurns = []*skillSlashSpec{nil, nil}
	m.sendNextQueued()
	if len(m.pendingQueue) != 1 || m.pendingQueue[0] != "two" {
		t.Fatalf("drain must stop after the first dispatched turn: %v", m.pendingQueue)
	}
}
