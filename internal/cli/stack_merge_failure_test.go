package cli

// Failure-ordering regression tests for the stack merge poll pass. These tests
// pin that terminal chunk failures halt the drive before any auto-merge or
// auto-delivery of remaining chunks can leave partial stack content on the base
// branch.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// TestChunkFailureHaltsBeforeAutoMerge pins the ordering guard: a durably
// failed chunk must be detected before merge_policy=auto merges or delivers
// any remaining published chunks. The prior order auto-merged first and only
// checked failure afterwards, leaving partial stack content on the base branch.
func TestChunkFailureHaltsBeforeAutoMerge(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := tasks.NewMemoryStore()

	stackID := "stack-fail-halts"
	seedStackTask(t, ledger, stackID, "c1")
	if err := ledger.TransitionTask(stackID, "c1", stackStatusPublished); err != nil {
		t.Fatal(err)
	}
	// c2 is already durably failed.
	if err := ledger.CreateTask(tasks.Task{ID: "c2", PlanRef: stackID, Scope: stackScope(stackID), Status: stackStatusFailed}); err != nil {
		t.Fatal(err)
	}

	// c1 has a delivered run with a pushed branch so autoMergeOne would reach
	// the PR merge boundary if it runs before the failure check.
	run := workflowledger.RunSnapshot{
		RunID: "wfr-fail-c1", InvocationKey: stackID + ":c1",
		WorkflowName: "stacked", WorktreeName: "wt-fail-c1", BaseRef: "main",
		RemoteURL: "https://github.com/o/r.git",
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "fix"}})
	if err != nil {
		t.Fatal(err)
	}
	seedDeliveryPendingRun(t, repo, run, snapshotJSON)
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: "key", Status: "succeeded",
		HeadRef: "wf/wt-fail-c1", CommitSHA: "sha",
	}); err != nil {
		t.Fatal(err)
	}

	merges := &recordingStackMergePR{}
	prevMerge := workflowStackMergePR
	t.Cleanup(func() { workflowStackMergePR = prevMerge })
	workflowStackMergePR = merges.Merge

	delivers := &recordingStackDeliverRun{}
	prevDeliver := workflowStackDeliverRun
	t.Cleanup(func() { workflowStackDeliverRun = prevDeliver })
	workflowStackDeliverRun = delivers.Deliver

	prevNewPR := workflowDeliverNewPR
	t.Cleanup(func() { workflowDeliverNewPR = prevNewPR })
	workflowDeliverNewPR = func() delivery.PRClient { return &fakeFindPRClient{ref: &delivery.PRRef{RemoteID: "123"}} }

	// A real repo whose origin carries the chunk's head branch, so the overlap
	// guard genuinely evaluates and would let the merge proceed.
	root, _ := scratchStackRepo(t)
	gitRun(t, root, "checkout", "-b", "wf/wt-fail-c1")
	gitRun(t, root, "push", "origin", "wf/wt-fail-c1")
	prepared := &preparedWorkflowRun{
		repo: repo, root: root,
		compiled: &compiler.CompiledWorkflow{Name: "test", Delivery: &definition.Delivery{Base: "main"}},
	}

	var stdout bytes.Buffer
	_, err = chunkMergePollPass(context.Background(), prepared, ledger, neverMergedChecker{}, stackID, []ChunkPlan{{ID: "c1"}, {ID: "c2"}}, "auto", &stdout, io.Discard)
	if err == nil {
		t.Fatal("chunkMergePollPass returned nil with a durably failed chunk; want halt error")
	}
	if !strings.Contains(err.Error(), "failed terminally") {
		t.Fatalf("error = %v, want it to mention failed terminally", err)
	}
	if merges.called != 0 {
		t.Fatalf("workflowStackMergePR called %d times for c1 before failure halt; want 0", merges.called)
	}
	if delivers.called {
		t.Fatal("workflowStackDeliverRun called before failure halt; want no calls")
	}
}

