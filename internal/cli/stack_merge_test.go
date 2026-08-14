package cli

// Context-cooperative stack merge waits (this wave): the merge wait loops poll
// with bare time.Sleep(20s), so the bounded driveCtx a session auto-delivery
// attempt passes (workflowAutoDeliveryAttemptTimeout) can never actually stop
// a stuck drive: a stuck merge-queue poll holds the plan run's execution flock
// indefinitely (the observed wedge). These tests pin the fix: a
// cancelled/expired context must make the wait loops return promptly with the
// cancellation error instead of sleeping through their poll forever.

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// neverMergedChecker is a MergeChecker stub that never reports a merge, so the
// wait loops keep polling instead of completing.
type neverMergedChecker struct{}

func (neverMergedChecker) Merged(context.Context, string, bool) (bool, error) {
	return false, nil
}

// TestWaitForChunkMergesHonorsCancelledContext pins the fix: a pre-cancelled
// context must make waitForChunkMerges return promptly with the cancellation
// error instead of sleeping through its 20s poll forever (which would hold the
// plan run's execution flock indefinitely).
func TestWaitForChunkMergesHonorsCancelledContext(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := tasks.NewMemoryStore()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled: the wait must return immediately
	done := make(chan error, 1)
	go func() {
		done <- waitForChunkMerges(ctx, &preparedWorkflowRun{repo: repo}, ledger, neverMergedChecker{}, "stack-ctx", []ChunkPlan{{ID: "c1"}}, "", io.Discard, io.Discard)
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
	done := make(chan error, 1)
	go func() {
		done <- waitForIntegrationMerge(ctx, repo, neverMergedChecker{}, "stack-ctx", io.Discard, io.Discard)
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
	byID := map[string]tasks.Task{
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

	byID["c1-deferred"] = tasks.Task{ID: "c1-deferred", Status: stackStatusMerged, Deps: []string{"c1"}}
	if _, blocked := blockedByUnmergedDependent(byID, "c1"); blocked {
		t.Fatal("blockedByUnmergedDependent(c1) = blocked after its follow-up merged, want unblocked")
	}
}
