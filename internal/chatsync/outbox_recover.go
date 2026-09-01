package chatsync

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// truncateToMarkLocked drops everything the failed batch wrote past mark and
// makes the truncation durable, so a crash cannot resurrect a partial record.
// The caller must hold ob.mu.
func (ob *Outbox) truncateToMarkLocked(mark int64) error {
	if err := outboxTruncateFile(ob.eventsFile, mark); err != nil {
		return fmt.Errorf("truncate outbox to append mark: %w", err)
	}
	if err := outboxSyncFile(ob.eventsFile); err != nil {
		return fmt.Errorf("sync truncated events file: %w", err)
	}
	return nil
}

// retryPendingTruncateLocked pays a rollback a previous Append could not
// complete. Until it succeeds no new record may be written, because the failed
// batch's bytes are still past the mark and the caller has already reissued
// those seqs. The caller must hold ob.mu.
func (ob *Outbox) retryPendingTruncateLocked() error {
	if !ob.hasPendingTrunc {
		return nil
	}
	if err := ob.truncateToMarkLocked(ob.pendingTrunc); err != nil {
		return fmt.Errorf("outbox rollback still owed: %w", err)
	}
	ob.hasPendingTrunc = false
	return nil
}

// repairEventsFile drops the trailing bytes of events.jsonl that no reader can
// trust, and makes the shortened file durable.
//
// A failed fsync leaves an arbitrary subset of the batch on disk, and a failed
// rollback leaves the whole batch there. Either way the tail can hold a torn
// record, or a record whose seq repeats or skips the one before it. The wire
// contract has no tolerance for that: the API rejects an append whose first
// seq is not serverLastSeq+1, so one bad record wedges the session for the
// life of the process. Losing an unacknowledged tail event costs one event;
// keeping it costs the session.
//
// The healthy case reads the file and writes nothing.
func repairEventsFile(dir string) error {
	path := filepath.Join(dir, eventsFileName)

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open events file for repair: %w", err)
	}
	size, good, scanErr := scanGoodPrefix(f)
	closeErr := f.Close()
	if scanErr != nil {
		return scanErr
	}
	if closeErr != nil {
		return fmt.Errorf("close events file after repair scan: %w", closeErr)
	}
	if good == size {
		return nil
	}
	return truncateEventsFileTo(path, good)
}

// scanGoodPrefix returns the file size and the length of the longest prefix
// that holds only complete, parsable records whose seqs step up by exactly one.
// The first record may carry any seq, because a rebase renumbers the file onto
// the server's mark rather than onto 1.
func scanGoodPrefix(f *os.File) (size int64, good int64, err error) {
	st, err := f.Stat()
	if err != nil {
		return 0, 0, fmt.Errorf("stat events file for repair: %w", err)
	}
	size = st.Size()

	r := bufio.NewReader(f)
	var prevSeq int64
	havePrev := false

	for {
		raw, readErr := r.ReadBytes('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return size, good, fmt.Errorf("read events file for repair: %w", readErr)
		}
		if len(raw) == 0 {
			return size, good, nil
		}
		if errors.Is(readErr, io.EOF) {
			// No terminating newline: the record was still being written.
			return size, good, nil
		}

		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 {
			var se StoredEvent
			if json.Unmarshal(trimmed, &se) != nil {
				return size, good, nil
			}
			if havePrev && se.Seq != prevSeq+1 {
				return size, good, nil
			}
			prevSeq = se.Seq
			havePrev = true
		}
		good += int64(len(raw))
	}
}

// truncateEventsFileTo cuts the file to length and fsyncs it, so the discarded
// tail cannot come back after a crash.
func truncateEventsFileTo(path string, length int64) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open events file to repair tail: %w", err)
	}
	defer f.Close()

	if err := outboxTruncateFile(f, length); err != nil {
		return fmt.Errorf("repair events file tail: %w", err)
	}
	if err := outboxSyncFile(f); err != nil {
		return fmt.Errorf("sync repaired events file: %w", err)
	}
	return nil
}
