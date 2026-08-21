package cli

// Integration-PR merge retry regressions (F3 residual): waitForIntegrationMerge
// must re-attempt autoMergeOne on every poll tick under merge_policy=auto, not
// just once up front. These tests live in their own file because
// stack_merge_test.go is already near the repo's per-file line ceiling.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// retryThenSuccessMergePR is a MergeChecker/MergePR double stub that fails
// once with a retriable merge refusal, succeeds on the second attempt, and
// reports merged only after the successful attempt. It pins the F3 residual:
// waitForIntegrationMerge must retry autoMergeOne each poll tick, not just
// once up front.
type retryThenSuccessMergePR struct {
	called int
	merged bool
	t      *testing.T
}

func (m *retryThenSuccessMergePR) Merge(_ context.Context, slug, remoteID string, draft bool) error {
	m.called++
	if m.called == 1 {
		return errors.New("merge PR 123: pull request review is required")
	}
	m.merged = true
	return nil
}

func (m *retryThenSuccessMergePR) Merged(_ context.Context, _, _, _, _ string, _ bool) (bool, error) {
	return m.merged, nil
}

// TestWaitForIntegrationMergeRetriesAutoMergeEachTick pins the F3 residual:
// under merge_policy=auto the integration PR merge must be retried on every
// poll tick, so a transient merge refusal that outlives MergePullRequest's
// internal retry still completes instead of leaving the PR open forever.
func TestWaitForIntegrationMergeRetriesAutoMergeEachTick(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	stackID := "stack-int-retry"
	run := seedIntegrationRunAdmitted(t, repo, stackID, false)
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: "test-delivery", Status: "succeeded",
		HeadRef: "wf/wt-integration", CommitSHA: "deadbeef",
	}); err != nil {
		t.Fatal(err)
	}

	root, _ := scratchStackRepo(t)
	gitRun(t, root, "checkout", "-b", "wf/wt-integration")
	gitRun(t, root, "push", "origin", "wf/wt-integration")
	prepared := &preparedWorkflowRun{repo: repo, root: root, compiled: &definition.CompiledWorkflow{Name: "test", Delivery: &definition.Delivery{Base: "main"}}}

	merges := &retryThenSuccessMergePR{t: t}
	prevMerge := workflowStackMergePR
	t.Cleanup(func() { workflowStackMergePR = prevMerge })
	workflowStackMergePR = merges.Merge

	prevNewPR := workflowDeliverNewPR
	t.Cleanup(func() { workflowDeliverNewPR = prevNewPR })
	workflowDeliverNewPR = func() delivery.PRClient { return &fakeFindPRClient{ref: &delivery.PRRef{RemoteID: "123"}} }

	prevInterval := stackMergePollInterval
	t.Cleanup(func() { stackMergePollInterval = prevInterval })
	stackMergePollInterval = 10 * time.Millisecond

	var stdout bytes.Buffer
	if err := waitForIntegrationMerge(context.Background(), prepared, repo, merges, stackID, "auto", &stdout, io.Discard); err != nil {
		t.Fatalf("waitForIntegrationMerge() error = %v; stdout = %q", err, stdout.String())
	}
	if merges.called != 2 {
		t.Fatalf("merge attempts = %d, want 2 (one retriable failure then one success)", merges.called)
	}
	if !strings.Contains(stdout.String(), "integration PR merged") {
		t.Fatalf("stdout = %q, want integration PR merged message", stdout.String())
	}
}

// TestWaitForIntegrationMergePermanentMergeErrorHalts pins that a permanent
// merge refusal on the integration PR stops the wait immediately with the
// error, not retried forever.
func TestWaitForIntegrationMergePermanentMergeErrorHalts(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	stackID := "stack-int-perm"
	run := seedIntegrationRunAdmitted(t, repo, stackID, false)
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: "test-delivery", Status: "succeeded",
		HeadRef: "wf/wt-integration", CommitSHA: "deadbeef",
	}); err != nil {
		t.Fatal(err)
	}

	root, _ := scratchStackRepo(t)
	gitRun(t, root, "checkout", "-b", "wf/wt-integration")
	gitRun(t, root, "push", "origin", "wf/wt-integration")
	prepared := &preparedWorkflowRun{repo: repo, root: root, compiled: &definition.CompiledWorkflow{Name: "test", Delivery: &definition.Delivery{Base: "main"}}}

	merges := &recordingStackMergePR{err: errors.New("merge PR 123: pull request is closed")}
	prevMerge := workflowStackMergePR
	t.Cleanup(func() { workflowStackMergePR = prevMerge })
	workflowStackMergePR = merges.Merge

	prevNewPR := workflowDeliverNewPR
	t.Cleanup(func() { workflowDeliverNewPR = prevNewPR })
	workflowDeliverNewPR = func() delivery.PRClient { return &fakeFindPRClient{ref: &delivery.PRRef{RemoteID: "123"}} }

	err := waitForIntegrationMerge(context.Background(), prepared, repo, neverMergedChecker{}, stackID, "auto", io.Discard, io.Discard)
	if err == nil {
		t.Fatal("waitForIntegrationMerge returned nil for permanent merge failure; want error")
	}
	if !strings.Contains(err.Error(), "permanent merge failure") {
		t.Fatalf("error = %v, want 'permanent merge failure'", err)
	}
	if merges.called != 1 {
		t.Fatalf("merge attempts = %d, want 1", merges.called)
	}
}

// TestWaitForIntegrationMergeGrantPolicyDoesNotAutoMerge pins the regression
// guard: under merge_policy!=auto the wait must NOT call autoMergeOne; it only
// polls merge state. A checker that reports merged immediately completes, and
// any call to the merge stub fails the test.
func TestWaitForIntegrationMergeGrantPolicyDoesNotAutoMerge(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	stackID := "stack-int-grant"
	seedIntegrationRunAdmitted(t, repo, stackID, false)

	prevMerge := workflowStackMergePR
	t.Cleanup(func() { workflowStackMergePR = prevMerge })
	workflowStackMergePR = func(_ context.Context, _, _ string, _ bool) error {
		t.Fatal("workflowStackMergePR called under grant policy")
		return nil
	}

	prepared := &preparedWorkflowRun{repo: repo}
	if err := waitForIntegrationMerge(context.Background(), prepared, repo, &immediateMergedChecker{}, stackID, "approve", io.Discard, io.Discard); err != nil {
		t.Fatalf("waitForIntegrationMerge() error = %v", err)
	}
}
