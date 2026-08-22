package clichat

// Context-cooperative stack merge waits (this wave): the merge wait loops poll
// with bare time.Sleep(20s), so the bounded driveCtx a session auto-delivery
// attempt passes (workflowAutoDeliveryAttemptTimeout) can never actually stop
// a stuck drive: a stuck merge-queue poll holds the plan run's execution flock
// indefinitely (the observed wedge). These tests pin the fix: a
// cancelled/expired context must make the wait loops return promptly with the
// cancellation error instead of sleeping through their poll forever.

import (
	"bytes"
	"context"
	"errors"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// neverMergedChecker is a MergeChecker stub that never reports a merge, so the
// wait loops keep polling instead of completing.
type neverMergedChecker struct{}

func (neverMergedChecker) Merged(context.Context, string, string, string, string, bool) (bool, error) {
	return false, nil
}

// TestWaitForChunkMergesHonorsCancelledContext pins the fix: a pre-cancelled
// context must make waitForChunkMerges return promptly with the cancellation
// error instead of sleeping through its 20s poll forever (which would hold the
// plan run's execution flock indefinitely).
func TestWaitForChunkMergesHonorsCancelledContext(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := workflowledger.NewMemoryStore()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled: the wait must return immediately
	done := make(chan error, 1)
	go func() {
		done <- waitForChunkMerges(ctx, &cliworkflow.PreparedWorkflowRun{Repo: repo}, ledger, neverMergedChecker{}, "stack-ctx", []ChunkPlan{{ID: "c1"}}, "", io.Discard, io.Discard)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("waitForChunkMerges returned nil for a cancelled context; want the cancellation error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waitForChunkMerges error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitForChunkMerges did not return after its context was cancelled; the wait loop is not context-cooperative")
	}
}

// TestWaitForIntegrationMergeHonorsCancelledContext pins the same fix on the
// integration-PR wait: a pre-cancelled context must return promptly even while
// the integration branch is still unmerged.
func TestWaitForIntegrationMergeHonorsCancelledContext(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	// A delivery_pending integration run with a pushed head branch that never
	// merges keeps the poll loop alive until the context stops it.
	run := workflowledger.RunSnapshot{
		RunID: "wfr-int-ctx", InvocationKey: "stack-ctx:" + stackIntegrationChunkID,
		WorkflowName: "stacked", WorktreeName: "wt-int-ctx",
	}
	seedDeliveryPendingRun(t, repo, run, []byte("{}"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled: the wait must return immediately
	prepared := &cliworkflow.PreparedWorkflowRun{Repo: repo}
	done := make(chan error, 1)
	go func() {
		done <- waitForIntegrationMerge(ctx, prepared, repo, neverMergedChecker{}, "stack-ctx", "approve", io.Discard, io.Discard)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("waitForIntegrationMerge returned nil for a cancelled context; want the cancellation error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waitForIntegrationMerge error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitForIntegrationMerge did not return after its context was cancelled; the wait loop is not context-cooperative")
	}
}

// TestBlockedByUnmergedDependentBlocksParentUntilFollowUpMerges pins a live
// e2e finding: a diff-size split's follow-up PR is based on its parent
// chunk's own branch (delivery.EnsureFollowUpPublished), not master.
// Squash-merging the parent first deletes that base branch; GitHub does not
// reliably retarget the follow-up PR onto master, and it closes unmerged,
// orphaning its content. autoMergePublishedChunks must never merge a chunk
// while a task that depends on it (its follow-up) has not merged yet.
func TestBlockedByUnmergedDependentBlocksParentUntilFollowUpMerges(t *testing.T) {
	byID := map[string]workflowledger.Task{
		"c1":          {ID: "c1", Status: stackStatusPublished},
		"c1-deferred": {ID: "c1-deferred", Status: stackStatusPublished, Deps: []string{"c1"}},
	}
	if blocker, blocked := blockedByUnmergedDependent(byID, "c1"); !blocked || blocker != "c1-deferred" {
		t.Fatalf("blockedByUnmergedDependent(c1) = (%q, %v), want (\"c1-deferred\", true)", blocker, blocked)
	}
	// The follow-up itself has no dependents, so it must never be blocked -
	// otherwise nothing could ever merge first and the stack would wedge.
	if _, blocked := blockedByUnmergedDependent(byID, "c1-deferred"); blocked {
		t.Fatal("blockedByUnmergedDependent(c1-deferred) = blocked, want unblocked (it has no dependents)")
	}

	byID["c1-deferred"] = workflowledger.Task{ID: "c1-deferred", Status: stackStatusMerged, Deps: []string{"c1"}}
	if _, blocked := blockedByUnmergedDependent(byID, "c1"); blocked {
		t.Fatal("blockedByUnmergedDependent(c1) = blocked after its follow-up merged, want unblocked")
	}
}

// immediateMergedChecker reports merged=true on the first call so the wait
// loops complete without polling.
type immediateMergedChecker struct{ called int }

func (c *immediateMergedChecker) Merged(context.Context, string, string, string, string, bool) (bool, error) {
	c.called++
	return true, nil
}

// recordingStackDeliverRun records the call and settles the run to succeeded
// with a delivery record so waitIntegrationRunSettled sees pushed evidence.
type recordingStackDeliverRun struct {
	called       bool
	allowPublish bool
	settlePushed bool
	settleNoDiff bool
	returnErr    error
}

func (d *recordingStackDeliverRun) Deliver(ctx context.Context, _ string, _ *config.Resolved, _ *storage.SQLite, repo workflowledger.Repository, runID string, allowPublish, _ bool, _, _ io.Writer) error {
	d.called = true
	d.allowPublish = allowPublish
	if d.returnErr != nil {
		return d.returnErr
	}
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	now := time.Now()
	status := workflowledger.RunStatusSucceeded
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, status, &now); err != nil {
		return err
	}
	if !d.settleNoDiff {
		if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
			RunID: runID, IdempotencyKey: "test-delivery", Status: "succeeded",
			HeadRef: "wf/wt-integration", CommitSHA: "deadbeef",
		}); err != nil {
			return err
		}
	}
	return nil
}

