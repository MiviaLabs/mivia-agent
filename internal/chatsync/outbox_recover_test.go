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
