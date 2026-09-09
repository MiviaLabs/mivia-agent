package chatsync

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// failSyncFor makes every fsync on a file whose name ends with suffix fail,
// and leaves every other fsync real. The returned func restores the seam, and
// the seam is restored at the end of the test in any case.
func failSyncFor(t *testing.T, suffix string) func() {
	t.Helper()
	prev := outboxSyncFile
	outboxSyncFile = func(f *os.File) error {
		if strings.HasSuffix(f.Name(), suffix) {
			return os.ErrInvalid
		}
		return prev(f)
	}
	t.Cleanup(func() { outboxSyncFile = prev })
	return func() { outboxSyncFile = prev }
}

// failTruncateFor makes every truncate on a file whose name ends with suffix
// fail WITHOUT truncating. That is the durable state a truncate whose fsync
// never reached the disk leaves behind after a power loss: the bytes past the
// mark are still readable. The returned func restores the seam.
func failTruncateFor(t *testing.T, suffix string) func() {
	t.Helper()
	prev := outboxTruncateFile
	outboxTruncateFile = func(f *os.File, size int64) error {
		if strings.HasSuffix(f.Name(), suffix) {
			return os.ErrInvalid
		}
		return prev(f, size)
	}
	t.Cleanup(func() { outboxTruncateFile = prev })
	return func() { outboxTruncateFile = prev }
}

// assertContiguousOnDisk reopens the outbox at dir and fails unless every
// stored event carries a seq exactly one above the record before it. A
// duplicate or a gap here is terminal: the API rejects any append whose first
// seq is not serverLastSeq+1, so one bad record wedges the session for good.
func assertContiguousOnDisk(t *testing.T, dir string) []StoredEvent {
	t.Helper()

	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("reopen outbox: %v", err)
	}
	defer ob.Close()

	events, err := ob.UnflushedEvents()
	if err != nil {
		t.Fatalf("UnflushedEvents after reopen: %v", err)
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq != events[i-1].Seq+1 {
			t.Fatalf("events.jsonl is not contiguous: record %d has seq %d after seq %d (all seqs: %v)",
				i, events[i].Seq, events[i-1].Seq, seqsOf(events))
		}
	}
	return events
}

func seqsOf(events []StoredEvent) []int64 {
	out := make([]int64, 0, len(events))
	for _, se := range events {
		out = append(out, se.Seq)
	}
	return out
}

// TestOutboxAppend_DoubleFaultLeavesNoDuplicateSeq covers the double fault:
// the batch fsync fails AND the rollback truncate fails. Append reports both
// errors and moves no counter, so the caller rolls the seq counter back and
// reissues the same seqs - but the rolled-back bytes are still on disk. The
// reissued batch then gives the file two records for one seq, which the
// server's contiguity check rejects for the life of the session.
func TestOutboxAppend_DoubleFaultLeavesNoDuplicateSeq(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox-double-fault")
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}

	restoreSync := failSyncFor(t, eventsFileName)
	restoreTruncate := failTruncateFor(t, eventsFileName)
	if err := ob.Append(goodBatch()...); err == nil {
		t.Fatal("Append returned nil with both the batch fsync and the rollback truncate failing; the double fault is the premise of this test")
	}
	restoreSync()
	restoreTruncate()

	// The seams are real again from here: the disk recovered, and the caller
	// reissues the seqs Append reported as never stored.
	if err := ob.Append(goodBatch()...); err != nil {
		t.Fatalf("Append of the reissued batch: %v", err)
	}

	// The live file must already be clean. Recovery on the next open is the
	// backstop, not the licence to write a duplicate now: this process keeps
	// flushing from this file, and the server rejects the duplicate long
	// before anything reopens the outbox.
	live, err := ob.UnflushedEvents()
	if err != nil {
		t.Fatalf("UnflushedEvents before reopen: %v", err)
	}
	for i := 1; i < len(live); i++ {
		if live[i].Seq != live[i-1].Seq+1 {
			t.Fatalf("the live events file is not contiguous after the reissue: seqs %v", seqsOf(live))
		}
	}

	if err := ob.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events := assertContiguousOnDisk(t, dir)
	if len(events) != 3 {
		t.Errorf("events.jsonl holds %d records after the reissue, want 3 (seqs %v)", len(events), seqsOf(events))
	}
}

// TestOutboxAppend_DoubleFaultThenCrashLeavesNoDuplicateSeq is the same double
// fault with no chance to repair in process: the outbox is closed while the
// rollback is still owed, which is what a crash between the two fsyncs leaves.
// The reopened file must still be contiguous.
func TestOutboxAppend_DoubleFaultThenCrashLeavesNoDuplicateSeq(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox-double-fault-crash")
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}

	restoreSync := failSyncFor(t, eventsFileName)
	restoreTruncate := failTruncateFor(t, eventsFileName)
	if err := ob.Append(goodBatch()...); err == nil {
		t.Fatal("Append returned nil with both the batch fsync and the rollback truncate failing; the double fault is the premise of this test")
	}
	_ = ob.Close()
	restoreSync()
	restoreTruncate()

	assertContiguousOnDisk(t, dir)
}