// recordingStackMergePR records every merge request it receives.
type recordingStackMergePR struct {
	called int
	slug   string
	remote string
	draft  bool
	err    error
}

func (m *recordingStackMergePR) Merge(_ context.Context, slug, remoteID string, draft bool) error {
	m.called++
	m.slug = slug
	m.remote = remoteID
	m.draft = draft
	return m.err
}

// fakeFindPRClient is a PRClient stub that returns a fixed PRRef from
// FindByHead so autoMergeOne reaches the merge boundary without a live gh.
type fakeFindPRClient struct {
	ref *delivery.PRRef
}

func (f *fakeFindPRClient) FindByHead(context.Context, string, string) (*delivery.PRRef, error) {
	return f.ref, nil
}

func (f *fakeFindPRClient) Create(context.Context, string, delivery.PRInput) (delivery.PRRef, error) {
	return delivery.PRRef{}, nil
}

func (f *fakeFindPRClient) IsMerged(context.Context, string, string) (bool, error) {
	return false, nil
}

// seedIntegrationRunAdmitted seeds a delivery_pending integration run row with
// the stable admission key <stackID>:integration.
func seedIntegrationRunAdmitted(t *testing.T, repo workflowledger.Repository, stackID string, noWorktree bool) workflowledger.RunSnapshot {
	t.Helper()
	worktreeName := ""
	if !noWorktree {
		worktreeName = "wt-integration"
	}
	run := workflowledger.RunSnapshot{
		RunID: "wfr-" + stackID + "-integration", InvocationKey: stackID + ":" + stackIntegrationChunkID,
		WorkflowName: "stacked", WorktreeName: worktreeName, BaseRef: "main", BaseCommit: "basecommit",
		RemoteURL: "https://github.com/o/r.git",
	}
	seedDeliveryPendingRun(t, repo, run, []byte("{}"))
	return run
}

