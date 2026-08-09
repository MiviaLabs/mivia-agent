package controller

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestNoteAndLastHeartbeat verifies that a noted heartbeat is readable and
// that an unknown task id reports no heartbeat.
func TestNoteAndLastHeartbeat(t *testing.T) {
	ResetStepHeartbeats()
	before := time.Now()
	NoteStepHeartbeat("task-1")
	after := time.Now()
	got, ok := LastStepHeartbeat("task-1")
	if !ok {
		t.Fatal("heartbeat for task-1 is missing after a note")
	}
	if got.Before(before) || got.After(after) {
		t.Fatalf("recorded heartbeat %s is outside the note window [%s, %s]", got, before, after)
	}
	if _, ok := LastStepHeartbeat("unknown-task"); ok {
		t.Fatal("unknown task id reported a heartbeat; want ok=false")
	}
}

// TestHeartbeatNoteEmptyTaskIDNoop verifies that an empty task id is ignored.
func TestHeartbeatNoteEmptyTaskIDNoop(t *testing.T) {
	ResetStepHeartbeats()
	NoteStepHeartbeat("")
	if got, ok := LastStepHeartbeat(""); ok {
		t.Fatalf("empty task id reported heartbeat %s; want a no-op", got)
	}
	if len(stepHeartbeats.last) != 0 {
		t.Fatalf("registry size = %d after empty note, want 0", len(stepHeartbeats.last))
	}
}

// TestConcurrentHeartbeatNoteAndRead verifies that the registry is safe for
// concurrent note and read operations (run with -race).
func TestConcurrentHeartbeatNoteAndRead(t *testing.T) {
	ResetStepHeartbeats()
	const workers = 32
	const notesPerWorker = 100
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < notesPerWorker; i++ {
				taskID := fmt.Sprintf("task-%d-%d", worker, i)
				NoteStepHeartbeat(taskID)
				if _, ok := LastStepHeartbeat(taskID); !ok {
					t.Errorf("heartbeat for %s is missing after a note", taskID)
				}
			}
		}(w)
	}
	wg.Wait()
}

// TestHeartbeatEvictionAtCap verifies that the registry never exceeds the
// capacity cap and that the oldest entries are evicted first. It uses the
// unexported note with synthetic times for determinism.
func TestHeartbeatEvictionAtCap(t *testing.T) {
	ResetStepHeartbeats()
	base := time.Unix(1_000_000_000, 0)
	for i := 0; i < maxStepHeartbeatEntries+5; i++ {
		stepHeartbeats.note(fmt.Sprintf("task-%d", i), base.Add(time.Duration(i)*time.Second))
	}
	if got := len(stepHeartbeats.last); got != maxStepHeartbeatEntries {
		t.Fatalf("registry size = %d, want %d (size must stay at the cap)", got, maxStepHeartbeatEntries)
	}
	for i := 0; i < 5; i++ {
		if _, ok := LastStepHeartbeat(fmt.Sprintf("task-%d", i)); ok {
			t.Fatalf("task-%d survived; want the oldest entries evicted", i)
		}
	}
	if _, ok := LastStepHeartbeat("task-5"); !ok {
		t.Fatal("task-5 is missing; want the oldest surviving entry present")
	}
	for i := maxStepHeartbeatEntries; i < maxStepHeartbeatEntries+5; i++ {
		if _, ok := LastStepHeartbeat(fmt.Sprintf("task-%d", i)); !ok {
			t.Fatalf("task-%d is missing; want the newest entries present", i)
		}
	}
}

// TestResetStepHeartbeatsClears verifies that the reset helper empties the
// registry so tests start from a clean slate.
func TestResetStepHeartbeatsClears(t *testing.T) {
	ResetStepHeartbeats()
	NoteStepHeartbeat("task-1")
	if _, ok := LastStepHeartbeat("task-1"); !ok {
		t.Fatal("setup: heartbeat for task-1 is missing before reset")
	}
	ResetStepHeartbeats()
	if _, ok := LastStepHeartbeat("task-1"); ok {
		t.Fatal("heartbeat for task-1 survived the reset; want it cleared")
	}
	if len(stepHeartbeats.last) != 0 {
		t.Fatalf("registry size = %d after reset, want 0", len(stepHeartbeats.last))
	}
}
