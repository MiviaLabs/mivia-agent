package cli

// F7 regression coverage: an orphaned mid-flight chunk run must not wedge
// the stack forever. driveChunkResumedOutcome is the part of the self-heal
// path (driveChunkInFlight) that is a plain function over already-open
// state, so it is covered directly without standing up a full controller.
// GAP 1 adds integration-run self-heal coverage through the real
// driveIntegrationRun path.

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

func TestDriveChunkResumedOutcomeSucceededNoDiffMarksMerged(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := tasks.NewMemoryStore()
	stackID := "stack-resume-nodiff"
	seedStackTask(t, ledger, stackID, "a")

	run := workflowledger.RunSnapshot{
		RunID: "wfr-resume-nodiff", InvocationKey: stackID + ":a",
		WorkflowName: "stacked", WorktreeName: "wt-resume-nodiff", BaseRef: "main",
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "compile"}})
	if err != nil {
		t.Fatal(err)
	}
	seedDeliveryPendingRun(t, repo, run, snapshotJSON)
	settleRunToSucceeded(t, repo, run.RunID)
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: "key-resume-nodiff", Status: "no_diff",
		BaseRef: "main", HeadRef: "wf/wt-resume-nodiff", Provider: "github",
	}); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	prepared := &preparedWorkflowRun{repo: repo}
	halt, err := driveChunkResumedOutcome(context.Background(), prepared, ledger, stackID, "a", run.RunID, &stdout)
	if err != nil {
		t.Fatalf("driveChunkResumedOutcome: %v", err)
	}
	if !halt {
		t.Fatal("halt = false, want true (one chunk settle per drive pass)")
	}
	task, err := ledger.GetTask(stackID, "a")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != stackStatusMerged {
		t.Fatalf("task status = %q, want merged", task.Status)
	}
}

func TestDriveChunkResumedOutcomeDeliveryPendingAwaitsGrant(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := tasks.NewMemoryStore()
	stackID := "stack-resume-pending"
	seedStackTask(t, ledger, stackID, "a")

	run := workflowledger.RunSnapshot{
		RunID: "wfr-resume-pending", InvocationKey: stackID + ":a",
		WorkflowName: "stacked", WorktreeName: "wt-resume-pending", BaseRef: "main",
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "compile"}})
	if err != nil {
		t.Fatal(err)
	}
	seedDeliveryPendingRun(t, repo, run, snapshotJSON)

	var stdout bytes.Buffer
	prepared := &preparedWorkflowRun{repo: repo}
	halt, err := driveChunkResumedOutcome(context.Background(), prepared, ledger, stackID, "a", run.RunID, &stdout)
	if err != nil {
		t.Fatalf("driveChunkResumedOutcome: %v", err)
	}
	if !halt {
		t.Fatal("halt = false, want true")
	}
	task, err := ledger.GetTask(stackID, "a")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != stackStatusReviewed {
		t.Fatalf("task status = %q, want reviewed (resume settled at delivery_pending without publishing)", task.Status)
	}
}

func TestDriveChunkResumedOutcomeFailedReopensBounded(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := tasks.NewMemoryStore()
	stackID := "stack-resume-failed"
	seedStackTask(t, ledger, stackID, "a")

	run := workflowledger.RunSnapshot{
		RunID: "wfr-resume-failed", InvocationKey: stackID + ":a",
		WorkflowName: "stacked", WorktreeName: "wt-resume-failed", BaseRef: "main",
		Status: workflowledger.RunStatusPending,
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "compile"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(context.Background(), run, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	stored, err = repo.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), run.RunID, stored.Version, workflowledger.RunStatusFailed, nil); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	prepared := &preparedWorkflowRun{repo: repo}
	halt, err := driveChunkResumedOutcome(context.Background(), prepared, ledger, stackID, "a", run.RunID, &stdout)
	if err != nil {
		t.Fatalf("driveChunkResumedOutcome: %v", err)
	}
	if !halt {
		t.Fatal("halt = false, want true")
	}
	task, err := ledger.GetTask(stackID, "a")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != stackStatusReopened {
		t.Fatalf("task status = %q, want reopened (bounded retry, not a terminal fail on the first attempt)", task.Status)
	}
}

// --- Integration-run self-heal (GAP 1) -----------------------------------

// claimOverrideRepo wraps a workflowledger.Repository and overrides
// GetRunClaim to report a live or stale claim for testing.
type claimOverrideRepo struct {
	workflowledger.Repository
	liveClaim bool
}

func (m *claimOverrideRepo) GetRunClaim(_ context.Context, _ string) (string, time.Time, bool, error) {
	if m.liveClaim {
		return "test-holder", time.Now(), true, nil
	}
	return "", time.Time{}, false, nil
}