// TestAutoDeliverReviewedChunksRetriesOrphanedDelivery is the F9 regression
// at the auto-delivery layer: a chunk task reconciled to reviewed with its
// run still delivery_pending (the process that admitted it died mid-delivery)
// is never re-admitted by driveChunk (reviewed is not a pre-admission
// status), so under merge_policy=auto nothing else would ever retry its
// delivery. autoDeliverReviewedChunks must find it and deliver it.
func TestAutoDeliverReviewedChunksRetriesOrphanedDelivery(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := workflowledger.NewMemoryStore()

	stackID := "stack-auto-redeliver"
	seedStackTask(t, ledger, stackID, "a")
	if err := ledger.TransitionTask(stackID, "a", stackStatusReviewed); err != nil {
		t.Fatal(err)
	}
	run := workflowledger.RunSnapshot{
		RunID: "wfr-redeliver", InvocationKey: stackID + ":a",
		WorkflowName: "stacked", WorktreeName: "wt-redeliver", BaseRef: "main",
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "compile"}})
	if err != nil {
		t.Fatal(err)
	}
	seedDeliveryPendingRun(t, repo, run, snapshotJSON)

	deliver := &recordingStackDeliverRun{}
	prevDeliver := workflowStackDeliverRun
	t.Cleanup(func() { workflowStackDeliverRun = prevDeliver })
	workflowStackDeliverRun = deliver.Deliver

	prepared := &cliworkflow.PreparedWorkflowRun{Repo: repo}
	var stdout bytes.Buffer
	if err := autoDeliverReviewedChunks(context.Background(), prepared, repo, ledger, stackID, &stdout, io.Discard); err != nil {
		t.Fatalf("autoDeliverReviewedChunks() error = %v; stdout = %q", err, stdout.String())
	}

	if !deliver.called {
		t.Fatal("orphaned reviewed chunk's run was never re-delivered")
	}
	if !deliver.allowPublish {
		t.Fatalf("cliworkflow.DeliverRunWithStore allowPublish = %v, want true", deliver.allowPublish)
	}
	task, err := ledger.GetTask(stackID, "a")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != stackStatusPublished {
		t.Fatalf("task status after auto-redelivery = %q, want published", task.Status)
	}
}

// TestDriveStackAutoRedeliverGatesOnPolicy pins driveStackAutoRedeliver's
// wiring for the one-shot `mivia stack drive` command (runStackDrive ->
// driveStack directly, never through driveStackToCompletion's
// chunkMergePollPass): under merge_policy=auto it must retry a reviewed
// chunk's orphaned delivery itself, and under any other policy it must be a
// no-op (the durable grant-only pause is the correct outcome there, not an
// auto-delivery attempt).
func TestDriveStackAutoRedeliverGatesOnPolicy(t *testing.T) {
	for _, tc := range []struct {
		policy     string
		wantCalled bool
	}{
		{"auto", true},
		{"approve", false},
		{"", false},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			repo := workflowledger.NewMemoryRepository()
			t.Cleanup(func() { _ = repo.Close() })
			ledger := workflowledger.NewMemoryStore()

			stackID := "stack-drive-redeliver-" + tc.policy
			if stackID == "stack-drive-redeliver-" {
				stackID = "stack-drive-redeliver-none"
			}
			seedStackTask(t, ledger, stackID, "a")
			if err := ledger.TransitionTask(stackID, "a", stackStatusReviewed); err != nil {
				t.Fatal(err)
			}
			run := workflowledger.RunSnapshot{
				RunID: "wfr-" + stackID, InvocationKey: stackID + ":a",
				WorkflowName: "stacked", WorktreeName: "wt-" + stackID, BaseRef: "main",
			}
			snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "compile"}})
			if err != nil {
				t.Fatal(err)
			}
			seedDeliveryPendingRun(t, repo, run, snapshotJSON)

			deliver := &recordingStackDeliverRun{}
			prevDeliver := workflowStackDeliverRun
			t.Cleanup(func() { workflowStackDeliverRun = prevDeliver })
			workflowStackDeliverRun = deliver.Deliver

			prepared := &cliworkflow.PreparedWorkflowRun{Repo: repo}
			if err := driveStackAutoRedeliver(context.Background(), prepared, ledger, stackID, tc.policy, io.Discard, io.Discard); err != nil {
				t.Fatalf("driveStackAutoRedeliver() error = %v", err)
			}
			if deliver.called != tc.wantCalled {
				t.Fatalf("delivered = %v, want %v for policy %q", deliver.called, tc.wantCalled, tc.policy)
			}
		})
	}
}

