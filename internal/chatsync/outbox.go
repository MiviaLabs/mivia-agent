package chatsync

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
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
func (ob *Outbox) Append(events ...WireEvent) error {
	ob.mu.Lock()
	defer ob.mu.Unlock()

	if len(events) == 0 {
		return nil
	}
	if ob.unflushed+len(events) > ob.maxUnflushed {
		return ErrOutboxOverflow
	}

	for _, ev := range events {
		if ev.Seq > ob.maxSeq {
			ob.maxSeq = ev.Seq
		}
		data, err := json.Marshal(ev)
		if err != nil {
			return fmt.Errorf("marshal event: %w", err)
		}
		data = append(data, '\n')
		if _, err := ob.eventsFile.Write(data); err != nil {
			return fmt.Errorf("write event: %w", err)
		}
	}

	if err := ob.eventsFile.Sync(); err != nil {
		return fmt.Errorf("sync events file: %w", err)
	}
	ob.unflushed += len(events)
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
	data, err := json.Marshal(newCursor)
	if err != nil {
		return fmt.Errorf("marshal cursor: %w", err)
	}

	tmpPath := filepath.Join(ob.dir, cursorFileName+".tmp")
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write tmp cursor: %w", err)
	}

	cursorPath := filepath.Join(ob.dir, cursorFileName)
	if err := os.Rename(tmpPath, cursorPath); err != nil {
		return fmt.Errorf("rename cursor: %w", err)
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
