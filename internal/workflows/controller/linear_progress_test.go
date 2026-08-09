package controller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// resumeCountingRepository wraps a Repository and records RecordRunResumed
// calls, so tests can assert a resumed StartNew records the resume event.
type resumeCountingRepository struct {
	workflowledger.Repository
	resumed []string
}

// RecordRunResumed records the call and forwards it to the wrapped repo.
func (r *resumeCountingRepository) RecordRunResumed(ctx context.Context, runID string) error {
	r.resumed = append(r.resumed, runID)
	return r.Repository.RecordRunResumed(ctx, runID)
}

// failingResumeRepository wraps a Repository and fails every RecordRunResumed
// call, so tests can assert the resume path stays best-effort.
type failingResumeRepository struct {
	workflowledger.Repository
	resumed []string
}

// RecordRunResumed records the call and returns a permanent error.
func (r *failingResumeRepository) RecordRunResumed(ctx context.Context, runID string) error {
	r.resumed = append(r.resumed, runID)
	return errors.New("resume record unavailable")
}

// TestSetProgressSinkBeforeStart: SetProgressSink must follow the SetVerifiers
// contract - allowed before Start, rejected after Start.
func TestSetProgressSinkBeforeStart(t *testing.T) {
	ctrl, err := NewLinearController(workflowledger.NewMemoryRepository(), &linearRunner{}, linearWorkflow(t), nil, nil, "wfr-sink", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetProgressSink(&recordingProgressSink{}); err != nil {
		t.Fatalf("SetProgressSink before start = %v, want nil", err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetProgressSink(&recordingProgressSink{}); err == nil {
		t.Fatal("SetProgressSink after start succeeded")
	}
}

// TestControllerRunEmitsRunFinished: a run to terminal success must emit one
// ProgressRunFinished event carrying the controller run ID.
func TestControllerRunEmitsRunFinished(t *testing.T) {
	wf := linearWorkflow(t)
	runner := &linearRunner{outputs: map[string]json.RawMessage{"first": json.RawMessage(`{"ok":true}`), "second": json.RawMessage(`{"done":true}`)}}
	repo := workflowledger.NewMemoryRepository()
	snapshot, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{SchemaVersion: 1, DefinitionTOML: []byte("linear"), DefinitionDigest: wf.Digest})
	if err != nil {
		t.Fatal(err)
	}
	steps := map[string]StepRuntime{
		"first":  {Agent: agents.ResolvedAgent{Name: "one"}},
		"second": {Agent: agents.ResolvedAgent{Name: "two"}},
	}
	ctrl, err := NewLinearController(repo, runner, wf, steps, map[string]any{"task": "build"}, "wfr-progress-run", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingProgressSink{}
	if err := ctrl.SetProgressSink(sink); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v, err = %v, want succeeded", got, err)
	}
	events := sink.take()
	finished := 0
	for _, e := range events {
		if e.Kind == ProgressRunFinished {
			finished++
			if e.RunID != ctrl.RunID {
				t.Fatalf("run_finished run ID = %q, want %q", e.RunID, ctrl.RunID)
			}
			if e.Detail != "succeeded" {
				t.Fatalf("run_finished detail = %q, want succeeded", e.Detail)
			}
		}
		if e.Kind == ProgressRunFailed {
			t.Fatal("run_failed emitted on a successful run")
		}
	}
	if finished != 1 {
		t.Fatalf("run_finished events = %d, want 1", finished)
	}
}

// TestControllerRunEmitsRunFailed: a run canceled by its child must emit
// ProgressRunFailed with the cause detail through failWithStatus.
func TestControllerRunEmitsRunFailed(t *testing.T) {
	wf := linearWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, canceledLinearRunner{}, wf, map[string]StepRuntime{
		"first": {Agent: agents.ResolvedAgent{Name: "one"}},
	}, map[string]any{"task": "build"}, "wfr-progress-fail", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingProgressSink{}
	if err := ctrl.SetProgressSink(sink); err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v, want context.Canceled", err)
	}
	events := sink.take()
	failed := 0
	for _, e := range events {
		if e.Kind == ProgressRunFailed {
			failed++
			if e.RunID != ctrl.RunID {
				t.Fatalf("run_failed run ID = %q, want %q", e.RunID, ctrl.RunID)
			}
			if e.Detail == "" {
				t.Fatal("run_failed detail is empty")
			}
		}
	}
	if failed != 1 {
		t.Fatalf("run_failed events = %d, want 1", failed)
	}
}

// TestHumanGateParkEmitsNoRunFinishedThenOneOnCompletion: parking a run at
// waiting_approval must NOT emit run_finished (a park is not a finish), and
// completing the approval must emit exactly one run_finished(succeeded).
func TestHumanGateParkEmitsNoRunFinishedThenOneOnCompletion(t *testing.T) {
	wf := humanGateProgressWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	sink := &recordingProgressSink{}
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, "wfr-run-finished-park", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetProgressSink(sink); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("run = %+v, err = %v, want waiting_approval", got, err)
	}
	for _, e := range sink.take() {
		if e.Kind == ProgressRunFinished {
			t.Fatalf("run_finished emitted while parked at waiting_approval: %+v", e)
		}
	}
	if err := ctrl.Approve(context.Background(), PendingApprovalID("approve_me", 1), "operator"); err != nil {
		t.Fatal(err)
	}
	got, err = ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("after approve = %+v, err = %v, want succeeded", got, err)
	}
	finished := 0
	for _, e := range sink.take() {
		if e.Kind == ProgressRunFinished {
			finished++
			if e.Detail != "succeeded" {
				t.Fatalf("run_finished detail = %q, want succeeded", e.Detail)
			}
		}
	}
	if finished != 1 {
		t.Fatalf("run_finished events = %d, want exactly 1", finished)
	}
}