// TestWaitIntegrationRunSettledAutoPolicyDeliversWithoutAllowPublish pins the
// fix for the confirmed infinite hang: under merge_policy=auto the integration
// run is delivered even when the caller passes allowPublish=false, and the
// auto-merge that follows finds the newly-created PR and completes.
func TestWaitIntegrationRunSettledAutoPolicyDeliversWithoutAllowPublish(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := workflowledger.NewMemoryStore()

	stackID := "stack-auto-publish"
	seedIntegrationRunAdmitted(t, repo, stackID, false)

	deliver := &recordingStackDeliverRun{}
	prevDeliver := workflowStackDeliverRun
	t.Cleanup(func() { workflowStackDeliverRun = prevDeliver })
	workflowStackDeliverRun = deliver.Deliver

	merges := &recordingStackMergePR{}
	prevMerge := workflowStackMergePR
	t.Cleanup(func() { workflowStackMergePR = prevMerge })
	workflowStackMergePR = merges.Merge

	prevNewPR := cliworkflow.WorkflowDeliverNewPR
	t.Cleanup(func() { cliworkflow.WorkflowDeliverNewPR = prevNewPR })
	cliworkflow.WorkflowDeliverNewPR = func() delivery.PRClient { return &fakeFindPRClient{ref: &delivery.PRRef{RemoteID: "123"}} }

	checker := &immediateMergedChecker{}
	var stdout bytes.Buffer
	// A real repo whose origin carries the integration head branch, so the
	// overlap guard genuinely evaluates (and passes): this test pins the
	// integration auto-merge path, not the guard.
	root, _ := scratchStackRepo(t)
	gitRun(t, root, "checkout", "-b", "wf/wt-integration")
	gitRun(t, root, "push", "origin", "wf/wt-integration")
	prepared := &cliworkflow.PreparedWorkflowRun{Repo: repo, Root: root, Compiled: &definition.CompiledWorkflow{Name: "test", Delivery: &definition.Delivery{Base: "main"}}}

	if err := waitIntegrationRunSettled(context.Background(), prepared, ledger, checker, stackID, "auto", false, &stdout, io.Discard); err != nil {
		t.Fatalf("waitIntegrationRunSettled() error = %v; stdout = %q", err, stdout.String())
	}

	if !deliver.called {
		t.Fatal("integration run was not delivered under merge_policy=auto without --allow-publish")
	}
	if !deliver.allowPublish {
		t.Fatalf("cliworkflow.DeliverRunWithStore allowPublish = %v, want true", deliver.allowPublish)
	}
	if merges.called == 0 {
		t.Fatal("integration PR was not auto-merged after delivery")
	}
	if !strings.Contains(stdout.String(), "integration PR merged") {
		t.Fatalf("stdout = %q, want integration PR merged message", stdout.String())
	}
}