// TestOpenOutbox_RepairsTornAndDuplicatedTail proves the recovery path itself.
// A failed fsync makes the durable tail arbitrary: it can hold a half-written
// record, or records a later run already reissued. OpenOutbox must drop that
// tail rather than hand a duplicate or a torn line to the caller.
func TestOpenOutbox_RepairsTornAndDuplicatedTail(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantSeqs []int64
	}{
		{
			name:     "duplicate tail",
			content:  line(1) + line(2) + line(3) + line(2) + line(3),
			wantSeqs: []int64{1, 2, 3},
		},
		{
			name:     "gap in tail",
			content:  line(1) + line(2) + line(7),
			wantSeqs: []int64{1, 2},
		},
		{
			name:     "torn last record",
			content:  line(1) + line(2) + `{"seq":3,"type":"turn`,
			wantSeqs: []int64{1, 2},
		},
		{
			name:     "unparsable last record",
			content:  line(1) + line(2) + "not json\n",
			wantSeqs: []int64{1, 2},
		},
		{
			name:     "healthy file is untouched",
			content:  line(1) + line(2) + line(3),
			wantSeqs: []int64{1, 2, 3},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, eventsFileName)
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("seed events file: %v", err)
			}

			ob, err := OpenOutbox(dir, 100)
			if err != nil {
				t.Fatalf("OpenOutbox on a damaged tail: %v", err)
			}
			defer ob.Close()

			events, err := ob.UnflushedEvents()
			if err != nil {
				t.Fatalf("UnflushedEvents: %v", err)
			}
			got := seqsOf(events)
			if len(got) != len(tc.wantSeqs) {
				t.Fatalf("recovered seqs = %v, want %v", got, tc.wantSeqs)
			}
			for i := range got {
				if got[i] != tc.wantSeqs[i] {
					t.Fatalf("recovered seqs = %v, want %v", got, tc.wantSeqs)
				}
			}

			// The repair must be durable, not a read-time filter.
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read events file: %v", err)
			}
			if want := len(tc.wantSeqs); strings.Count(string(raw), "\n") != want {
				t.Errorf("events.jsonl still holds %d lines on disk, want %d; content: %q",
					strings.Count(string(raw), "\n"), want, raw)
			}
		})
	}
}

func line(seq int64) string {
	return `{"seq":` + strconv.FormatInt(seq, 10) + `,"type":"turn.started","payload":{"text":"x"}}` + "\n"
}

// TestTruncateToMarkLocked_SyncErrorSurfaces pins truncateToMarkLocked's
// own outboxSyncFile error wrap, distinct from its truncate-error wrap
// TestOutboxAppend_DoubleFaultLeavesNoDuplicateSeq already covers.
func TestTruncateToMarkLocked_SyncErrorSurfaces(t *testing.T) {
	ob, _ := openTestOutbox(t)
	restore := failSyncFor(t, eventsFileName)
	defer restore()
	if err := ob.truncateToMarkLocked(0); err == nil {
		t.Fatal("truncateToMarkLocked hid a sync failure")
	}
}

// TestRepairEventsFile_OpenErrorSurfaces pins repairEventsFile's own
// non-ErrNotExist open-error wrap. os.Open on a directory (the earlier
// attempt) succeeds - opening a directory does not itself fail - so the
// precondition here instead denies traversal into the parent directory
// entirely (chmod 0000), which makes os.Open fail with a permission
// error rather than ErrNotExist.
func TestRepairEventsFile_OpenErrorSurfaces(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "events-dir")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, eventsFileName), []byte("{\"seq\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, probeErr := os.Open(filepath.Join(dir, eventsFileName)); probeErr == nil {
		t.Skip("platform still allows traversal into a 0000 directory")
	}
	if err := repairEventsFile(dir); err == nil {
		t.Fatal("repairEventsFile hid a non-ErrNotExist open failure")
	}
}

// TestRepairEventsFile_MissingFileIsANoop pins the os.IsNotExist branch:
// no events file at all is healthy, not an error.
func TestRepairEventsFile_MissingFileIsANoop(t *testing.T) {
	if err := repairEventsFile(t.TempDir()); err != nil {
		t.Fatalf("repairEventsFile() = %v, want nil for a missing events file", err)
	}
}

// TestScanGoodPrefix_StatErrorSurfaces pins scanGoodPrefix's own Stat
// error wrap, forced by closing the file's fd out from under it before
// the scan runs.
func TestScanGoodPrefix_StatErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFileName)
	if err := os.WriteFile(path, []byte(`{"seq":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := scanGoodPrefix(f); err == nil {
		t.Fatal("scanGoodPrefix hid a Stat failure on a closed file")
	}
}

// TestTruncateEventsFileTo_OpenErrorSurfaces pins truncateEventsFileTo's
// own open-error wrap.
func TestTruncateEventsFileTo_OpenErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, eventsFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := truncateEventsFileTo(filepath.Join(dir, eventsFileName), 0); err == nil {
		t.Fatal("truncateEventsFileTo hid an open failure")
	}
}

// TestTruncateEventsFileTo_TruncateErrorSurfaces pins truncateEventsFileTo's
// own outboxTruncateFile error wrap, distinct from its open-error and
// sync-error wraps.
func TestTruncateEventsFileTo_TruncateErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFileName)
	if err := os.WriteFile(path, []byte(`{"seq":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restore := failTruncateFor(t, eventsFileName)
	defer restore()
	if err := truncateEventsFileTo(path, 0); err == nil {
		t.Fatal("truncateEventsFileTo hid a truncate failure")
	}
}

// TestTruncateEventsFileTo_SyncErrorSurfaces pins truncateEventsFileTo's
// own outboxSyncFile error wrap, distinct from its truncate-error wrap.
func TestTruncateEventsFileTo_SyncErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFileName)
	if err := os.WriteFile(path, []byte(`{"seq":1}`+"\n{"+`"seq":2}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restore := failSyncFor(t, eventsFileName)
	defer restore()
	if err := truncateEventsFileTo(path, 0); err == nil {
		t.Fatal("truncateEventsFileTo hid a sync failure")
	}
}
