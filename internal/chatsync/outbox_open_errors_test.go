package chatsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenOutbox_MkdirFailsWhenParentIsAFile pins OpenOutbox's first guard:
// a dir path whose parent component is a regular file cannot be created,
// and MkdirAll's error must surface wrapped, not swallowed.
func TestOpenOutbox_MkdirFailsWhenParentIsAFile(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOutbox(filepath.Join(blocker, "outbox"), 10); err == nil {
		t.Fatal("OpenOutbox accepted a dir path with a file for a parent")
	} else if !strings.Contains(err.Error(), "create outbox dir") {
		t.Fatalf("err = %v, want the create-dir wrap", err)
	}
}

// TestOpenOutbox_AcquireLockFailsOnReadOnlyDir pins acquireLock's own guard:
// a dir that already exists but refuses new files cannot create the lock
// file, and the error must surface as ErrOutboxLocked - the same sentinel a
// genuinely-held lock reports, since a caller cannot open the outbox either
// way.
func TestOpenOutbox_AcquireLockFailsOnReadOnlyDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	probe := filepath.Join(dir, "writability-probe")
	if f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
		_ = os.Remove(probe)
		t.Skip("platform still creates files in a read-only directory")
	}
	if _, err := OpenOutbox(dir, 10); err == nil {
		t.Fatal("OpenOutbox accepted a directory it cannot write a lock file into")
	}
}

// TestOpenOutbox_RepairErrorSurfacesAndReleasesLock pins the repair-failure
// path: repairEventsFile answering anything other than ErrNotExist must
// abort OpenOutbox with the lock released (a leaked lock would strand every
// later open attempt behind ErrOutboxLocked forever). Making events.jsonl a
// directory forces os.Open's read-for-repair to fail with a non-ErrNotExist
// error without needing any OS-specific permission trick.
func TestOpenOutbox_RepairErrorSurfacesAndReleasesLock(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, eventsFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOutbox(dir, 10); err == nil {
		t.Fatal("OpenOutbox accepted an events.jsonl that is actually a directory")
	}
	// The lock must have been released: a second open attempt fails on the
	// same repair error, never on ErrOutboxLocked.
	_, err := OpenOutbox(dir, 10)
	if err == nil || err == ErrOutboxLocked {
		t.Fatalf("second OpenOutbox = %v, want the repair error again, not a leaked lock", err)
	}
}

// TestOpenOutbox_LoadCursorErrorSurfacesAndCloses pins loadCursorAndCount's
// propagation through OpenOutbox: an error there must close the
// partially-opened outbox (events file and lock) rather than leaking either.
// Making cursor.json a directory forces os.ReadFile to fail with a
// non-ErrNotExist error, exercising both loadCursorAndCount's own guard and
// OpenOutbox's wrapping Close call in one fixture.
func TestOpenOutbox_LoadCursorErrorSurfacesAndCloses(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, cursorFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOutbox(dir, 10); err == nil {
		t.Fatal("OpenOutbox accepted a cursor.json that is actually a directory")
	}
	// The lock and events file must have been released by Close: a second
	// attempt reaches the very same cursor error again, not ErrOutboxLocked.
	_, err := OpenOutbox(dir, 10)
	if err == nil || err == ErrOutboxLocked {
		t.Fatalf("second OpenOutbox = %v, want the cursor error again, not a leaked lock", err)
	}
}