// TestWaitIntegrationRunSettledAutoPolicyMergesExternallyDeliveredRun pins the
// fix for the externally-delivered integration run: the run already settled
// succeeded before waitIntegrationRunSettled read it, but under auto policy it
// still merges the open PR instead of reporting complete with the branch left
// on origin.
func TestWaitIntegrationRunSettledAutoPolicyMergesExternallyDeliveredRun(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := workflowledger.NewMemoryStore()

	stackID := "stack-auto-external"
	run := seedIntegrationRunAdmitted(t, repo, stackID, false)

	// External delivery: a delivery record exists and the run is succeeded.
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: "external-delivery", Status: "succeeded",
		HeadRef: "wf/wt-integration", CommitSHA: "deadbeef",
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := repo.CompareAndSetRunStatus(context.Background(), run.RunID, stored.Version, workflowledger.RunStatusSucceeded, &now); err != nil {
		t.Fatal(err)
	}

	merges := &recordingStackMergePR{}
	prevMerge := workflowStackMergePR
	t.Cleanup(func() { workflowStackMergePR = prevMerge })
	workflowStackMergePR = merges.Merge

	prevNewPR := cliworkflow.WorkflowDeliverNewPR
	t.Cleanup(func() { cliworkflow.WorkflowDeliverNewPR = prevNewPR })
	cliworkflow.WorkflowDeliverNewPR = func() delivery.PRClient { return &fakeFindPRClient{ref: &delivery.PRRef{RemoteID: "123"}} }

	checker := &immediateMergedChecker{}
	var stdout bytes.Buffer
	// A real repo whose origin carries the integration head branch, so the
	// overlap guard genuinely evaluates (and passes): this test pins the
	// integration auto-merge path, not the guard.
	root, _ := scratchStackRepo(t)
	gitRun(t, root, "checkout", "-b", "wf/wt-integration")
	gitRun(t, root, "push", "origin", "wf/wt-integration")
	prepared := &cliworkflow.PreparedWorkflowRun{Repo: repo, Root: root, Compiled: &definition.CompiledWorkflow{Name: "test", Delivery: &definition.Delivery{Base: "main"}}}

	if err := waitIntegrationRunSettled(context.Background(), prepared, ledger, checker, stackID, "auto", false, &stdout, io.Discard); err != nil {
		t.Fatalf("waitIntegrationRunSettled() error = %v; stdout = %q", err, stdout.String())
	}

	if merges.called == 0 {
		t.Fatal("integration PR was not auto-merged when the run was already externally delivered")
	}
}

// TestWaitIntegrationRunSettledNoDiffIntegrationCompletesWithoutMerge pins the
// no-diff completion shape: the integration run settles succeeded but never
// pushes a branch, so there is no PR to merge and the stack must complete
// without entering an infinite poll.
func TestWaitIntegrationRunSettledNoDiffIntegrationCompletesWithoutMerge(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := workflowledger.NewMemoryStore()

	stackID := "stack-auto-nodiff"
	run := seedIntegrationRunAdmitted(t, repo, stackID, true)
	stored, err := repo.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := repo.CompareAndSetRunStatus(context.Background(), run.RunID, stored.Version, workflowledger.RunStatusSucceeded, &now); err != nil {
		t.Fatal(err)
	}

	merges := &recordingStackMergePR{}
	prevMerge := workflowStackMergePR
	t.Cleanup(func() { workflowStackMergePR = prevMerge })
	workflowStackMergePR = merges.Merge

	checker := &immediateMergedChecker{}
	var stdout bytes.Buffer
	prepared := &cliworkflow.PreparedWorkflowRun{Repo: repo}

	if err := waitIntegrationRunSettled(context.Background(), prepared, ledger, checker, stackID, "auto", false, &stdout, io.Discard); err != nil {
		t.Fatalf("waitIntegrationRunSettled() error = %v; stdout = %q", err, stdout.String())
	}

	if merges.called != 0 {
		t.Fatalf("integration PR merge called %d times, want 0 for no-diff completion", merges.called)
	}
	if !strings.Contains(stdout.String(), "no diff") {
		t.Fatalf("stdout = %q, want no-diff completion message", stdout.String())
	}
}

