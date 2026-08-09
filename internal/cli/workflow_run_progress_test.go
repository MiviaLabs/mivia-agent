package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
)

// TestWorkflowProgressWriterWritesValidJSONLines checks that one emitted
// progress event becomes one valid JSON line.
func TestWorkflowProgressWriterWritesValidJSONLines(t *testing.T) {
	var buf bytes.Buffer
	writer := &workflowProgressWriter{w: &buf}
	event := controller.ProgressEvent{
		Kind:             controller.ProgressStepStarted,
		RunID:            "wfr-test",
		StepID:           "one",
		TaskID:           "task-1",
		CoordinatorRunID: "cr-1",
		AttemptNo:        3,
		Detail:           "started",
		Timestamp:        time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	writer.Emit(event)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1: %q", len(lines), buf.String())
	}
	var got controller.ProgressEvent
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal = %v, line %q", err, lines[0])
	}
	if got.Kind != event.Kind ||
		got.RunID != event.RunID ||
		got.StepID != event.StepID ||
		got.TaskID != event.TaskID ||
		got.CoordinatorRunID != event.CoordinatorRunID ||
		got.AttemptNo != event.AttemptNo ||
		got.Detail != event.Detail ||
		!got.Timestamp.Equal(event.Timestamp) {
		t.Fatalf("event round-trip mismatch: got %+v, want %+v", got, event)
	}
}

// TestWorkflowProgressWriterBrokenPipeIgnoresEPIPE proves Emit returns (no
// panic) when the underlying writer is a pipe whose read end is closed: the
// write fails with EPIPE and the error is ignored, because fd-2 progress
// reporting must degrade, not kill, the workflow run. A normal buffer still
// receives the event afterwards.
func TestWorkflowProgressWriterBrokenPipeIgnoresEPIPE(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = readEnd.Close() // closing the read end makes later writes fail with EPIPE
	writer := &workflowProgressWriter{w: writeEnd}
	writer.Emit(controller.ProgressEvent{Kind: controller.ProgressRunFinished, Detail: "succeeded"})
	_ = writeEnd.Close()

	// The same writer still works against a healthy buffer.
	var buf bytes.Buffer
	healthy := &workflowProgressWriter{w: &buf}
	healthy.Emit(controller.ProgressEvent{Kind: controller.ProgressStepStarted, StepID: "one"})
	if got := strings.TrimSpace(buf.String()); got == "" {
		t.Fatal("healthy buffer received no progress line")
	}
}

// TestWorkflowProgressWriterConcurrentEmit checks that concurrent Emit calls
// do not corrupt the JSON-lines stream. Run with -race.
func TestWorkflowProgressWriterConcurrentEmit(t *testing.T) {
	var buf bytes.Buffer
	writer := &workflowProgressWriter{w: &buf}
	const goroutines = 100
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			writer.Emit(controller.ProgressEvent{
				Kind:      controller.ProgressStepHeartbeat,
				RunID:     "wfr-test",
				StepID:    "one",
				AttemptNo: i,
			})
		}(i)
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != goroutines {
		t.Fatalf("lines = %d, want %d: %q", len(lines), goroutines, buf.String())
	}
	seen := make(map[int]bool, goroutines)
	for _, line := range lines {
		var got controller.ProgressEvent
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("unmarshal = %v, line %q", err, line)
		}
		if got.Kind != controller.ProgressStepHeartbeat {
			t.Fatalf("kind = %q, want step_heartbeat", got.Kind)
		}
		seen[got.AttemptNo] = true
	}
	if len(seen) != goroutines {
		t.Fatalf("distinct attempts = %d, want %d", len(seen), goroutines)
	}
}
