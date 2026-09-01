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
	"sync"
	"syscall"
	"time"
)

var (
	ErrOutboxLocked   = errors.New("chatsync: outbox is locked by another process")
	ErrOutboxOverflow = errors.New("chatsync: outbox capacity exceeded")
)

const (
	DefaultMaxUnflushed = 5000
	eventsFileName      = "events.jsonl"
	cursorFileName      = "cursor.json"
	lockFileName        = "lock"
)

// outboxSyncFile and outboxTruncateFile are the durability seams. Production
// always runs the two closures below. Tests replace them to make one specific
// fsync or truncate fail, which is the only way to reach the recovery paths
// that a disk error or a power loss would otherwise reach. Both are keyed on
// the caller's *os.File, so a test targets one file by name. Never swap them
// outside a test, and always restore them with t.Cleanup.
var (
	outboxSyncFile     = func(f *os.File) error { return f.Sync() }
	outboxTruncateFile = func(f *os.File, size int64) error { return f.Truncate(size) }
)

// Cursor represents the durable flushed sequence marker.
type Cursor struct {
	FlushedSeq int64     `json:"flushed_seq"`
	FlushedAt  time.Time `json:"flushed_at"`
}

// Outbox owns local per-session append-only event files, mutual exclusion locking,
// and durable cursor markers.
type Outbox struct {
	dir          string
	maxUnflushed int
	mu           sync.Mutex
	lockFile     *os.File
	eventsFile   *os.File
	cursor       Cursor
	unflushed    int
	maxSeq       int64

	// pendingTrunc is the offset a failed rollback still owes events.jsonl,
	// and hasPendingTrunc says whether one is owed at all. Offset 0 is a
	// legitimate mark, so the flag cannot be folded into the offset.
	pendingTrunc    int64
	hasPendingTrunc bool
}

// OpenOutbox opens or creates the outbox directory, acquires the process lock,
// and loads the current cursor.
func OpenOutbox(dir string, maxUnflushed int) (*Outbox, error) {
	if maxUnflushed <= 0 {
		maxUnflushed = DefaultMaxUnflushed
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create outbox dir: %w", err)
	}

	lf, err := acquireLock(dir)
	if err != nil {
		return nil, err
	}

	// A previous run can have died with a rollback still owed, or with a
	// record only half on disk. Repair before anything reads the file, so no
	// caller ever sees a duplicate, a gap, or a torn record.
	if err := repairEventsFile(dir); err != nil {
		releaseLock(lf)
		return nil, err
	}

	eventsPath := filepath.Join(dir, eventsFileName)
	ef, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		releaseLock(lf)
		return nil, fmt.Errorf("open events file: %w", err)
	}

	ob := &Outbox{
		dir:          dir,
		maxUnflushed: maxUnflushed,
		lockFile:     lf,
		eventsFile:   ef,
	}

	if err := ob.loadCursorAndCount(); err != nil {
		_ = ob.Close()
		return nil, err
	}

	return ob, nil
}

func acquireLock(dir string) (*os.File, error) {
	lockPath := filepath.Join(dir, lockFileName)
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lf.Close()
		return nil, ErrOutboxLocked
	}
	return lf, nil
}

func releaseLock(lf *os.File) {
	if lf != nil {
		_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
		_ = lf.Close()
	}
}

// Close releases the events file and process flock.
func (ob *Outbox) Close() error {
	ob.mu.Lock()
	defer ob.mu.Unlock()

	var firstErr error
	if ob.eventsFile != nil {
		if err := ob.eventsFile.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		ob.eventsFile = nil
	}
	if ob.lockFile != nil {
		releaseLock(ob.lockFile)
		ob.lockFile = nil
	}
	return firstErr
}

func (ob *Outbox) loadCursorAndCount() error {
	cursorPath := filepath.Join(ob.dir, cursorFileName)
	data, err := os.ReadFile(cursorPath)
	if err == nil {
		var cur Cursor
		if err := json.Unmarshal(data, &cur); err == nil {
			ob.cursor = cur
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read cursor: %w", err)
	}

	unflushed, err := ob.countUnflushedFromDisk()
	if err != nil {
		return err
	}
	ob.unflushed = unflushed
	return nil
}

// Append persists WireEvents into events.jsonl with fsync before returning.
//
// The batch is all-or-nothing. A failure part way through used to leave the
// earlier records on disk while `unflushed` stayed un-incremented, so the batch
// read as never stored: the caller rolled the seq counter back and reissued the
// same seqs, and the file then held two records for one seq. The server's
// contiguity check rejects that, which wedges the stream for good. On any
// failure the file is therefore truncated back to the offset it held before the
// batch started, and no counter moves.
func (ob *Outbox) Append(events ...WireEvent) error {
	ob.mu.Lock()
	defer ob.mu.Unlock()

	if len(events) == 0 {
		return nil
	}
	if err := ob.retryPendingTruncateLocked(); err != nil {
		return err
	}
	if ob.unflushed+len(events) > ob.maxUnflushed {
		return ErrOutboxOverflow
	}

	mark, err := ob.eventsFile.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("locate outbox append mark: %w", err)
	}

	if err := ob.writeBatchLocked(events); err != nil {
		if truncErr := ob.truncateToMarkLocked(mark); truncErr != nil {
			// The rollback failed too, so the batch's bytes are still
			// readable past the mark. Writing the reissued seqs on top of
			// them would give one seq two records. Owe the rollback and
			// refuse to append again until it is paid.
			ob.pendingTrunc = mark
			ob.hasPendingTrunc = true
			return errors.Join(err, truncErr)
		}
		return err
	}

	for _, ev := range events {
		if ev.Seq > ob.maxSeq {
			ob.maxSeq = ev.Seq
		}
	}
	ob.unflushed += len(events)
	return nil
}

