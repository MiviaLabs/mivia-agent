package chatsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestOutbox(t *testing.T) (*Outbox, string) {
	t.Helper()
	dir := t.TempDir()
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	t.Cleanup(func() { _ = ob.Close() })
	return ob, dir
}

// TestOutboxAppend_EmptyBatchIsANoOp pins Append's zero-event early return:
// no seek, no write, no counter change.
func TestOutboxAppend_EmptyBatchIsANoOp(t *testing.T) {
	ob, _ := openTestOutbox(t)
	if err := ob.Append(); err != nil {
		t.Fatalf("Append() with no events = %v, want nil", err)
	}
	if ob.UnflushedCount() != 0 {
		t.Fatalf("UnflushedCount = %d, want 0", ob.UnflushedCount())
	}
}

// TestOutboxAppend_PropagatesPendingTruncateError pins Append's first guard:
// a rollback the outbox still owes from a prior failed batch must refuse
// every later Append, and the failing truncate's error must surface.
func TestOutboxAppend_PropagatesPendingTruncateError(t *testing.T) {
	ob, _ := openTestOutbox(t)
	ob.hasPendingTrunc = true
	ob.pendingTrunc = 0
	prev := outboxTruncateFile
	outboxTruncateFile = func(*os.File, int64) error { return os.ErrInvalid }
	t.Cleanup(func() { outboxTruncateFile = prev })
	if err := ob.Append(WireEvent{Seq: 1}); err == nil {
		t.Fatal("Append succeeded despite an owed rollback that cannot be paid")
	}
}

// TestOutboxAdvanceCursor_WriteCursorErrorSurfaces pins AdvanceCursor's
// propagation of writeCursorLocked's failure: a read-only outbox directory
// (after the lock and events files already exist) cannot atomically replace
// cursor.json, and AdvanceCursor must report that rather than silently
// keeping the stale cursor.
func TestOutboxAdvanceCursor_WriteCursorErrorSurfaces(t *testing.T) {
	ob, dir := openTestOutbox(t)
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
	if err := ob.AdvanceCursor(1); err == nil {
		t.Fatal("AdvanceCursor succeeded despite a read-only outbox directory")
	}
}