// TestDriveIntegrationRunResumesOrphanedRun proves that an orphaned
// integration run (stale or absent claim) is auto-resumed by the drive
// instead of printing "already exists" and returning nil. The MemoryRepository
// has no ClaimReader, so GetRunClaim always returns ok=false (stale claim).
func TestDriveIntegrationRunResumesOrphanedRun(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := tasks.NewMemoryStore()
	stackID := "stack-integration-resume"
	if _, err := ledger.StorePlan(tasks.Plan{ID: stackID, Scope: stackScope(stackID), Schema: stackPlanSchema}); err != nil {
		t.Fatal(err)
	}

	run := workflowledger.RunSnapshot{
		RunID: "wfr-integration-orphan", InvocationKey: stackID + ":" + stackIntegrationChunkID,
		WorkflowName: "mini-stack", BaseRef: "main",
		Status: workflowledger.RunStatusPending,
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "build", "stack_mode": "single"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(context.Background(), run, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}

	resumeCalled := false
	origResume := stackChunkResumeFn
	stackChunkResumeFn = func(runID, root, configPath string, force, allowPub, acceptVerifier, acceptSkillChange bool, stdout, stderr io.Writer) error {
		resumeCalled = true
		return nil
	}
	defer func() { stackChunkResumeFn = origResume }()

	prepared := &preparedWorkflowRun{repo: repo, res: &config.Resolved{}}
	var stdout bytes.Buffer
	if err := driveIntegrationRun(context.Background(), prepared, ledger, stackID, "main", "auto", map[string]string{"task": "build"}, true, &stdout, io.Discard); err != nil {
		t.Fatalf("driveIntegrationRun: %v", err)
	}
	if !resumeCalled {
		t.Fatal("stackChunkResumeFn was not called; the orphaned integration run was not resumed")
	}
	if !strings.Contains(stdout.String(), "resumed") {
		t.Fatalf("output %q must mention the resume", stdout.String())
	}
}

// TestDriveIntegrationRunParksOnLiveClaim proves that an integration run
// held by a live process is NOT auto-resumed: the drive parks with an honest
// status message and leaves the run alone.
func TestDriveIntegrationRunParksOnLiveClaim(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := tasks.NewMemoryStore()
	stackID := "stack-integration-park"
	if _, err := ledger.StorePlan(tasks.Plan{ID: stackID, Scope: stackScope(stackID), Schema: stackPlanSchema}); err != nil {
		t.Fatal(err)
	}

	run := workflowledger.RunSnapshot{
		RunID: "wfr-integration-park", InvocationKey: stackID + ":" + stackIntegrationChunkID,
		WorkflowName: "mini-stack", BaseRef: "main",
		Status: workflowledger.RunStatusPending,
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "build", "stack_mode": "single"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(context.Background(), run, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}

	mockRepo := &claimOverrideRepo{Repository: repo, liveClaim: true}

	resumeCalled := false
	origResume := stackChunkResumeFn
	stackChunkResumeFn = func(runID, root, configPath string, force, allowPub, acceptVerifier, acceptSkillChange bool, stdout, stderr io.Writer) error {
		resumeCalled = true
		return nil
	}
	defer func() { stackChunkResumeFn = origResume }()

	prepared := &preparedWorkflowRun{repo: mockRepo, res: &config.Resolved{}}
	var stdout bytes.Buffer
	if err := driveIntegrationRun(context.Background(), prepared, ledger, stackID, "main", "auto", map[string]string{"task": "build"}, true, &stdout, io.Discard); err != nil {
		t.Fatalf("driveIntegrationRun: %v", err)
	}
	if resumeCalled {
		t.Fatal("stackChunkResumeFn was called on a live-claim integration run; the drive must park instead")
	}
	if !strings.Contains(stdout.String(), "already in flight") {
		t.Fatalf("output %q must mention the run is in flight", stdout.String())
	}
}

// TestDriveIntegrationRunResumedFailureLeavesForCompletion proves that a
// resumed integration run that fails is left in its failed status for
// waitIntegrationRunSettled to handle. driveIntegrationInFlight does no
// task transitions (no task exists for integration), so the failure surfaces
// in the completion pass, not here.
func TestDriveIntegrationRunResumedFailureLeavesForCompletion(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := tasks.NewMemoryStore()
	stackID := "stack-integration-fail"
	if _, err := ledger.StorePlan(tasks.Plan{ID: stackID, Scope: stackScope(stackID), Schema: stackPlanSchema}); err != nil {
		t.Fatal(err)
	}

	run := workflowledger.RunSnapshot{
		RunID: "wfr-integration-fail", InvocationKey: stackID + ":" + stackIntegrationChunkID,
		WorkflowName: "mini-stack", BaseRef: "main",
		Status: workflowledger.RunStatusPending,
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "build", "stack_mode": "single"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(context.Background(), run, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	stored, err = repo.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := repo.CompareAndSetRunStatus(context.Background(), run.RunID, stored.Version, workflowledger.RunStatusFailed, &now); err != nil {
		t.Fatal(err)
	}

	origResume := stackChunkResumeFn
	stackChunkResumeFn = func(runID, root, configPath string, force, allowPub, acceptVerifier, acceptSkillChange bool, stdout, stderr io.Writer) error {
		return nil
	}
	defer func() { stackChunkResumeFn = origResume }()

	prepared := &preparedWorkflowRun{repo: repo, res: &config.Resolved{}}
	var stdout bytes.Buffer
	if err := driveIntegrationRun(context.Background(), prepared, ledger, stackID, "main", "auto", map[string]string{"task": "build"}, true, &stdout, io.Discard); err != nil {
		t.Fatalf("driveIntegrationRun: %v", err)
	}
	fresh, err := repo.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %q, want failed (driveIntegrationInFlight must leave the failure for waitIntegrationRunSettled)", fresh.Status)
	}
}
