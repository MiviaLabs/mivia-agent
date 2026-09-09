package chatsync

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFailedRebaseIsTerminalForTheOutbox pins the contract documented on
// rewriteEventsFileLocked: once a rebase fails, THIS Outbox value is dead, and
// it must stay dead in the loud way - every later Append returns an error, and
// none of them panics or silently reports success.
//
// The state is reachable. rebaseLocked writes the new cursor BEFORE renumbering
// the file, deliberately, because a rebased file under the old higher cursor
// reads as fully flushed and loses events silently. rewriteEventsFileLocked
// then closes and nils eventsFile before it can know the rewrite will succeed,
// so every failure between those two points leaves the cursor already moved to
// the new base while the file on disk still carries the OLD numbering.
//
// Reopening that file would make Append work again on a file whose seqs the
// cursor now contradicts, which is the cursor-versus-file mismatch class
// be8118d7 fixed in the open path - a silent, durable wrong answer. A hard
// error is the correct outcome here; the caller reports it. What must never
// happen is a panic, which would take the host process down over a disk error
// the callers already handle.
//
// The forced failure is a DIRECTORY at the events tmp path: the O_CREATE|
// O_WRONLY open in rewriteEventsFileLocked cannot open a directory for writing,
// so it fails after eventsFile has already been closed and nilled.
func TestFailedRebaseIsTerminalForTheOutbox(t *testing.T) {
	dir := t.TempDir()
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	t.Cleanup(func() { _ = ob.Close() })

	if err := ob.Append(WireEvent{Seq: 1, Type: "turn.start", Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("Append before rebase: %v", err)
	}

	// Block the rewrite at its first write step, after eventsFile is gone.
	if err := os.Mkdir(filepath.Join(dir, eventsFileName+".tmp"), 0o700); err != nil {
		t.Fatalf("seed blocking tmp directory: %v", err)
	}

	if _, err := ob.Rebase(0); err == nil {
		t.Fatal("Rebase() error = nil, want a failure; the tmp path is a directory and cannot be opened for writing, so this test would prove nothing about the failed-rebase state")
	}

	// The pin. Two appends, because a state that errors once and then panics
	// or succeeds is exactly the silent regression this test exists to catch.
	for i := 1; i <= 2; i++ {
		err := ob.Append(WireEvent{Seq: int64(i), Type: "turn.end", Payload: []byte(`{}`)})
		if err == nil {
			t.Fatalf("Append #%d after a failed rebase: error = nil, want a failure; the events file is closed and the cursor has already moved to the new base, so a silent success would durably write events under a numbering the cursor contradicts", i)
		}
	}
}