// TestOutboxWriteCursorLocked_MarshalErrorSurfaces pins the marshal guard
// directly: a FlushedAt year outside [0,9999] is the one value
// encoding/json's time.Time.MarshalJSON refuses (RFC 3339 has no room for a
// 5+ digit or negative year), so this is the one way to make json.Marshal
// itself fail on a Cursor without corrupting unrelated state.
func TestOutboxWriteCursorLocked_MarshalErrorSurfaces(t *testing.T) {
	ob, _ := openTestOutbox(t)
	bad := Cursor{FlushedSeq: 1, FlushedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := ob.writeCursorLocked(bad); err == nil {
		t.Fatal("writeCursorLocked accepted a FlushedAt outside RFC 3339's year range")
	} else if !strings.Contains(err.Error(), "marshal cursor") {
		t.Fatalf("err = %v, want the marshal wrap", err)
	}
}

// TestOutboxUnflushedEvents_NonNotExistOpenErrorSurfaces pins
// unflushedEventsLocked's open guard: once the outbox is live, replacing
// events.jsonl with a directory is not a "file missing" condition and must
// return the raw open error, not an empty read.
func TestOutboxUnflushedEvents_NonNotExistOpenErrorSurfaces(t *testing.T) {
	ob, dir := openTestOutbox(t)
	if err := ob.Close(); err != nil {
		t.Fatal(err)
	}
	// os.Open SUCCEEDS on a directory - only a later read fails - so a
	// directory-in-place-of-the-file precondition does not reach
	// unflushedEventsLocked's own open-error branch (it instead fails one
	// step later, inside the scan). Denying traversal into the whole
	// events-file's parent directory does fail at os.Open itself.
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	eventsPath := filepath.Join(dir, eventsFileName)
	if _, probeErr := os.Open(eventsPath); probeErr == nil {
		t.Skip("platform still allows traversal into a 0000 directory")
	}
	if _, err := ob.UnflushedEvents(); err == nil {
		t.Fatal("UnflushedEvents accepted a non-ErrNotExist open failure")
	}
}

// TestOutboxUnflushedEvents_BlankLineSkippedAndUnparsableErrors pins the
// scan loop's two remaining branches in one pass: a blank line between
// records is skipped rather than treated as a parse failure, and a
// genuinely malformed record after it still fails the whole read - a
// half-good result would let a caller silently drop events past the bad
// line.
func TestOutboxUnflushedEvents_BlankLineSkippedAndUnparsableErrors(t *testing.T) {
	ob, dir := openTestOutbox(t)
	if err := ob.Close(); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(dir, eventsFileName)
	raw := "\n" + `{"seq":1,"type":"turn_start","payload":{}}` + "\nnot json\n"
	if err := os.WriteFile(eventsPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ob.UnflushedEvents(); err == nil {
		t.Fatal("UnflushedEvents accepted a malformed record")
	} else if !strings.Contains(err.Error(), "unmarshal stored event") {
		t.Fatalf("err = %v, want the unmarshal wrap", err)
	}
}

// TestOutboxUnflushedEvents_ScanErrorSurfaces pins the scanner.Err() branch:
// a single line past the scanner's 1MiB buffer cap is a genuine scan
// failure (bufio.ErrTooLong), not a parse failure on a truncated read.
func TestOutboxUnflushedEvents_ScanErrorSurfaces(t *testing.T) {
	ob, dir := openTestOutbox(t)
	if err := ob.Close(); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(dir, eventsFileName)
	oversized := strings.Repeat("a", 2*1024*1024)
	if err := os.WriteFile(eventsPath, []byte(oversized), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ob.UnflushedEvents(); err == nil {
		t.Fatal("UnflushedEvents accepted a line past the scanner buffer cap")
	} else if !strings.Contains(err.Error(), "scan events") {
		t.Fatalf("err = %v, want the scan wrap", err)
	}
}

// TestOutboxCountUnflushedFromDisk_MissingFileIsZero pins the direct,
// pre-creation shape of countUnflushedFromDisk's own not-exist branch. Every
// OpenOutbox call creates events.jsonl before this ever runs, so the branch
// is unreachable through the public API and is exercised here by calling
// the unexported method on a bare Outbox value over an empty directory.
func TestOutboxCountUnflushedFromDisk_MissingFileIsZero(t *testing.T) {
	ob := &Outbox{dir: t.TempDir()}
	n, err := ob.countUnflushedFromDisk()
	if err != nil {
		t.Fatalf("countUnflushedFromDisk on a missing file: %v", err)
	}
	if n != 0 {
		t.Fatalf("count = %d, want 0", n)
	}
}

// TestOutboxCountUnflushedFromDisk_BlankLineSkippedAndUnparsableErrors
// mirrors TestOutboxUnflushedEvents_BlankLineSkippedAndUnparsableErrors for
// countUnflushedFromDisk's own copy of the scan loop.
func TestOutboxCountUnflushedFromDisk_BlankLineSkippedAndUnparsableErrors(t *testing.T) {
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, eventsFileName)
	raw := "\n" + `{"seq":1,"type":"turn_start","payload":{}}` + "\nnot json\n"
	if err := os.WriteFile(eventsPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	ob := &Outbox{dir: dir}
	if _, err := ob.countUnflushedFromDisk(); err == nil {
		t.Fatal("countUnflushedFromDisk accepted a malformed record")
	} else if !strings.Contains(err.Error(), "unmarshal stored event") {
		t.Fatalf("err = %v, want the unmarshal wrap", err)
	}
}

// TestOutboxLoadCursorAndCount_PersistClampErrorSurfaces pins the clamp
// branch's own persist failure, separate from TestOpenOutbox_
// RepairClampsCursorPastMaxSeq (which pins the clamp value and only ever
// exercises the successful persist). Calling loadCursorAndCount directly on
// a read-only directory lets the clamp condition and its failing persist be
// driven without the ordering complexity of OpenOutbox's own lock/events
// creation, which needs a writable directory first.
func TestOutboxLoadCursorAndCount_PersistClampErrorSurfaces(t *testing.T) {
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
	ob := &Outbox{dir: dir, cursor: Cursor{FlushedSeq: 5}, maxSeq: 0}
	if err := ob.loadCursorAndCount(); err == nil {
		t.Fatal("loadCursorAndCount accepted a clamp it could not persist")
	} else if !strings.Contains(err.Error(), "persist clamped cursor") {
		t.Fatalf("err = %v, want the persist-clamp wrap", err)
	}
}

// TestOpenOutbox_EventsFileCreateFailsAfterLockAcquired pins OpenOutbox's
// own guard at the events-file create step: the lock file already exists
// (so acquireLock only opens it - no directory write needed), but the
// directory refuses a genuinely NEW file, so creating events.jsonl fails.
// Isolates this from acquireLock's own failure
// (TestOpenOutbox_AcquireLockFailsOnReadOnlyDir), which never reaches this
// line because the lock file does not exist yet there.
func TestOpenOutbox_EventsFileCreateFailsAfterLockAcquired(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, lockFileName)
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
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
		t.Fatal("OpenOutbox accepted a directory that cannot create events.jsonl")
	} else if !strings.Contains(err.Error(), "open events file") {
		t.Fatalf("err = %v, want the open-events wrap", err)
	}
}

// TestOpenOutbox_CountUnflushedFromDiskErrorSurfaces pins OpenOutbox's own
// wrap of countUnflushedFromDisk's error during init, distinct from
// TestOutboxCountUnflushedFromDisk_BlankLineSkippedAndUnparsableErrors
// (which calls the method directly, bypassing OpenOutbox entirely).
func TestOpenOutbox_CountUnflushedFromDiskErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, eventsFileName)
	// A merely-malformed trailing record is exactly what repairEventsFile
	// (run first, inside OpenOutbox) truncates away before
	// countUnflushedFromDisk ever sees it - so that precondition never
	// reaches this branch. A single VALID record whose line exceeds the
	// 1MiB bufio.Scanner cap countUnflushedFromDisk uses is different:
	// repairEventsFile's own bufio.Reader has no such cap and accepts the
	// line as good (it parses and is contiguous from seq 1), so the file
	// survives repair intact, and only the later Scanner-based count
	// trips bufio.ErrTooLong.
	huge := `{"seq":1,"type":"turn_start","payload":{"text":"` + strings.Repeat("x", 2*1024*1024) + "\"}}\n"
	if err := os.WriteFile(eventsPath, []byte(huge), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOutbox(dir, 10); err == nil {
		t.Fatal("OpenOutbox accepted an events.jsonl with a record past the scan buffer cap")
	}
}

