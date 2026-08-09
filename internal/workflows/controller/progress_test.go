package controller

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// recordingProgressSink collects emitted progress events for assertions.
type recordingProgressSink struct {
	mu     sync.Mutex
	events []ProgressEvent
}

// Emit appends one progress event to the recorded list.
func (s *recordingProgressSink) Emit(e ProgressEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

// take returns the recorded events and clears the list.
func (s *recordingProgressSink) take() []ProgressEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := append([]ProgressEvent(nil), s.events...)
	s.events = nil
	return events
}

func progressController(t *testing.T, runID string, sink ProgressSink) *LinearController {
	t.Helper()
	ctrl, err := NewLinearController(workflowledger.NewMemoryRepository(), &linearRunner{}, linearWorkflow(t), nil, nil, runID, []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if sink != nil {
		if err := ctrl.SetProgressSink(sink); err != nil {
			t.Fatal(err)
		}
	}
	return ctrl
}

// TestEmitProgressNoOpWithNilSink: a nil sink must make emitProgress a no-op.
// The caller's event must stay untouched.
func TestEmitProgressNoOpWithNilSink(t *testing.T) {
	ctrl := progressController(t, "wfr-progress-nil", nil)
	event := ProgressEvent{Kind: ProgressRunFinished, Detail: "succeeded"}
	ctrl.emitProgress(event)
	if !event.Timestamp.IsZero() || event.RunID != "" {
		t.Fatalf("emitProgress with a nil sink mutated the event: %+v", event)
	}
}

// TestEmitProgressFillsTimestampAndRunID: emitProgress must stamp the event
// with the controller clock and the controller run ID.
func TestEmitProgressFillsTimestampAndRunID(t *testing.T) {
	now := time.Date(2026, 8, 6, 1, 2, 3, 0, time.UTC)
	sink := &recordingProgressSink{}
	ctrl := progressController(t, "wfr-progress-stamp", sink)
	if err := ctrl.SetTimeSource(func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	ctrl.emitProgress(ProgressEvent{Kind: ProgressRunFinished, Detail: "succeeded"})
	events := sink.take()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if !events[0].Timestamp.Equal(now) {
		t.Fatalf("timestamp = %v, want %v", events[0].Timestamp, now)
	}
	if events[0].RunID != "wfr-progress-stamp" {
		t.Fatalf("run ID = %q, want wfr-progress-stamp", events[0].RunID)
	}
}

// TestProgressEventJSONShape: the CLI writer marshals ProgressEvent directly.
// Every field must appear under its Go name.
func TestProgressEventJSONShape(t *testing.T) {
	event := ProgressEvent{
		Kind: ProgressStepCompleted, RunID: "wfr-1", StepID: "build", TaskID: "task-1",
		CoordinatorRunID: "coord-1", AttemptNo: 2, Detail: "succeeded",
		Timestamp: time.Date(2026, 8, 6, 1, 2, 3, 0, time.UTC),
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"Kind": "step_completed", "RunID": "wfr-1", "StepID": "build",
		"TaskID": "task-1", "CoordinatorRunID": "coord-1",
		"AttemptNo": float64(2), "Detail": "succeeded",
	} {
		if decoded[key] != want {
			t.Fatalf("field %s = %v, want %v", key, decoded[key], want)
		}
	}
	if ts, ok := decoded["Timestamp"].(string); !ok || ts == "" {
		t.Fatalf("Timestamp field = %v, want a non-empty time string", decoded["Timestamp"])
	}
}

// TestEmitStepHelpersBuildEvents: the step and run helpers must build events
// with the controller run ID and the attempt child identity.
func TestEmitStepHelpersBuildEvents(t *testing.T) {
	now := time.Date(2026, 8, 6, 1, 2, 3, 0, time.UTC)
	sink := &recordingProgressSink{}
	ctrl := progressController(t, "wfr-progress-helpers", sink)
	if err := ctrl.SetTimeSource(func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	step := definition.Step{ID: "build", Kind: "agent"}
	attempt := workflowledger.StepAttempt{AttemptID: "wfa-1", RunID: "wfr-progress-helpers", StepID: "build", AttemptNo: 3, CoordinatorRunID: "coord-9", TaskID: "task-9"}
	ctrl.emitStepStarted(step, attempt)
	ctrl.emitStepCompleted(step, attempt, "succeeded")
	ctrl.emitRunFinished("succeeded")
	ctrl.emitRunFailed("step failed")
	events := sink.take()
	if len(events) != 4 {
		t.Fatalf("events = %d, want 4", len(events))
	}
	want := []ProgressKind{ProgressStepStarted, ProgressStepCompleted, ProgressRunFinished, ProgressRunFailed}
	for i, kind := range want {
		if events[i].Kind != kind {
			t.Fatalf("event %d kind = %q, want %q", i, events[i].Kind, kind)
		}
		if events[i].RunID != "wfr-progress-helpers" {
			t.Fatalf("event %d run ID = %q, want wfr-progress-helpers", i, events[i].RunID)
		}
	}
	for i := 0; i < 2; i++ {
		if events[i].StepID != "build" || events[i].TaskID != "task-9" || events[i].CoordinatorRunID != "coord-9" || events[i].AttemptNo != 3 {
			t.Fatalf("event %d step identity = %+v", i, events[i])
		}
	}
	if events[1].Detail != "succeeded" || events[2].Detail != "succeeded" || events[3].Detail != "step failed" {
		t.Fatalf("event details = %q / %q / %q", events[1].Detail, events[2].Detail, events[3].Detail)
	}
	if !events[0].Timestamp.Equal(now) {
		t.Fatalf("event timestamp = %v, want %v", events[0].Timestamp, now)
	}
}
