package chatsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The rebase failure paths, one per step of rewriteEventsFileLocked.
//
// Rebase is the only operation that renumbers a durable file. Its ordering is
// deliberate and load-bearing (see the comments in rebaseLocked and
// rewriteEventsFileLocked): the cursor is written FIRST, so a failure after
// that point leaves a cursor the file on disk contradicts, and the outbox is
// then poisoned on purpose - eventsFile stays nil and every later Append
// fails - rather than resurrected onto a file whose numbering the cursor now
// disagrees with.
//
// TestFailedRebaseIsTerminalForTheOutbox pins that contract for a failure at
// the FIRST step (the tmp open). Every step after it returns through its own
// error branch, and each one has to end in the same state: an error naming
// the step, a dead outbox, and no silent success. Those branches never ran,
// so nothing stopped one of them from reopening the file or swallowing the
// error.
//
// The failure BEFORE the cursor write is the opposite contract and is tested
// first: it must leave the outbox untouched and still writable, because
// nothing durable has moved yet.

// seedOutboxWithEvents opens an outbox at a fresh dir and appends n events.
func seedOutboxWithEvents(t *testing.T, n int) (*Outbox, string) {
	t.Helper()
	dir := t.TempDir()
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	t.Cleanup(func() { _ = ob.Close() })
	for i := 1; i <= n; i++ {
		if err := ob.Append(WireEvent{Seq: int64(i), Type: "turn.start", Payload: []byte(`{}`)}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	return ob, dir
}

// assertRebaseLeftTheOutboxDead states the shared post-condition of every
// failure that happens AFTER the cursor write.
func assertRebaseLeftTheOutboxDead(t *testing.T, ob *Outbox, err error, wantStep string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Rebase() error = nil, want a failure at %q; the test proves nothing if the step it sabotaged still succeeded", wantStep)
	}
	if !strings.Contains(err.Error(), wantStep) {
		t.Errorf("Rebase() error = %q, want it to name the step %q so the caller can report which one failed", err, wantStep)
	}
	if !ob.Dead() {
		t.Errorf("Dead() = false after a rebase failed at %q; the cursor has already moved to the new base, "+
			"so a reopened events file would take appends under a numbering the cursor contradicts", wantStep)
	}
	if appendErr := ob.Append(WireEvent{Seq: 1, Type: "turn.end", Payload: []byte(`{}`)}); appendErr == nil {
		t.Errorf("Append after a rebase failed at %q returned nil; a failed rebase must be loud and permanent, not silent", wantStep)
	}
}

// TestRebaseThatCannotReadTheOutboxChangesNothing pins the ONE failure that
// must NOT poison the outbox. unflushedEventsLocked runs before the cursor
// write and before the events file is closed, so a read error there leaves
// every durable byte exactly as it was. Treating it like a post-cursor
// failure would kill a live session over a transient read.
func TestRebaseThatCannotReadTheOutboxChangesNothing(t *testing.T) {
	ob, dir := seedOutboxWithEvents(t, 2)
	before := ob.Cursor()

	// A truncated final record: the file is readable, one line is not JSON.
	f, err := os.OpenFile(filepath.Join(dir, eventsFileName), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open events file: %v", err)
	}
	if _, err := f.WriteString("{\"seq\":3,\"type\":\n"); err != nil {
		t.Fatalf("write partial record: %v", err)
	}
	_ = f.Close()

	n, err := ob.Rebase(7)
	if err == nil {
		t.Fatal("Rebase() error = nil over an unparseable events file; renumbering would then drop the records it could not read")
	}
	if n != 0 {
		t.Errorf("Rebase() re-indexed %d events on failure, want 0", n)
	}
	if got := ob.Cursor(); got.FlushedSeq != before.FlushedSeq {
		t.Errorf("cursor moved to %d on a read failure, want it left at %d: nothing durable was rewritten, "+
			"so a moved cursor would mark unflushed events as already sent", got.FlushedSeq, before.FlushedSeq)
	}
	if ob.Dead() {
		t.Error("Dead() = true after a read failure; the events file was never closed and the outbox must stay usable")
	}
	if err := ob.Append(WireEvent{Seq: 3, Type: "turn.end", Payload: []byte(`{}`)}); err != nil {
		t.Errorf("Append after a failed read: %v, want it to still work", err)
	}
}

// TestRebaseThatCannotWriteTheRenumberedFileIsTerminal drives the per-event
// write failure. The tmp path is a symlink to /dev/full, so the open succeeds
// - eventsFile is already closed and nilled by then - and the first Write
// returns ENOSPC, which is exactly what a full disk does mid-rebase.
func TestRebaseThatCannotWriteTheRenumberedFileIsTerminal(t *testing.T) {
	requireDevFull(t)
	ob, dir := seedOutboxWithEvents(t, 2)

	if err := os.Symlink("/dev/full", filepath.Join(dir, eventsFileName+".tmp")); err != nil {
		t.Fatalf("symlink tmp events path at /dev/full: %v", err)
	}

	_, err := ob.Rebase(0)
	assertRebaseLeftTheOutboxDead(t, ob, err, "write rebased event")
}

// TestRebaseThatCannotSwapTheFileInIsTerminal drives the rename failure. The
// directory is made read-only from inside the fsync seam - the last point
// rewriteEventsFileLocked passes before the rename - so every earlier step
// runs for real and only os.Rename is refused.
func TestRebaseThatCannotSwapTheFileInIsTerminal(t *testing.T) {
	requireNonRoot(t)
	ob, dir := seedOutboxWithEvents(t, 2)

	prev := outboxSyncFile
	outboxSyncFile = func(f *os.File) error {
		if strings.HasSuffix(f.Name(), eventsFileName+".tmp") {
			if err := os.Chmod(dir, 0o500); err != nil {
				t.Errorf("chmod outbox dir read-only: %v", err)
			}
		}
		return prev(f)
	}
	t.Cleanup(func() {
		outboxSyncFile = prev
		_ = os.Chmod(dir, 0o700)
	})

	_, err := ob.Rebase(0)
	assertRebaseLeftTheOutboxDead(t, ob, err, "rename events file")
}

// TestRebaseThatCannotReopenTheEventsFileIsTerminal drives the last branch.
// The tmp path is a symlink to a write-only file, so every step through the
// rename succeeds - the rename moves the SYMLINK, so events.jsonl now
// resolves to that same write-only file - and only the O_RDWR reopen is
// refused.
//
// This is the branch where a "helpful" fallback would be most tempting and
// most wrong: the renumbered file is already in place, so a reopen failure
// that was swallowed would leave a live outbox appending old-numbered events
// onto a rebased file.
func TestRebaseThatCannotReopenTheEventsFileIsTerminal(t *testing.T) {
	requireNonRoot(t)
	ob, dir := seedOutboxWithEvents(t, 2)

	target := filepath.Join(dir, "write-only-target")
	if err := os.WriteFile(target, nil, 0o222); err != nil {
		t.Fatalf("seed write-only rebase target: %v", err)
	}
	// os.WriteFile applies the process umask, so set the mode explicitly.
	if err := os.Chmod(target, 0o222); err != nil {
		t.Fatalf("chmod write-only rebase target: %v", err)
	}
	if f, err := os.OpenFile(target, os.O_RDWR, 0); err == nil {
		_ = f.Close()
		t.Skip("this filesystem or user ignores the write-only mode, so the reopen would succeed")
	}
	if err := os.Symlink(target, filepath.Join(dir, eventsFileName+".tmp")); err != nil {
		t.Fatalf("symlink tmp events path at the write-only target: %v", err)
	}

	_, err := ob.Rebase(0)
	assertRebaseLeftTheOutboxDead(t, ob, err, "reopen events file")
}

// TestRebaseThatCannotCloseTheRenumberedFileIsTerminal drives the branch
// between the sync and the rename.
//
// close(2) on a written file is where a delayed-allocation or quota error
// surfaces on real filesystems: the bytes were accepted by write(2) and only
// the flush at close reports that they never landed. Here the fsync seam
// closes the descriptor itself, so rewriteEventsFileLocked's own Close returns
// a genuine error at exactly that point.
//
// The state it leaves is the worst of the set and the reason the whole
// operation is terminal: the cursor is already at the new base, the rename
// never ran, so events.jsonl still carries the OLD numbering. Every event in
// it is at or below the new cursor and reads as flushed. An outbox that
// survived this would silently report a full backlog as already sent.
func TestRebaseThatCannotCloseTheRenumberedFileIsTerminal(t *testing.T) {
	ob, dir := seedOutboxWithEvents(t, 2)
	before, err := os.ReadFile(filepath.Join(dir, eventsFileName))
	if err != nil {
		t.Fatalf("read events file: %v", err)
	}

	prev := outboxSyncFile
	outboxSyncFile = func(f *os.File) error {
		if strings.HasSuffix(f.Name(), eventsFileName+".tmp") {
			// Sync for real, then take the descriptor away, so the Close that
			// follows fails the way a flush-at-close failure does.
			syncErr := prev(f)
			_ = f.Close()
			return syncErr
		}
		return prev(f)
	}
	t.Cleanup(func() { outboxSyncFile = prev })

	_, rebaseErr := ob.Rebase(9)
	assertRebaseLeftTheOutboxDead(t, ob, rebaseErr, "close tmp events file")

	after, err := os.ReadFile(filepath.Join(dir, eventsFileName))
	if err != nil {
		t.Fatalf("read events file after the failure: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("events.jsonl changed although the rename never ran: before %q, after %q", before, after)
	}
	if got := ob.Cursor().FlushedSeq; got != 9 {
		t.Fatalf("cursor = %d, want 9: the cursor is written before the rewrite, which is exactly why "+
			"the outbox must stay dead - every event still on disk now reads as flushed", got)
	}
}

func requireNonRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the permission bits these failures are built from")
	}
}

func requireDevFull(t *testing.T) {
	t.Helper()
	f, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("/dev/full is not writable here: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write([]byte("x")); err == nil {
		t.Skip("/dev/full accepted a write, so it cannot stand in for a full disk")
	}
}
