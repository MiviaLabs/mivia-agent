package chatsync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// statusFileName is the durable health record beside events.jsonl and
// cursor.json. It exists so a stalled sync can be diagnosed AFTER the process
// is gone: every other signal (OnStop, StopReason, the retry schedule) is
// process-local and dies with it.
const statusFileName = "status.json"

// Sync health states, as written to status.json.
const (
	SyncStateHealthy   = "healthy"
	SyncStateDegraded  = "degraded"
	SyncStateRecovered = "recovered"
	SyncStateStopped   = "stopped"
)

const (
	// degradedFailureThreshold is the count arm of the degraded rule: this
	// many consecutive failed pushes and the host is told.
	degradedFailureThreshold = 3
	// degradedSilenceThreshold is the time arm: a failed push this long after
	// the last success is degraded on its own, because backoff saturates at
	// 30s and three failures could otherwise take 90s to accumulate.
	degradedSilenceThreshold = 60 * time.Second
)

// SyncStatus is the record status.json holds. A reader tells a throttled
// create stall from a plain push stall by create_throttled_until, which is
// null whenever no throttle is engaged.
type SyncStatus struct {
	State                string     `json:"state"`
	Reason               string     `json:"reason"`
	Unflushed            int        `json:"unflushed"`
	LastSuccessAt        *time.Time `json:"last_success_at"`
	ConsecutiveFailures  int        `json:"consecutive_failures"`
	Recoveries           int        `json:"recoveries"`
	CreateFailures       int        `json:"create_failures"`
	CreateThrottledUntil *time.Time `json:"create_throttled_until"`
	At                   time.Time  `json:"at"`
}

// syncHealth tracks push health and writes status.json on transition only.
//
// Every note* method returns the state it transitioned INTO, or "" when the
// call changed nothing, so the session fires a host callback exactly once per
// transition and never per failed push. The write is best-effort (R8): a
// full or read-only disk loses the diagnostic, never the session.
type syncHealth struct {
	mu    sync.Mutex
	now   func() time.Time
	write func(SyncStatus) error

	state               string
	lastSuccessAt       time.Time
	consecutiveFailures int

	// Recovery bookkeeping, carried into every record so a throttled-create
	// stall and a plain push stall do not serialise identically.
	recoveries           int
	createFailures       int
	createThrottledUntil time.Time
}

func newSyncHealth(write func(SyncStatus) error) *syncHealth {
	return &syncHealth{now: time.Now, write: write}
}

// newStatusFileWriter returns the writer for <dir>/status.json.
func newStatusFileWriter(dir string) func(SyncStatus) error {
	return func(st SyncStatus) error {
		data, err := json.Marshal(st)
		if err != nil {
			return err
		}
		return writeFileAtomic(dir, statusFileName, data)
	}
}

// noteOpen records a successful attach: the session starts healthy and the
// attach counts as the last success, so the time arm has a baseline.
func (h *syncHealth) noteOpen(unflushed int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state = SyncStateHealthy
	h.lastSuccessAt = h.now()
	h.consecutiveFailures = 0
	h.record("attached", unflushed)
}

// noteSuccess records a push that landed. It transitions only out of
// degraded: a healthy session that keeps succeeding is not news.
func (h *syncHealth) noteSuccess(unflushed int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == SyncStateStopped {
		return ""
	}
	failures := h.consecutiveFailures
	h.lastSuccessAt = h.now()
	h.consecutiveFailures = 0
	if h.state != SyncStateDegraded {
		return ""
	}
	h.state = SyncStateRecovered
	h.record(fmt.Sprintf("push succeeded after %d consecutive failures", failures), unflushed)
	return SyncStateRecovered
}

// noteRecovery counts a completed recovery. It is recorded with the next
// transition; a recovery is not itself one.
func (h *syncHealth) noteRecovery() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recoveries++
	h.createFailures = 0
	h.createThrottledUntil = time.Time{}
}

// noteCreateFailure records a CreateSession attempt that failed during
// recovery. It counts toward the degraded streak like any failed push, and
// carries the create counters so the record names the throttle when one is
// engaged.
func (h *syncHealth) noteCreateFailure(err error, unflushed, failures int, throttledUntil time.Time) string {
	h.mu.Lock()
	h.createFailures = failures
	h.createThrottledUntil = throttledUntil
	h.mu.Unlock()
	return h.noteFailure(err, unflushed)
}

// noteFailure records a push that did not land. It transitions into degraded
// once, on whichever arm trips first.
func (h *syncHealth) noteFailure(err error, unflushed int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == SyncStateStopped {
		return ""
	}
	h.consecutiveFailures++
	if h.state == SyncStateDegraded {
		return ""
	}
	silent := h.now().Sub(h.lastSuccessAt)
	countArm := h.consecutiveFailures >= degradedFailureThreshold
	timeArm := silent >= degradedSilenceThreshold
	if !countArm && !timeArm {
		return ""
	}
	h.state = SyncStateDegraded
	h.record(fmt.Sprintf("%d consecutive push failures, no success for %s: %v",
		h.consecutiveFailures, silent.Round(time.Second), err), unflushed)
	return SyncStateDegraded
}

// noteStop records why sync stopped. The first reason wins: a terminal latch
// (poison, auth, a recovery bound) is recorded by the worker, and the orderly
// Stop that follows must not overwrite it with "session closed".
func (h *syncHealth) noteStop(reason string, unflushed int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == SyncStateStopped {
		return ""
	}
	h.state = SyncStateStopped
	h.record(reason, unflushed)
	return SyncStateStopped
}

// record writes the current state. The caller must hold h.mu.
func (h *syncHealth) record(reason string, unflushed int) {
	if h.write == nil {
		return
	}
	st := SyncStatus{
		State:               h.state,
		Reason:              reason,
		Unflushed:           unflushed,
		ConsecutiveFailures: h.consecutiveFailures,
		Recoveries:          h.recoveries,
		CreateFailures:      h.createFailures,
		At:                  h.now(),
	}
	if !h.lastSuccessAt.IsZero() {
		at := h.lastSuccessAt
		st.LastSuccessAt = &at
	}
	if !h.createThrottledUntil.IsZero() {
		until := h.createThrottledUntil
		st.CreateThrottledUntil = &until
	}
	// Best-effort by design (R8): the file is a diagnostic, and a diagnostic
	// that cannot be written must not take the session down with it.
	_ = h.write(st)
}

// writeFileAtomic writes <dir>/<name> through a temp file, fsync and rename,
// so a reader never sees a partial file and a failed write leaves the
// previous one intact. It is the single write discipline for every small
// record the outbox directory holds (cursor.json, status.json).
func writeFileAtomic(dir, name string, data []byte) error {
	tmpPath := filepath.Join(dir, name+".tmp")
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open tmp %s: %w", name, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write tmp %s: %w", name, err)
	}
	if err := outboxSyncFile(f); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync tmp %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close tmp %s: %w", name, err)
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename %s: %w", name, err)
	}
	return nil
}