// TestOutboxAdvanceCursor_StaleSeqIsANoop pins AdvanceCursor's own
// stale-seq guard: a seq behind the already-flushed cursor must not
// rewrite cursor.json (a concurrent or reordered ack arriving late).
func TestOutboxAdvanceCursor_StaleSeqIsANoop(t *testing.T) {
	ob, _ := openTestOutbox(t)
	if err := ob.AdvanceCursor(5); err != nil {
		t.Fatal(err)
	}
	before := ob.Cursor()
	if err := ob.AdvanceCursor(1); err != nil {
		t.Fatal(err)
	}
	if after := ob.Cursor(); after != before {
		t.Fatalf("Cursor() = %+v after a stale AdvanceCursor, want unchanged %+v", after, before)
	}
}

// TestOutboxAppend_WriteErrorSurfaces pins writeBatchLocked's own
// ob.eventsFile.Write error wrap. eventsFile is swapped for a read-only
// handle on the SAME path: Seek (which Append does first, to locate the
// append mark) works fine on a read-only file, but the subsequent Write
// fails with EBADF - the one deterministic way to fail Write without
// also failing the Seek immediately before it.
func TestOutboxAppend_WriteErrorSurfaces(t *testing.T) {
	ob, dir := openTestOutbox(t)
	roFile, err := os.OpenFile(filepath.Join(dir, eventsFileName), os.O_RDONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	ob.eventsFile = roFile
	t.Cleanup(func() { _ = roFile.Close() })
	if err := ob.Append(WireEvent{Seq: 1}); err == nil {
		t.Fatal("Append accepted a write on a read-only events file handle")
	} else if !strings.Contains(err.Error(), "write event") {
		t.Fatalf("err = %v, want the write-event wrap", err)
	}
}

// TestOutboxUnflushedEvents_MissingFileIsEmpty mirrors
// TestOutboxCountUnflushedFromDisk_MissingFileIsZero for
// unflushedEventsLocked's own not-exist branch: every OpenOutbox call
// creates events.jsonl before this can run through the public API, so
// it is exercised directly on a bare Outbox value over an empty
// directory.
func TestOutboxUnflushedEvents_MissingFileIsEmpty(t *testing.T) {
	ob := &Outbox{dir: t.TempDir()}
	events, err := ob.UnflushedEvents()
	if err != nil {
		t.Fatalf("UnflushedEvents on a missing file: %v", err)
	}
	if events != nil {
		t.Fatalf("events = %v, want nil", events)
	}
}