// TestChunkFailureCancelsDependents pins that a terminal chunk failure cancels
// queued or in-flight dependents before the poll pass returns the halt error,
// so the outer drive loop never admits them later.
func TestChunkFailureCancelsDependents(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := tasks.NewMemoryStore()

	stackID := "stack-fail-cancels"
	seedStackTask(t, ledger, stackID, "c1")
	if err := ledger.TransitionTask(stackID, "c1", stackStatusFailed); err != nil {
		t.Fatal(err)
	}
	if err := ledger.CreateTask(tasks.Task{
		ID: "c2", PlanRef: stackID, Scope: stackScope(stackID),
		Status: stackStatusPlanned, Deps: []string{"c1"},
	}); err != nil {
		t.Fatal(err)
	}

	prepared := &preparedWorkflowRun{
		repo:     repo,
		compiled: &compiler.CompiledWorkflow{Name: "test", Delivery: &definition.Delivery{Base: "main"}},
	}

	_, err := chunkMergePollPass(context.Background(), prepared, ledger, neverMergedChecker{}, stackID, []ChunkPlan{{ID: "c1"}, {ID: "c2"}}, "approve", io.Discard, io.Discard)
	if err == nil {
		t.Fatal("chunkMergePollPass returned nil with a durably failed chunk; want halt error")
	}
	if !strings.Contains(err.Error(), "failed terminally") {
		t.Fatalf("error = %v, want it to mention failed terminally", err)
	}

	c2, err := ledger.GetTask(stackID, "c2")
	if err != nil {
		t.Fatalf("read c2 task: %v", err)
	}
	if c2.Status != stackStatusCanceled {
		t.Fatalf("c2 status = %q, want %q after canceled dependent propagation", c2.Status, stackStatusCanceled)
	}
}

