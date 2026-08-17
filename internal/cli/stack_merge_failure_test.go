package cli

// Failure-ordering regression tests for the stack merge poll pass. These tests
// pin that terminal chunk failures halt the drive before any auto-merge or
// auto-delivery of remaining chunks can leave partial stack content on the base
// branch.

import (
	"bytes"
	"context"
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
