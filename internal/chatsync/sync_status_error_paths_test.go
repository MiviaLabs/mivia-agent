package chatsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteFileAtomic_RenameErrorSurfaces pins the rename guard, distinct
// from TestWriteFileAtomicLeavesNoPartialFile's sync-failure case: a
// directory already at the target path makes the rename itself fail.
func TestWriteFileAtomic_RenameErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "status.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(dir, "status.json", []byte("x")); err == nil {
		t.Fatal("writeFileAtomic accepted a target path occupied by a directory")
	} else if !strings.Contains(err.Error(), "rename") {
		t.Fatalf("err = %v, want the rename wrap", err)
	}
}

// TestSyncHealth_NoteSuccessAfterStoppedIsANoOp pins noteSuccess's terminal
// guard: once stopped, a late-arriving success must not resurrect the
// state or the notice.
func TestSyncHealth_NoteSuccessAfterStoppedIsANoOp(t *testing.T) {
	h := &syncHealth{state: SyncStateStopped}
	if got := h.noteSuccess(0); got != "" {
		t.Errorf("noteSuccess after stopped = %q, want empty (no notice)", got)
	}
	if h.state != SyncStateStopped {
		t.Errorf("state = %v, want it to stay Stopped", h.state)
	}
}

// TestSyncHealth_RecordWithNoWriterIsANoOp pins record's nil-write guard
// directly: a health tracker with no file writer configured must not panic.
func TestSyncHealth_RecordWithNoWriterIsANoOp(t *testing.T) {
	h := &syncHealth{}
	h.record("test", 0) // must not panic on a nil h.write
}