// TestChunkMergePollPassFreshFailureHaltsBeforeAutoMerge pins the OTHER
// failure-detection path chunkMergePollPass must halt on: a chunk that
// reconcileStack ITSELF marks failed on this very pass (a FRESH
// stackActionMarkFailed action, exhausted retries), not one that was already
// durably failed on entry (that path is TestChunkFailureHaltsBeforeAutoMerge
// above). Both must halt before merge_policy=auto's auto-merge/auto-deliver
// block runs.
func TestChunkMergePollPassFreshFailureHaltsBeforeAutoMerge(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := tasks.NewMemoryStore()

	stackID := "stack-fresh-fail-halts"
	seedStackTask(t, ledger, stackID, "c1")
	// Exhaust c1's retry budget (stackAttemptCount reads "reopened"
	// transitions from the journal), then give it a failed run row: the next
	// reconcile pass computes attempts == stackMaxChunkAttempts and marks it
	// failed FOR THE FIRST TIME on this call.
	for i := 0; i < stackMaxChunkAttempts; i++ {
		if err := ledger.TransitionTask(stackID, "c1", stackStatusReopened); err != nil {
			t.Fatal(err)
		}
	}
	seedFailedChunkRun(t, repo, stackID, "c1")

	merges := &recordingStackMergePR{}
	prevMerge := workflowStackMergePR
	t.Cleanup(func() { workflowStackMergePR = prevMerge })
	workflowStackMergePR = merges.Merge

	delivers := &recordingStackDeliverRun{}
	prevDeliver := workflowStackDeliverRun
	t.Cleanup(func() { workflowStackDeliverRun = prevDeliver })
	workflowStackDeliverRun = delivers.Deliver

	prepared := &preparedWorkflowRun{
		repo: repo, compiled: &compiler.CompiledWorkflow{Name: "test", Delivery: &definition.Delivery{Base: "main"}},
	}

	_, err := chunkMergePollPass(context.Background(), prepared, ledger, neverMergedChecker{}, stackID, []ChunkPlan{{ID: "c1"}}, "auto", io.Discard, io.Discard)
	if err == nil {
		t.Fatal("chunkMergePollPass returned nil for a chunk reconcile just marked failed; want a halt error")
	}
	if !strings.Contains(err.Error(), "failed terminally") {
		t.Fatalf("error = %v, want it to mention failed terminally", err)
	}
	if merges.called != 0 {
		t.Fatalf("workflowStackMergePR called %d times after a fresh failure halt; want 0", merges.called)
	}
	if delivers.called {
		t.Fatal("workflowStackDeliverRun called after a fresh failure halt; want no calls")
	}
	c1, err := ledger.GetTask(stackID, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if c1.Status != stackStatusFailed {
		t.Fatalf("c1 status = %q, want %q (reconcile applied the fresh mark_failed transition)", c1.Status, stackStatusFailed)
	}
}

// TestChunkMergePollPassAutoPolicyRunsAutoDeliverAndMerge pins the ordering
// counterpart: on a CLEAN pass (no failed chunk), merge_policy=auto must
// actually reach and run the auto-deliver/auto-merge block instead of the
// failure checks short-circuiting every pass.
func TestChunkMergePollPassAutoPolicyRunsAutoDeliverAndMerge(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := tasks.NewMemoryStore()

	stackID := "stack-auto-policy-clean-pass"
	seedStackTask(t, ledger, stackID, "c1")
	if err := ledger.TransitionTask(stackID, "c1", stackStatusMerged); err != nil {
		t.Fatal(err)
	}

	prepared := &preparedWorkflowRun{repo: repo}

	done, err := chunkMergePollPass(context.Background(), prepared, ledger, neverMergedChecker{}, stackID, []ChunkPlan{{ID: "c1"}}, "auto", io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("chunkMergePollPass() error = %v, want nil on a clean auto-policy pass", err)
	}
	if !done {
		t.Fatal("chunkMergePollPass() done = false, want true: the only chunk is already merged")
	}
}

// TestChunkMergePollPassPropagatesAutoDeliverError pins that a failure from
// autoDeliverReviewedChunks (an orphaned reviewed chunk whose re-delivery
// attempt itself errors) surfaces from chunkMergePollPass instead of being
// swallowed or treated as a routine "keep polling" outcome.
func TestChunkMergePollPassPropagatesAutoDeliverError(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := tasks.NewMemoryStore()

	stackID := "stack-auto-deliver-err"
	seedStackTask(t, ledger, stackID, "c1")
	if err := ledger.TransitionTask(stackID, "c1", stackStatusReviewed); err != nil {
		t.Fatal(err)
	}
	run := workflowledger.RunSnapshot{
		RunID: "wfr-auto-deliver-err-c1", InvocationKey: stackID + ":c1",
		WorkflowName: "stacked", WorktreeName: "wt-auto-deliver-err", BaseRef: "main",
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	seedDeliveryPendingRun(t, repo, run, snapshotJSON)

	deliver := &recordingStackDeliverRun{returnErr: errors.New("injected deliver failure")}
	prevDeliver := workflowStackDeliverRun
	t.Cleanup(func() { workflowStackDeliverRun = prevDeliver })
	workflowStackDeliverRun = deliver.Deliver

	prepared := &preparedWorkflowRun{repo: repo}
	_, err = chunkMergePollPass(context.Background(), prepared, ledger, neverMergedChecker{}, stackID, []ChunkPlan{{ID: "c1"}}, "auto", io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "injected deliver failure") {
		t.Fatalf("chunkMergePollPass() error = %v, want it to propagate autoDeliverReviewedChunks' failure", err)
	}
}

// TestChunkMergePollPassPropagatesAutoMergeError pins that a permanent merge
// failure from autoMergePublishedChunks surfaces from chunkMergePollPass
// instead of being swallowed.
func TestChunkMergePollPassPropagatesAutoMergeError(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := tasks.NewMemoryStore()

	stackID := "stack-auto-merge-err"
	seedStackTask(t, ledger, stackID, "c1")
	if err := ledger.TransitionTask(stackID, "c1", stackStatusPublished); err != nil {
		t.Fatal(err)
	}
	run := workflowledger.RunSnapshot{
		RunID: "wfr-auto-merge-err-c1", InvocationKey: stackID + ":c1",
		WorkflowName: "stacked", WorktreeName: "wt-auto-merge-err", BaseRef: "main",
		RemoteURL: "https://github.com/o/r.git",
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "fix"}})
	if err != nil {
		t.Fatal(err)
	}
	seedDeliveryPendingRun(t, repo, run, snapshotJSON)
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: "key", Status: "succeeded",
		HeadRef: "wf/wt-auto-merge-err", CommitSHA: "sha",
	}); err != nil {
		t.Fatal(err)
	}

	merges := &recordingStackMergePR{err: errors.New("merge PR 123: pull request is closed")}
	prevMerge := workflowStackMergePR
	t.Cleanup(func() { workflowStackMergePR = prevMerge })
	workflowStackMergePR = merges.Merge

	prevNewPR := workflowDeliverNewPR
	t.Cleanup(func() { workflowDeliverNewPR = prevNewPR })
	workflowDeliverNewPR = func() delivery.PRClient { return &fakeFindPRClient{ref: &delivery.PRRef{RemoteID: "123"}} }

	// A real repo whose origin carries the chunk's head branch, so the
	// overlap guard genuinely evaluates (and passes) - this pins the
	// permanent-merge-error propagation, not the guard.
	root, _ := scratchStackRepo(t)
	gitRun(t, root, "checkout", "-b", "wf/wt-auto-merge-err")
	gitRun(t, root, "push", "origin", "wf/wt-auto-merge-err")
	prepared := &preparedWorkflowRun{
		repo: repo, root: root,
		compiled: &compiler.CompiledWorkflow{Name: "test", Delivery: &definition.Delivery{Base: "main"}},
	}

	_, err = chunkMergePollPass(context.Background(), prepared, ledger, neverMergedChecker{}, stackID, []ChunkPlan{{ID: "c1"}}, "auto", io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "permanent merge failure") {
		t.Fatalf("chunkMergePollPass() error = %v, want it to propagate autoMergePublishedChunks' permanent failure", err)
	}
}
