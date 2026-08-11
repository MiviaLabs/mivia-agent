package cli

import (
	"testing"
)

// queueFixture builds a model whose three queue slices hold two plain items
// and one skill item, so alignment tests exercise every field.
func queueFixture(t *testing.T) *tuiModel {
	t.Helper()
	m := &tuiModel{}
	m.pendingQueue = []string{"first", "second", ""}
	m.pendingQueueLabels = []string{"first", "second", "/skill label"}
	m.pendingSkillTurns = []*skillSlashSpec{nil, nil, {display: "/skill label"}}
	return m
}

func TestQueueItemAtReturnsAlignedItem(t *testing.T) {
	m := queueFixture(t)
	item := m.queueItemAt(2)
	if item.sent != "" || item.display != "/skill label" || item.skill == nil {
		t.Fatalf("queueItemAt(2) = %+v, want skill item with empty sent and label display", item)
	}
	if item.index != 2 {
		t.Fatalf("queueItemAt(2).index = %d, want 2", item.index)
	}
	plain := m.queueItemAt(0)
	if plain.sent != "first" || plain.display != "first" || plain.skill != nil {
		t.Fatalf("queueItemAt(0) = %+v, want plain item", plain)
	}
}

func TestQueueItemAtOutOfRangeIsZero(t *testing.T) {
	m := queueFixture(t)
	if got := m.queueItemAt(99); got.sent != "" || got.display != "" || got.skill != nil {
		t.Fatalf("queueItemAt(99) = %+v, want zero item", got)
	}
}

func TestQueueRemoveAtSplicesAllThreeSlices(t *testing.T) {
	m := queueFixture(t)
	item := m.queueRemoveAt(1)
	if item.sent != "second" {
		t.Fatalf("removed sent = %q, want second", item.sent)
	}
	if len(m.pendingQueue) != 2 || len(m.pendingQueueLabels) != 2 || len(m.pendingSkillTurns) != 2 {
		t.Fatalf("slice lengths after remove = %d/%d/%d, want 2/2/2",
			len(m.pendingQueue), len(m.pendingQueueLabels), len(m.pendingSkillTurns))
	}
	if m.pendingQueue[1] != "" || m.pendingQueueLabels[1] != "/skill label" || m.pendingSkillTurns[1] == nil {
		t.Fatalf("skill item drifted out of alignment after remove: %q/%q/%v",
			m.pendingQueue[1], m.pendingQueueLabels[1], m.pendingSkillTurns[1])
	}
	if !equalStrings(m.pendingQueue, []string{"first", ""}) {
		t.Fatalf("pendingQueue = %v", m.pendingQueue)
	}
}

func TestQueueRemoveAtShortSlicesDoesNotPanic(t *testing.T) {
	// Some tests construct the queue with only pendingQueue set; removal must
	// tolerate the short label/skill slices (defensive, preserves old
	// sendNextQueued behavior).
	m := &tuiModel{}
	m.pendingQueue = []string{"/select"}
	item := m.queueRemoveAt(0)
	if item.sent != "/select" {
		t.Fatalf("removed sent = %q", item.sent)
	}
	if len(m.pendingQueue) != 0 {
		t.Fatalf("pendingQueue = %v, want empty", m.pendingQueue)
	}
}

func TestQueueInsertAtRestoresOriginalIndex(t *testing.T) {
	m := queueFixture(t)
	removed := m.queueRemoveAt(1)
	m.queueInsertAt(removed.index, removed)
	if len(m.pendingQueue) != 3 || len(m.pendingQueueLabels) != 3 || len(m.pendingSkillTurns) != 3 {
		t.Fatalf("slice lengths after insert = %d/%d/%d, want 3/3/3",
			len(m.pendingQueue), len(m.pendingQueueLabels), len(m.pendingSkillTurns))
	}
	if m.pendingQueue[1] != "second" || m.pendingQueueLabels[1] != "second" || m.pendingSkillTurns[1] != nil {
		t.Fatalf("inserted item misaligned: %q/%q/%v",
			m.pendingQueue[1], m.pendingQueueLabels[1], m.pendingSkillTurns[1])
	}
	if m.pendingQueue[2] != "" || m.pendingQueueLabels[2] != "/skill label" || m.pendingSkillTurns[2] == nil {
		t.Fatalf("skill item drifted: %q/%q/%v", m.pendingQueue[2], m.pendingQueueLabels[2], m.pendingSkillTurns[2])
	}
}

func TestQueueInsertAtClampsIndex(t *testing.T) {
	m := queueFixture(t)
	m.queueInsertAt(99, queuedItem{sent: "tail", display: "tail"})
	if len(m.pendingQueue) != 4 || m.pendingQueue[3] != "tail" {
		t.Fatalf("insert beyond end should append: %v", m.pendingQueue)
	}
	m.queueInsertAt(-1, queuedItem{sent: "head", display: "head"})
	if len(m.pendingQueue) != 5 || m.pendingQueue[0] != "head" {
		t.Fatalf("insert before start should prepend: %v", m.pendingQueue)
	}
}