// TestWaitIntegrationRunSettledGrantPolicyPausesForPublishGrant pins the
// non-auto (approve/grant) path: without --allow-publish the integration run
// stays delivery_pending, no merge is attempted, and the user gets the grant
// command.
func TestWaitIntegrationRunSettledGrantPolicyPausesForPublishGrant(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := workflowledger.NewMemoryStore()

	stackID := "stack-grant-pause"
	run := seedIntegrationRunAdmitted(t, repo, stackID, false)

	deliver := &recordingStackDeliverRun{}
	prevDeliver := workflowStackDeliverRun
	t.Cleanup(func() { workflowStackDeliverRun = prevDeliver })
	workflowStackDeliverRun = deliver.Deliver

	merges := &recordingStackMergePR{}
	prevMerge := workflowStackMergePR
	t.Cleanup(func() { workflowStackMergePR = prevMerge })
	workflowStackMergePR = merges.Merge

	checker := &immediateMergedChecker{}
	var stdout bytes.Buffer
	prepared := &cliworkflow.PreparedWorkflowRun{Repo: repo}

	if err := waitIntegrationRunSettled(context.Background(), prepared, ledger, checker, stackID, "approve", false, &stdout, io.Discard); err != nil {
		t.Fatalf("waitIntegrationRunSettled() error = %v; stdout = %q", err, stdout.String())
	}

	if deliver.called {
		t.Fatal("integration run was delivered under grant policy without --allow-publish")
	}
	if merges.called != 0 {
		t.Fatalf("integration PR merge called %d times under grant policy, want 0", merges.called)
	}
	want := "mivia workflow deliver " + run.RunID + " --allow-publish"
	if !strings.Contains(stdout.String(), want) {
		t.Fatalf("stdout = %q, want grant hint %q", stdout.String(), want)
	}
}

// TestWaitIntegrationRunSettledTerminalFailureReportsError pins the guard for
// terminal non-succeeded integration runs: a failed/canceled/timed_out run must
// not be reported as stack complete; the caller gets an error so the stack
// stays resumable/repairable.
func TestWaitIntegrationRunSettledTerminalFailureReportsError(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := workflowledger.NewMemoryStore()

	stackID := "stack-terminal-fail"
	run := seedIntegrationRunAdmitted(t, repo, stackID, false)
	stored, err := repo.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := repo.CompareAndSetRunStatus(context.Background(), run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	stored, err = repo.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), run.RunID, stored.Version, workflowledger.RunStatusFailed, &now); err != nil {
		t.Fatal(err)
	}

	merges := &recordingStackMergePR{}
	prevMerge := workflowStackMergePR
	t.Cleanup(func() { workflowStackMergePR = prevMerge })
	workflowStackMergePR = merges.Merge

	checker := &immediateMergedChecker{}
	prepared := &cliworkflow.PreparedWorkflowRun{Repo: repo}

	err = waitIntegrationRunSettled(context.Background(), prepared, ledger, checker, stackID, "auto", false, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("waitIntegrationRunSettled returned nil for a failed integration run; want an error")
	}
	if !strings.Contains(err.Error(), "failed") {
		t.Fatalf("error = %v, want it to mention the failed status", err)
	}
	if merges.called != 0 {
		t.Fatalf("integration PR merge called %d times, want 0 for a failed run", merges.called)
	}
}