// writeBatchLocked encodes the whole batch before it writes any of it, so an
// encode failure on a later event cannot leave an earlier one on disk. The
// caller must hold ob.mu.
func (ob *Outbox) writeBatchLocked(events []WireEvent) error {
	var buf bytes.Buffer
	for _, ev := range events {
		data, err := json.Marshal(ev)
		if err != nil {
			return fmt.Errorf("marshal event: %w", err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}

	if _, err := ob.eventsFile.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	if err := outboxSyncFile(ob.eventsFile); err != nil {
		return fmt.Errorf("sync events file: %w", err)
	}
	return nil
}

// AdvanceCursor updates cursor.json atomically via rename after server ack.
func (ob *Outbox) AdvanceCursor(seq int64) error {
	ob.mu.Lock()
	defer ob.mu.Unlock()

	if seq < ob.cursor.FlushedSeq {
		return nil
	}

	newCursor := Cursor{
		FlushedSeq: seq,
		FlushedAt:  time.Now(),
	}
	if err := ob.writeCursorLocked(newCursor); err != nil {
		return err
	}

	ob.cursor = newCursor
	unflushed, err := ob.countUnflushedFromDisk()
	if err == nil {
		ob.unflushed = unflushed
	}
	return nil
}

// Cursor returns a copy of the current cursor state.
func (ob *Outbox) Cursor() Cursor {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	return ob.cursor
}

// UnflushedEvents reads all stored events with seq > cursor.flushed_seq.
func (ob *Outbox) UnflushedEvents() ([]StoredEvent, error) {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	return ob.unflushedEventsLocked()
}

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

func (ob *Outbox) writeCursorLocked(cur Cursor) error {
	cursorData, err := json.Marshal(cur)
	if err != nil {
		return fmt.Errorf("marshal cursor: %w", err)
	}
	tmpCursorPath := filepath.Join(ob.dir, cursorFileName+".tmp")
	cf, err := os.OpenFile(tmpCursorPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open tmp cursor: %w", err)
	}
	if _, err := cf.Write(cursorData); err != nil {
		_ = cf.Close()
		return fmt.Errorf("write tmp cursor: %w", err)
	}
	if err := outboxSyncFile(cf); err != nil {
		_ = cf.Close()
		return fmt.Errorf("sync tmp cursor: %w", err)
	}
	if err := cf.Close(); err != nil {
		return fmt.Errorf("close tmp cursor: %w", err)
	}
	cursorPath := filepath.Join(ob.dir, cursorFileName)
	if err := os.Rename(tmpCursorPath, cursorPath); err != nil {
		return fmt.Errorf("rename cursor: %w", err)
	}
	return nil
}

func (ob *Outbox) unflushedEventsLocked() ([]StoredEvent, error) {
	eventsPath := filepath.Join(ob.dir, eventsFileName)
	f, err := os.Open(eventsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var result []StoredEvent
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var se StoredEvent
		if err := json.Unmarshal(line, &se); err != nil {
			return nil, fmt.Errorf("unmarshal stored event: %w", err)
		}
		if se.Seq > ob.cursor.FlushedSeq {
			result = append(result, se)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan events: %w", err)
	}
	return result, nil
}

// MaxSeq returns the highest sequence number recorded in the outbox.
func (ob *Outbox) MaxSeq() int64 {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	return ob.maxSeq
}

func (ob *Outbox) countUnflushedFromDisk() (int, error) {
	eventsPath := filepath.Join(ob.dir, eventsFileName)
	f, err := os.Open(eventsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var se StoredEvent
		if err := json.Unmarshal(line, &se); err != nil {
			return 0, fmt.Errorf("unmarshal stored event: %w", err)
		}
		if se.Seq > ob.maxSeq {
			ob.maxSeq = se.Seq
		}
		if se.Seq > ob.cursor.FlushedSeq {
			count++
		}
	}
	return count, scanner.Err()
}
