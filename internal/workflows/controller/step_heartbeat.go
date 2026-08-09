package controller

import (
	"sync"
	"time"
)

// maxStepHeartbeatEntries bounds the number of task ids in the heartbeat
// registry. When the registry is at capacity, a new task id evicts the
// oldest entry.
const maxStepHeartbeatEntries = 8192

// stepHeartbeatRegistry records the last heartbeat time for each task id.
// The mutex guards the map for concurrent use by the workflow controller
// and the CLI.
type stepHeartbeatRegistry struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// stepHeartbeats is the process-wide heartbeat registry.
var stepHeartbeats = stepHeartbeatRegistry{last: make(map[string]time.Time)}

// NoteStepHeartbeat records the current time as the last heartbeat for the
// task id. An empty task id is a no-op. The workflow join watchdog uses the
// recorded time to distinguish a live child from a stalled one.
func NoteStepHeartbeat(taskID string) {
	if taskID == "" {
		return
	}
	stepHeartbeats.note(taskID, time.Now())
}

// LastStepHeartbeat returns the recorded heartbeat time for the task id.
// The boolean result is false when the task id has no recorded heartbeat.
func LastStepHeartbeat(taskID string) (time.Time, bool) {
	return stepHeartbeats.lastHeartbeat(taskID)
}

// ResetStepHeartbeats clears the heartbeat registry. It is a test helper.
// Tests use it to start from a clean registry.
func ResetStepHeartbeats() {
	stepHeartbeats.mu.Lock()
	defer stepHeartbeats.mu.Unlock()
	stepHeartbeats.last = make(map[string]time.Time)
}

// note records the given time as the last heartbeat for the task id.
// When the task id is new and the registry is at capacity, note evicts the
// oldest entry first. The eviction scan is linear over the map. This is
// acceptable at the small capacity cap: a full scan of 8192 entries is much
// cheaper than a second data structure with its own bookkeeping.
func (r *stepHeartbeatRegistry) note(taskID string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.last[taskID]; !exists && len(r.last) >= maxStepHeartbeatEntries {
		var oldestKey string
		var oldestAt time.Time
		first := true
		for key, ts := range r.last {
			if first || ts.Before(oldestAt) {
				oldestKey = key
				oldestAt = ts
				first = false
			}
		}
		delete(r.last, oldestKey)
	}
	r.last[taskID] = at
}

// lastHeartbeat returns the recorded heartbeat time for the task id. The
// boolean result is false when the task id has no recorded heartbeat.
// The method is named lastHeartbeat, not last: Go forbids a field and a
// method with the same name, and the map field keeps the designed name last.
func (r *stepHeartbeatRegistry) lastHeartbeat(taskID string) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	at, ok := r.last[taskID]
	return at, ok
}