// TestAutoMergeOnePermanentErrorHaltPoll pins F12 finding 4: a permanent
// merge failure (PR closed, auth broken, branch deleted) must halt the poll
// instead of being swallowed as "not mergeable yet".
func TestAutoMergeOnePermanentErrorHaltPoll(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := workflowledger.NewMemoryStore()

	stackID := "stack-perm-fail"
	seedStackTask(t, ledger, stackID, "c1")
	if err := ledger.TransitionTask(stackID, "c1", stackStatusPublished); err != nil {
		t.Fatal(err)
	}
	run := workflowledger.RunSnapshot{
		RunID: "wfr-perm-fail-c1", InvocationKey: stackID + ":c1",
		WorkflowName: "stacked", WorktreeName: "wt-perm-fail", BaseRef: "main",
		RemoteURL: "https://github.com/o/r.git",
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "fix"}})
	if err != nil {
		t.Fatal(err)
	}
	seedDeliveryPendingRun(t, repo, run, snapshotJSON)
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: "key", Status: "succeeded",
		HeadRef: "wf/wt-perm-fail", CommitSHA: "sha",
	}); err != nil {
		t.Fatal(err)
	}

	merges := &recordingStackMergePR{err: errors.New("merge PR 123: pull request is closed")}
	prevMerge := workflowStackMergePR
	t.Cleanup(func() { workflowStackMergePR = prevMerge })
	workflowStackMergePR = merges.Merge

	prevNewPR := cliworkflow.WorkflowDeliverNewPR
	t.Cleanup(func() { cliworkflow.WorkflowDeliverNewPR = prevNewPR })
	cliworkflow.WorkflowDeliverNewPR = func() delivery.PRClient { return &fakeFindPRClient{ref: &delivery.PRRef{RemoteID: "123"}} }

	// A real repo whose origin carries the chunk's head branch, so the
	// overlap guard genuinely evaluates (and passes) instead of failing its
	// probe: this test pins the MERGE-error classification, not the guard.
	root, _ := scratchStackRepo(t)
	gitRun(t, root, "checkout", "-b", "wf/wt-perm-fail")
	gitRun(t, root, "push", "origin", "wf/wt-perm-fail")
	prepared := &cliworkflow.PreparedWorkflowRun{Repo: repo, Root: root, Compiled: &definition.CompiledWorkflow{Name: "test", Delivery: &definition.Delivery{Base: "main"}}}
	var stdout bytes.Buffer
	err = autoMergeOne(context.Background(), prepared, repo, stackID, "c1", &stdout, io.Discard)
	if err == nil {
		t.Fatal("autoMergeOne returned nil for permanent merge failure; want error")
	}
	if !strings.Contains(err.Error(), "permanent merge failure") {
		t.Fatalf("error = %v, want 'permanent merge failure'", err)
	}
}

// TestAutoMergeOneRetriableErrorKeepsPolling pins that retriable merge
// errors (pending CI, review requirements) are still swallowed and the poll
// continues after the permanent-error fix.
func TestAutoMergeOneRetriableErrorKeepsPolling(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := workflowledger.NewMemoryStore()

	stackID := "stack-retry-ok"
	seedStackTask(t, ledger, stackID, "c1")
	if err := ledger.TransitionTask(stackID, "c1", stackStatusPublished); err != nil {
		t.Fatal(err)
	}
	run := workflowledger.RunSnapshot{
		RunID: "wfr-retry-c1", InvocationKey: stackID + ":c1",
		WorkflowName: "stacked", WorktreeName: "wt-retry", BaseRef: "main",
		RemoteURL: "https://github.com/o/r.git",
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "fix"}})
	if err != nil {
		t.Fatal(err)
	}
	seedDeliveryPendingRun(t, repo, run, snapshotJSON)
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: "key", Status: "succeeded",
		HeadRef: "wf/wt-retry", CommitSHA: "sha",
	}); err != nil {
		t.Fatal(err)
	}

	merges := &recordingStackMergePR{err: errors.New("merge PR 123: pull request review is required")}
	prevMerge := workflowStackMergePR
	t.Cleanup(func() { workflowStackMergePR = prevMerge })
	workflowStackMergePR = merges.Merge

	prevNewPR := cliworkflow.WorkflowDeliverNewPR
	t.Cleanup(func() { cliworkflow.WorkflowDeliverNewPR = prevNewPR })
	cliworkflow.WorkflowDeliverNewPR = func() delivery.PRClient { return &fakeFindPRClient{ref: &delivery.PRRef{RemoteID: "123"}} }

	// A real repo whose origin carries the chunk's head branch, so the
	// overlap guard genuinely evaluates (and passes) instead of failing its
	// probe: this test pins the MERGE-error classification, not the guard.
	root, _ := scratchStackRepo(t)
	gitRun(t, root, "checkout", "-b", "wf/wt-retry")
	gitRun(t, root, "push", "origin", "wf/wt-retry")
	prepared := &cliworkflow.PreparedWorkflowRun{Repo: repo, Root: root, Compiled: &definition.CompiledWorkflow{Name: "test", Delivery: &definition.Delivery{Base: "main"}}}
	var stdout bytes.Buffer
	err = autoMergeOne(context.Background(), prepared, repo, stackID, "c1", &stdout, io.Discard)
	if err != nil {
		t.Fatalf("autoMergeOne returned error for retriable failure: %v; want nil (keep polling)", err)
	}
	if !strings.Contains(stdout.String(), "not mergeable yet") {
		t.Fatalf("stdout = %q, want 'not mergeable yet'", stdout.String())
	}
}