func TestQueueAppendPreservesSkillShape(t *testing.T) {
	m := &tuiModel{}
	m.queueAppend(queuedItem{sent: "text", display: "text"})
	if len(m.pendingQueue) != 1 || len(m.pendingQueueLabels) != 1 || len(m.pendingSkillTurns) != 1 {
		t.Fatalf("append lengths = %d/%d/%d, want 1/1/1",
			len(m.pendingQueue), len(m.pendingQueueLabels), len(m.pendingSkillTurns))
	}
	if m.pendingSkillTurns[0] != nil {
		t.Fatalf("plain append must carry a nil skill, got %v", m.pendingSkillTurns[0])
	}
	m.queueAppend(queuedItem{sent: "", display: "/skill", skill: &skillSlashSpec{display: "/skill"}})
	if m.pendingSkillTurns[1] == nil || m.pendingQueue[1] != "" {
		t.Fatalf("skill append must preserve the spec and empty sent")
	}
}

func TestResetQueueStateClearsQueueAndManager(t *testing.T) {
	m := queueFixture(t)
	m.queueMgr = queueMgrState{open: true, selected: 1}
	m.editingQueued = true
	m.editMemory = queuedItem{index: 1, sent: "x"}
	m.resetQueueState()
	if len(m.pendingQueue) != 0 || len(m.pendingQueueLabels) != 0 || len(m.pendingSkillTurns) != 0 {
		t.Fatalf("reset must clear all three slices")
	}
	if m.queueMgr.open || m.editingQueued || m.editMemory.sent != "" {
		t.Fatalf("reset must clear manager and edit state: %+v %v %+v", m.queueMgr, m.editingQueued, m.editMemory)
	}
}

func TestClampQueueManagerAutoClosesAtZero(t *testing.T) {
	m := &tuiModel{}
	m.queueMgr = queueMgrState{open: true, selected: 3}
	m.clampQueueManager()
	if m.queueMgr.open {
		t.Fatalf("empty queue must auto-close the manager")
	}
}

func TestClampQueueManagerClampsSelection(t *testing.T) {
	m := queueFixture(t)
	m.queueMgr = queueMgrState{open: true, selected: 7}
	m.clampQueueManager()
	if !m.queueMgr.open || m.queueMgr.selected != 2 {
		t.Fatalf("selection must clamp to 2, got open=%v selected=%d", m.queueMgr.open, m.queueMgr.selected)
	}
	m.queueMgr.selected = -2
	m.clampQueueManager()
	if m.queueMgr.selected != 0 {
		t.Fatalf("negative selection must clamp to 0, got %d", m.queueMgr.selected)
	}
}

func TestRestoreQueueEditRestoresItemDraftAndCursor(t *testing.T) {
	m := &tuiModel{}
	m.textarea = newComposerTextarea()
	m.textarea.SetValue("the draft")
	m.textarea.SetCursor(4)
	m.editingQueued = true
	m.editMemory = queuedItem{index: 1, sent: "queued text", display: "queued text", savedDraft: "the draft", savedCursor: 4}
	m.pendingQueue = []string{"a", "b"}
	m.pendingQueueLabels = []string{"a", "b"}
	m.pendingSkillTurns = []*skillSlashSpec{nil, nil}
	m.restoreQueueEdit()
	if m.editingQueued {
		t.Fatalf("restore must clear editingQueued")
	}
	if len(m.pendingQueue) != 3 || m.pendingQueue[1] != "queued text" {
		t.Fatalf("item must be restored at its original index: %v", m.pendingQueue)
	}
	if m.textarea.Value() != "the draft" {
		t.Fatalf("draft not restored: %q", m.textarea.Value())
	}
}

func TestRestoreQueueEditNoopWhenNotEditing(t *testing.T) {
	m := &tuiModel{}
	m.restoreQueueEdit()
	if m.editingQueued {
		t.Fatalf("noop restore must not set editingQueued")
	}
}

// TestRestoreQueueEditMultiLineCaretRow pins the multi-line caret restore:
// bubbles SetValue parks the caret on the last line, so the original row must
// be walked back to before SetCursor can place the column.
func TestRestoreQueueEditMultiLineCaretRow(t *testing.T) {
	m := &tuiModel{}
	m.textarea = newComposerTextarea()
	m.textarea.SetValue("line one\nline two")
	m.textarea.CursorStart()
	m.editingQueued = true
	m.editMemory = queuedItem{
		index: 0, sent: "q", display: "q",
		savedDraft: "line one\nline two", savedCursor: 0, savedRow: 0,
	}
	m.pendingQueue = []string{"a"}
	m.pendingQueueLabels = []string{"a"}
	m.pendingSkillTurns = []*skillSlashSpec{nil}
	m.restoreQueueEdit()
	if m.textarea.Line() != 0 {
		t.Fatalf("caret row not restored: %d, want 0", m.textarea.Line())
	}
}
