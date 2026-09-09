package chatsync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ResetForFork rewrites the outbox with only the currently unflushed events
// re-indexed starting at sequence 1, and resets the flushed cursor to 0.
// It returns the number of re-indexed unflushed events.
func (ob *Outbox) ResetForFork() (int, error) {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	return ob.rebaseLocked(0)
}

// Rebase re-indexes every unflushed event to start at base+1 and sets the
// flushed cursor to base. It returns the number of re-indexed events.
//
// This is the runtime answer to a sequence-gap 400 when the server is BEHIND
// the outbox: the events between the server's mark and the outbox's first
// unflushed seq are gone, and no resend can produce them, so the only way back
// to a contiguous stream is to renumber onto the server's mark. Forking is the
// same operation with base 0.
func (ob *Outbox) Rebase(base int64) (int, error) {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	return ob.rebaseLocked(base)
}

func (ob *Outbox) rebaseLocked(base int64) (int, error) {
	unflushed, err := ob.unflushedEventsLocked()
	if err != nil {
		return 0, err
	}

	// The cursor lands before the renumbered file, and the order is the whole
	// point. A rebased file under the old, higher cursor reads as fully
	// flushed: every surviving event is skipped, and nothing reports the loss.
	// The reverse leak is harmless - a low cursor over the old file re-admits
	// acknowledged events, which is a resend of a contiguous run, not a gap.
	newCursor := Cursor{
		FlushedSeq: base,
		FlushedAt:  time.Now(),
	}
	if err := ob.writeCursorLocked(newCursor); err != nil {
		return 0, err
	}
	ob.cursor = newCursor

	if err := ob.rewriteEventsFileLocked(unflushed, base); err != nil {
		// Disk and memory agree on the new cursor; only the renumbering is
		// missing. Recount so the capacity guard reflects what is queued now.
		if n, countErr := ob.countUnflushedFromDisk(); countErr == nil {
			ob.unflushed = n
		}
		return 0, err
	}

	ob.unflushed = len(unflushed)
	ob.maxSeq = base + int64(len(unflushed))

	return len(unflushed), nil
}

// rewriteEventsFileLocked renumbers the unflushed events onto base and swaps
// the result in for events.jsonl.
//
// A FAILURE HERE IS TERMINAL FOR THIS OUTBOX VALUE, deliberately. The file is
// closed and nilled up front, and every error path below returns before the
// reopen, so eventsFile stays nil and every later Append fails at its Seek with
// ErrInvalid ("locate outbox append mark: invalid argument"). Only reopening the
// outbox - in practice, a process restart - clears it.
//
// That is the intended outcome, not an oversight. rebaseLocked has ALREADY
// written the new cursor by the time this runs, because the reverse order loses
// events silently (see the comment there). Reopening the old file on a failure
// path would restore Append onto a file whose seqs the persisted cursor now
// contradicts: writes would succeed, land durably, and be filtered out or
// rejected later - the cursor-versus-file mismatch class be8118d7 fixed in the
// open path. A hard, permanent error is strictly better than a silent wrong
// answer here.
//
// It is also not silent. All three callers - openingSeq, applyForkedAttach and
// handleBadRequest's rebase - propagate this error and either fail the open or
// stop sync with a stated reason, so the user is told.
//
// TestFailedRebaseIsTerminalForTheOutbox pins that contract, including the part
// that is easy to regress: dead must mean "returns an error", never a panic and
// never a silent success.
func (ob *Outbox) rewriteEventsFileLocked(unflushed []StoredEvent, base int64) error {
	if ob.eventsFile != nil {
		_ = ob.eventsFile.Close()
		ob.eventsFile = nil
	}

	eventsPath := filepath.Join(ob.dir, eventsFileName)
	tmpEventsPath := filepath.Join(ob.dir, eventsFileName+".tmp")

	f, err := os.OpenFile(tmpEventsPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open tmp events file: %w", err)
	}

	for i, se := range unflushed {
		we := WireEvent{
			Seq:     base + int64(i) + 1,
			Type:    se.Type,
			Payload: se.Payload,
		}
		data, err := json.Marshal(we)
		if err != nil {
			_ = f.Close()
			return fmt.Errorf("marshal rebased event: %w", err)
		}
		data = append(data, '\n')
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("write rebased event: %w", err)
		}
	}

	if err := outboxSyncFile(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync tmp events file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close tmp events file: %w", err)
	}

	if err := os.Rename(tmpEventsPath, eventsPath); err != nil {
		return fmt.Errorf("rename events file: %w", err)
	}

	ef, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("reopen events file: %w", err)
	}
	ob.eventsFile = ef
	// The rewritten file replaces every byte the old one held, so any rollback
	// owed against the old file is void.
	ob.hasPendingTrunc = false
	return nil
}

// Dead reports whether a failed rebase left this outbox permanently
// unwritable (see rewriteEventsFileLocked). Recovery checks it before
// minting a session it could never move the backlog into.
func (ob *Outbox) Dead() bool {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	return ob.eventsFile == nil
}
