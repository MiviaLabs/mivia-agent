package chatsync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCursorFile seeds a durable cursor the way a healthy prior run would
// have left one, without going through Outbox - the repair path this test
// covers runs BEFORE loadCursorAndCount, so the fixture must exist on disk
// before OpenOutbox ever touches it.
func writeCursorFile(t *testing.T, dir string, flushedSeq int64) {
	t.Helper()
	data, err := json.Marshal(Cursor{FlushedSeq: flushedSeq, FlushedAt: time.Now()})
	if err != nil {
		t.Fatalf("marshal cursor fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, cursorFileName), data, 0o600); err != nil {
		t.Fatalf("write cursor fixture: %v", err)
	}
}

// TestOpenOutbox_RepairClampsCursorPastMaxSeq covers a silent-data-loss defect
// in repairEventsFile: it truncates events.jsonl on RECORD STRUCTURE alone,
// before loadCursorAndCount ever runs, and nothing afterwards restores the
// invariant that made the cursor meaningful - FlushedSeq <= MaxSeq.
//
// Most reachable form: the FIRST record on disk is unparsable (a torn write
// at process start, or a corrupted sector), so repair empties the file down to
// seq 0 while cursor.json still claims 4 events flushed from a healthy prior
// run. A fresh or reset remote session reports ServerSeq=0, so openingSeq's
// rebase never fires (it only triggers when MaxSeq() > ServerSeq). The
// projector then starts at seq 1, and every newly appended event has
// seq <= 4 - the cursor's own stale watermark - so unflushedEventsLocked
// filters ALL of them out as "already flushed". They sit durably on disk,
// correctly ordered, correctly fsynced, and invisible to every flush for the
// life of the outbox. Nothing errors; sync's own "chat sync is running"
// notice (8b0579a7) actively says the opposite of what is true.
//
// The fix clamps cursor.FlushedSeq down to MaxSeq() whenever repair (or any
// other path) leaves it ahead, and persists the clamp durably so a second
// restart does not have to rediscover it from the same stale file.
func TestOpenOutbox_RepairClampsCursorPastMaxSeq(t *testing.T) {
	dir := t.TempDir()

	// A healthy prior run flushed seqs 1..4 and recorded it durably.
	writeCursorFile(t, dir, 4)

	// The next run's FIRST write tore: the file holds nothing parsable at all.
	// This is the most reachable form scanGoodPrefix will hand back an empty
	// good-prefix for, but the invariant it breaks is general to any repair
	// that truncates below the cursor's watermark.
	path := filepath.Join(dir, eventsFileName)
	if err := os.WriteFile(path, []byte("not json\n"), 0o600); err != nil {
		t.Fatalf("seed torn events file: %v", err)
	}

	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox on a repaired-to-empty file: %v", err)
	}
	defer ob.Close()

	if got := ob.MaxSeq(); got != 0 {
		t.Fatalf("MaxSeq() = %d after repair emptied the file, want 0", got)
	}
	if got := ob.Cursor().FlushedSeq; got != 0 {
		t.Fatalf("Cursor().FlushedSeq = %d after repair discarded everything past it, want 0 (clamped to MaxSeq); "+
			"a stale watermark above MaxSeq makes every newly appended event with seq <= %d silently invisible to the flusher forever", got, got)
	}

	// The regression, stated as behaviour rather than as internal state: a
	// session that reopens under this exact condition and appends fresh
	// events must see them in UnflushedEvents, not silently drop them.
	events := []WireEvent{
		{Seq: 1, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Text: "hi"}},
		{Seq: 2, Type: TypeTurnEnded, Payload: &TurnEndedPayload{Reason: "completed"}},
	}
	if err := ob.Append(events...); err != nil {
		t.Fatalf("Append after repair: %v", err)
	}
	got, err := ob.UnflushedEvents()
	if err != nil {
		t.Fatalf("UnflushedEvents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("UnflushedEvents returned %d events, want 2 (both just appended); "+
			"a stale cursor watermark from before the repair is filtering them out silently", len(got))
	}
}
