package agenttools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
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

// seedFailedAttemptWithErrorText mirrors seedFailedAttempt's shape but stores
// the error text under a content-addressed sha256 ref (the delivery error
// path) and records that ref on the failed attempt. When errorText is empty no
// content is stored, so the ref is dangling (the content-missing case).
func seedFailedAttemptWithErrorText(t *testing.T, repo workflowledger.Repository, runID string, errorText string) string {
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
	ref := "sha256:" + workflowledger.DigestHex([]byte(errorText))
	if errorText != "" {
		if err := repo.StoreContent(ctx, ref, []byte(errorText)); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := repo.GetStepAttempt(ctx, runID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteStepAttempt(ctx, runID, attempt.AttemptID, stored.Version, workflowledger.AttemptOutcome{
		Status: workflowledger.AttemptStatusFailed, ErrorRef: ref,
		ToStepID: "failure", TransitionIndex: -1,
		CoordinatorRunID: "coord-fail", TaskID: "task-fail",
	}); err != nil {
		t.Fatal(err)
	}
	return ref
}

// TestFailedAttemptErrorTextSurfaces pins Fix C: workflow_inspect surfaces the
// delivery error TEXT (the stored body of the attempt's ErrorRef), so a repair
// agent can read a delivery-stage rejection instead of an unresolvable digest.
func TestFailedAttemptErrorTextSurfaces(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-err-text-1"
	const errorText = "pre-commit hook rejected: HARD function LOC: internal/ledger/close_run_atomicity_test.go L3-L125 (123 lines, hard max 120). Extract helpers."
	ref := seedFailedAttemptWithErrorText(t, repo, runID, errorText)
	svc := testService(t, repo, nil)

	inspectOut, err := findTool(t, svc, agenttools.ToolWorkflowInspect).Execute(
		context.Background(), json.RawMessage(`{"run_id":"`+runID+`","step":"one","attempt":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var inspect agenttools.InspectView
	if err := json.Unmarshal([]byte(inspectOut), &inspect); err != nil {
		t.Fatal(err)
	}
	if inspect.ErrorRef != ref {
		t.Fatalf("inspect error_ref = %q, want %q", inspect.ErrorRef, ref)
	}
	if inspect.ErrorText != errorText {
		t.Fatalf("inspect error_text = %q, want the stored body %q", inspect.ErrorText, errorText)
	}
}

// TestFailedAttemptErrorTextAbsentWhenContentMissing pins the fail-soft path:
// an ErrorRef with no stored body yields an empty ErrorText, never a tool
// error.
func TestFailedAttemptErrorTextAbsentWhenContentMissing(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-err-text-missing"
	ref := seedFailedAttemptWithErrorText(t, repo, runID, "")
	svc := testService(t, repo, nil)

	inspectOut, err := findTool(t, svc, agenttools.ToolWorkflowInspect).Execute(
		context.Background(), json.RawMessage(`{"run_id":"`+runID+`","step":"one","attempt":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var inspect agenttools.InspectView
	if err := json.Unmarshal([]byte(inspectOut), &inspect); err != nil {
		t.Fatal(err)
	}
	if inspect.ErrorRef != ref {
		t.Fatalf("inspect error_ref = %q, want %q", inspect.ErrorRef, ref)
	}
	if inspect.ErrorText != "" {
		t.Fatalf("inspect error_text = %q, want empty when content is missing", inspect.ErrorText)
	}
}

// TestFailedAttemptErrorTextRedacted pins that the surfaced error text passes
// through the workspace redaction policy (redact.Text), so a secret inside a
// delivery rejection never reaches the agent.
func TestFailedAttemptErrorTextRedacted(t *testing.T) {
	previous := redact.Current()
	policy, err := redact.Compile([]string{`sk-[A-Za-z0-9]+`}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(previous) })

	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-err-text-redact"
	const body = "pre-commit hook rejected: HARD function LOC at internal/x_test.go; token sk-live-secret leaked"
	seedFailedAttemptWithErrorText(t, repo, runID, body)
	svc := testService(t, repo, nil)

	inspectOut, err := findTool(t, svc, agenttools.ToolWorkflowInspect).Execute(
		context.Background(), json.RawMessage(`{"run_id":"`+runID+`","step":"one","attempt":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var inspect agenttools.InspectView
	if err := json.Unmarshal([]byte(inspectOut), &inspect); err != nil {
		t.Fatal(err)
	}
	want := redact.Text(body)
	if inspect.ErrorText != want {
		t.Fatalf("inspect error_text = %q, want redacted %q", inspect.ErrorText, want)
	}
	if strings.Contains(inspect.ErrorText, "sk-live-secret") {
		t.Fatalf("error_text leaks the secret: %q", inspect.ErrorText)
	}
}