// TestStartNewResumeRecordsRunResumed: a second StartNew on the same run must
// return created=false and record the resume event on the repository.
func TestStartNewResumeRecordsRunResumed(t *testing.T) {
	wf := linearWorkflow(t)
	base := workflowledger.NewMemoryRepository()
	repo := &resumeCountingRepository{Repository: base}
	first, err := NewLinearController(repo, &linearRunner{}, wf, nil, nil, "wfr-resume-record", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	created, err := first.StartNew(context.Background())
	if err != nil || !created {
		t.Fatalf("first start = %v, created = %v, want created", err, created)
	}
	second, err := NewLinearController(repo, &linearRunner{}, wf, nil, nil, "wfr-resume-record", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	created, err = second.StartNew(context.Background())
	if err != nil || created {
		t.Fatalf("resume start = %v, created = %v, want created=false", err, created)
	}
	if len(repo.resumed) != 1 || repo.resumed[0] != "wfr-resume-record" {
		t.Fatalf("RecordRunResumed calls = %v, want [wfr-resume-record]", repo.resumed)
	}
}

// TestStartNewResumeSwallowsRecordError: a failing RecordRunResumed must not
// fail StartNew - the resume record is best-effort.
func TestStartNewResumeSwallowsRecordError(t *testing.T) {
	wf := linearWorkflow(t)
	base := workflowledger.NewMemoryRepository()
	repo := &failingResumeRepository{Repository: base}
	first, err := NewLinearController(repo, &linearRunner{}, wf, nil, nil, "wfr-resume-error", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.StartNew(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := NewLinearController(repo, &linearRunner{}, wf, nil, nil, "wfr-resume-error", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	created, err := second.StartNew(context.Background())
	if err != nil || created {
		t.Fatalf("resume start = %v, created = %v, want success with created=false", err, created)
	}
	if len(repo.resumed) != 1 {
		t.Fatalf("RecordRunResumed calls = %d, want 1", len(repo.resumed))
	}
}
