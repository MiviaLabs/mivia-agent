package agenttools_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// seedFailedAttempt creates a run with one failed attempt carrying an error
// reference, mirroring seedRunningAttempt's shape.
func seedFailedAttempt(t *testing.T, repo workflowledger.Repository, runID string) {
	t.Helper()
	ctx := context.Background()
	snapshot, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("name=x"), DefinitionDigest: "digest",
		Inputs: map[string]string{"task": "build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: "two-step", WorkflowDigest: "digest",
		SnapshotDigest: workflowledger.SnapshotDigest(snapshot),
		InputDigest:    workflowledger.InputDigest(map[string]string{"task": "build"}),
		Status:         workflowledger.RunStatusPending, ActiveStepID: "one",
		StartedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
	}, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, 1, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{
		AttemptID: "wfa-one-1", RunID: runID, StepID: "one", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning,
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetStepAttempt(ctx, runID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteStepAttempt(ctx, runID, attempt.AttemptID, stored.Version, workflowledger.AttemptOutcome{
		Status: workflowledger.AttemptStatusFailed, ErrorRef: "sha256:errorbody",
		ToStepID: "failure", TransitionIndex: -1,
		CoordinatorRunID: "coord-fail", TaskID: "task-fail",
	}); err != nil {
		t.Fatal(err)
	}
}

// TestFailedAttemptErrorRefSurfaces pins that workflow_status and
// workflow_inspect expose the error reference of a failed attempt.
func TestFailedAttemptErrorRefSurfaces(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-err-1"
	seedFailedAttempt(t, repo, runID)
	svc := testService(t, repo, nil)

	statusOut, err := findTool(t, svc, agenttools.ToolWorkflowStatus).Execute(
		context.Background(), json.RawMessage(`{"run_id":"`+runID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var status agenttools.StatusView
	if err := json.Unmarshal([]byte(statusOut), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Attempts) != 1 || status.Attempts[0].ErrorRef != "sha256:errorbody" {
		t.Fatalf("status attempt error_ref = %+v, want sha256:errorbody", status.Attempts)
	}

	inspectOut, err := findTool(t, svc, agenttools.ToolWorkflowInspect).Execute(
		context.Background(), json.RawMessage(`{"run_id":"`+runID+`","step":"one","attempt":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var inspect agenttools.InspectView
	if err := json.Unmarshal([]byte(inspectOut), &inspect); err != nil {
		t.Fatal(err)
	}
	if inspect.ErrorRef != "sha256:errorbody" {
		t.Fatalf("inspect error_ref = %q, want sha256:errorbody", inspect.ErrorRef)
	}
	if inspect.Output != nil {
		t.Fatalf("failed attempt must carry no output, got %v", inspect.Output)
	}
}
