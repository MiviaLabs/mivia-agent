package controller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestLinearProgressStepStartedAndCompletedOnSuccess: one fresh agent step
// admitted and executed to success must emit exactly one ProgressStepStarted
// and exactly one ProgressStepCompleted carrying the succeeded status and the
// attempt's child identity.
func TestLinearProgressStepStartedAndCompletedOnSuccess(t *testing.T) {
	wf := linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	runner := &linearRunner{outputs: map[string]json.RawMessage{"first": json.RawMessage(`{"ok":true}`)}}
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"first": {Agent: agents.ResolvedAgent{Name: "one"}},
	}, map[string]any{"task": "build"}, "wfr-step-progress-success", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingProgressSink{}
	if err := ctrl.SetProgressSink(sink); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ctrl.Advance(context.Background()); err != nil {
		t.Fatal(err)
	}
	started, completed := 0, 0
	startedTaskID := ""
	for _, e := range sink.take() {
		switch e.Kind {
		case ProgressStepStarted:
			started++
			if e.StepID != "first" || e.TaskID == "" || e.CoordinatorRunID == "" || e.AttemptNo != 1 {
				t.Fatalf("step_started event = %+v", e)
			}
			startedTaskID = e.TaskID
		case ProgressStepCompleted:
			completed++
			if e.StepID != "first" || e.Detail != "succeeded" || e.AttemptNo != 1 {
				t.Fatalf("step_completed event = %+v", e)
			}
			if e.TaskID != startedTaskID {
				t.Fatalf("step_completed task ID = %q, want %q", e.TaskID, startedTaskID)
			}
		}
	}
	if started != 1 {
		t.Fatalf("step_started events = %d, want 1", started)
	}
	if completed != 1 {
		t.Fatalf("step_completed events = %d, want 1", completed)
	}
}

// TestRedispatchEmitsInterruptedCompletionAndFreshStarted: the redispatch path
// must report the stale attempt completed as interrupted AND the fresh attempt
// started before the fresh child executes. The stale attempt must never die
// silently and the fresh attempt must never run without a step_started.
func TestRedispatchEmitsInterruptedCompletionAndFreshStarted(t *testing.T) {
	runner := &deadlineRecordingRunner{}
	ctrl, repo := newErrorController(t, runner, "wfr-redispatch-progress")
	sink := &recordingProgressSink{}
	if err := ctrl.SetProgressSink(sink); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), ctrl.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	// A RUNNING attempt with a recorded coordinator identity forces
	// joinInFlightAttempt -> interruptAndRedispatch (this runner has no join
	// capability), exactly the crash-resume path under test.
	if err := repo.CreateStepAttempt(context.Background(), workflowledger.StepAttempt{
		AttemptID: "wfa-one-1", RunID: ctrl.RunID, StepID: "one", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning, CoordinatorRunID: "coord-stale", TaskID: "task-stale",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := sink.take()
	var started, completed []ProgressEvent
	for _, e := range events {
		switch e.Kind {
		case ProgressStepStarted:
			started = append(started, e)
		case ProgressStepCompleted:
			completed = append(completed, e)
		}
	}
	if len(started) != 1 {
		t.Fatalf("step_started events = %d, want exactly 1: %+v", len(started), events)
	}
	if started[0].AttemptNo != 2 || started[0].TaskID == "" || started[0].CoordinatorRunID == "" {
		t.Fatalf("step_started = %+v, want the fresh attempt 2 with a child identity", started[0])
	}
	var interrupted, freshCompleted *ProgressEvent
	for i := range completed {
		e := completed[i]
		if e.AttemptNo == 1 {
			if e.Detail != "interrupted" {
				t.Fatalf("stale step_completed detail = %q, want interrupted", e.Detail)
			}
			interrupted = &e
		} else if e.AttemptNo == 2 {
			if e.Detail != "succeeded" {
				t.Fatalf("fresh step_completed detail = %q, want succeeded", e.Detail)
			}
			freshCompleted = &e
		}
	}
	if interrupted == nil || freshCompleted == nil {
		t.Fatalf("step_completed events = %+v, want one interrupted (attempt 1) and one succeeded (attempt 2)", completed)
	}
	// The fresh attempt's started must precede its completed in the trail.
	startedIdx, freshIdx := -1, -1
	for i, e := range events {
		if e.Kind == ProgressStepStarted && e.AttemptNo == 2 {
			startedIdx = i
		}
		if e.Kind == ProgressStepCompleted && e.AttemptNo == 2 {
			freshIdx = i
		}
	}
	if startedIdx < 0 || freshIdx < 0 || startedIdx > freshIdx {
		t.Fatalf("fresh step_started (idx %d) must precede fresh step_completed (idx %d): %+v", startedIdx, freshIdx, events)
	}
}

// TestLinearProgressStepCompletedFailed: a step whose runner returns an error
// must emit exactly one ProgressStepCompleted with the failed status and no
// duplicate completion events.
func TestLinearProgressStepCompletedFailed(t *testing.T) {
	wf := linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	runner := &failingRunner{cause: errors.New("agent refused the task")}
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"first": {Agent: agents.ResolvedAgent{Name: "one"}},
	}, map[string]any{"task": "build"}, "wfr-step-progress-failed", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingProgressSink{}
	if err := ctrl.SetProgressSink(sink); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ctrl.Advance(context.Background()); err == nil {
		t.Fatal("advance of a failing step succeeded")
	}
	completed := 0
	for _, e := range sink.take() {
		if e.Kind == ProgressStepCompleted {
			completed++
			if e.StepID != "first" || e.Detail != "failed" {
				t.Fatalf("step_completed event = %+v", e)
			}
		}
	}
	if completed != 1 {
		t.Fatalf("step_completed events = %d, want exactly 1", completed)
	}
}

// TestLinearProgressFailAttemptEmitsStepCompletedFailed: failAttempt completes
// the attempt as Failed, so it must emit exactly one ProgressStepCompleted
// with the failed status carrying the attempt's child identity.
func TestLinearProgressFailAttemptEmitsStepCompletedFailed(t *testing.T) {
	ctx := context.Background()
	wf := linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, map[string]StepRuntime{
		"first": {Agent: agents.ResolvedAgent{Name: "one"}},
	}, map[string]any{"task": "build"}, "wfr-fail-attempt-progress", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingProgressSink{}
	if err := ctrl.SetProgressSink(sink); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{
		AttemptID: "wfa-first-1", RunID: ctrl.RunID, StepID: "first", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning, CoordinatorRunID: "coord-1", TaskID: "task-1",
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetStepAttempt(ctx, ctrl.RunID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ctrl.failAttempt(ctx, run, stored, errors.New("request build failed")); err == nil {
		t.Fatal("failAttempt returned nil error")
	}
	completed := 0
	for _, e := range sink.take() {
		if e.Kind == ProgressStepCompleted {
			completed++
			if e.StepID != "first" || e.Detail != "failed" || e.TaskID != "task-1" || e.AttemptNo != 1 {
				t.Fatalf("step_completed event = %+v", e)
			}
		}
	}
	if completed != 1 {
		t.Fatalf("step_completed events = %d, want exactly 1", completed)
	}
}