// TestAutoMergeOneOverlapProbeFailureSkipsMerge pins the F12 overlap-probe
// degradation: when the overlap guard cannot run (git fetch/probe failure),
// autoMergeOne must NOT merge the PR that pass - an unevaluated guard is not
// a passed guard - but must also not halt the whole drive: the pass keeps
// polling and the reason stays visible in the drive output, so a transient
// probe failure heals on the next poll and a persistent one names itself.
func TestAutoMergeOneOverlapProbeFailureSkipsMerge(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := workflowledger.NewMemoryStore()

	stackID := "stack-overlap-probe"
	seedStackTask(t, ledger, stackID, "c1")
	if err := ledger.TransitionTask(stackID, "c1", stackStatusPublished); err != nil {
		t.Fatal(err)
	}
	run := workflowledger.RunSnapshot{
		RunID: "wfr-overlap-probe-c1", InvocationKey: stackID + ":c1",
		WorkflowName: "stacked", WorktreeName: "wt-overlap-probe", BaseRef: "main",
		RemoteURL: "https://github.com/o/r.git",
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "fix"}})
	if err != nil {
		t.Fatal(err)
	}
	seedDeliveryPendingRun(t, repo, run, snapshotJSON)
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: "key", Status: "succeeded",
		HeadRef: "wf/wt-overlap-probe", CommitSHA: "sha",
	}); err != nil {
		t.Fatal(err)
	}

	merges := &recordingStackMergePR{}
	prevMerge := workflowStackMergePR
	t.Cleanup(func() { workflowStackMergePR = prevMerge })
	workflowStackMergePR = merges.Merge

	prevNewPR := cliworkflow.WorkflowDeliverNewPR
	t.Cleanup(func() { cliworkflow.WorkflowDeliverNewPR = prevNewPR })
	cliworkflow.WorkflowDeliverNewPR = func() delivery.PRClient { return &fakeFindPRClient{ref: &delivery.PRRef{RemoteID: "123"}} }

	prevGit := cliworkflow.WorkflowDeliverGit
	t.Cleanup(func() { cliworkflow.WorkflowDeliverGit = prevGit })
	cliworkflow.WorkflowDeliverGit = errorGitRunner{err: errors.New("test: fetch failed")}

	prepared := &cliworkflow.PreparedWorkflowRun{Repo: repo, Root: t.TempDir(), Compiled: &definition.CompiledWorkflow{Name: "test", Delivery: &definition.Delivery{Base: "main"}}}
	var stdout bytes.Buffer
	if err := autoMergeOne(context.Background(), prepared, repo, stackID, "c1", &stdout, io.Discard); err != nil {
		t.Fatalf("autoMergeOne errored on an overlap probe failure: %v; want keep-polling nil", err)
	}
	if merges.called != 0 {
		t.Fatalf("merge called %d times past an unevaluated overlap guard; want 0", merges.called)
	}
	if !strings.Contains(stdout.String(), "overlap guard") {
		t.Fatalf("stdout = %q, want the probe-failure reason to stay visible", stdout.String())
	}
}
