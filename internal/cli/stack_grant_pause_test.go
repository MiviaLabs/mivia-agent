package cli

// Policy-A durable pause (design review, 2026-08-15): with merge_policy !=
// "auto", waitForChunkMerges polled every 20s while every unmerged chunk sat
// at "reviewed" awaiting a human publish grant - a poll that can never make
// progress by itself, holding the plan run's execution flock for however
// long the human is away. The industry-wide pattern (git-spr, ghstack,
// Graphite, GitHub environment approvals) is persist-and-exit: the ledger is
// already the durable resume point, so the wait must return cleanly with
// guidance instead of polling, exactly as waitIntegrationRunSettled already
// does for the integration run.

import (
	"context"
	"errors"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"io"
	"strings"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func seedStackTaskStatus(t *testing.T, ledger *workflowledger.Store, stackID, chunkID, status string) {
	t.Helper()
	if err := ledger.CreateTask(workflowledger.Task{ID: chunkID, PlanRef: stackID, Scope: stackScope(stackID), Status: status}); err != nil {
		t.Fatal(err)
	}
}

func TestStackAwaitsGrantOnly(t *testing.T) {
	cases := []struct {
		name     string
		statuses map[string]string
		want     bool
	}{
		{"all reviewed", map[string]string{"c1": stackStatusReviewed, "c2": stackStatusReviewed}, true},
		{"reviewed plus merged", map[string]string{"c1": stackStatusReviewed, "c2": stackStatusMerged}, true},
		{"reviewed plus published pauses too", map[string]string{"c1": stackStatusReviewed, "c2": stackStatusPublished}, true},
		{"published only pauses", map[string]string{"c1": stackStatusPublished}, true},
		{"published plus merged pauses", map[string]string{"c1": stackStatusPublished, "c2": stackStatusMerged}, true},
		{"running chunk still working", map[string]string{"c1": stackStatusReviewed, "c2": stackStatusRunning}, false},
		{"nothing waiting", map[string]string{"c1": stackStatusMerged}, false},
		{"empty", map[string]string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			byID := map[string]workflowledger.Task{}
			for id, st := range tc.statuses {
				byID[id] = workflowledger.Task{ID: id, Status: st}
			}
			if got := stackAwaitsGrantOnly(byID); got != tc.want {
				t.Fatalf("stackAwaitsGrantOnly = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWaitForChunkMergesPausesWhenPublishedFollowUpRemains pins the
// follow-up variant of the durable pause: under a non-auto policy a
// published chunk's PR (a diff-size split's follow-up is seeded published
// with its PR already open) waits on a HUMAN merge, so polling is a
// guaranteed no-op. The wait must pause with merge guidance instead of
// polling every 20s while holding the plan run's execution flock.
func TestWaitForChunkMergesPausesWhenPublishedFollowUpRemains(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := workflowledger.NewMemoryStore()
	stackID := "stack-published"
	if _, err := ledger.StorePlan(workflowledger.Plan{ID: stackID, Scope: stackScope(stackID), Schema: stackPlanSchema}); err != nil {
		t.Fatal(err)
	}
	seedStackTaskStatus(t, ledger, stackID, "c1", stackStatusMerged)
	seedStackTaskStatus(t, ledger, stackID, "c1-deferred", stackStatusPublished)
	// The published follow-up needs a live run row: reconcile reopens an
	// in-flight task whose run vanished, which would defeat the pause.
	seedDeliveryPendingRun(t, repo, workflowledger.RunSnapshot{RunID: "wfr-c1-deferred", InvocationKey: stackID + ":c1-deferred"}, []byte("snapshot"))

	var out strings.Builder
	done := make(chan error, 1)
	go func() {
		done <- waitForChunkMerges(context.Background(), &cliworkflow.PreparedWorkflowRun{Repo: repo}, ledger, neverMergedChecker{}, stackID, []ChunkPlan{{ID: "c1"}}, "", &out, io.Discard)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errStackAwaitsGrant) {
			t.Fatalf("waitForChunkMerges = %v, want errStackAwaitsGrant", err)
		}
		if !strings.Contains(out.String(), "merge") {
			t.Fatalf("output %q must carry the merge guidance for the published follow-up", out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitForChunkMerges kept polling; a human-merge-only stack must pause durably instead")
	}
}

// TestWaitForChunkMergesPausesWhenOnlyGrantsRemain pins the durable pause:
// with a non-auto policy and every unmerged chunk at reviewed, the wait must
// return errStackAwaitsGrant promptly (before its 20s poll sleep), printing
// the publish-grant guidance, instead of polling forever.
func TestWaitForChunkMergesPausesWhenOnlyGrantsRemain(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := workflowledger.NewMemoryStore()
	stackID := "stack-grant"
	if _, err := ledger.StorePlan(workflowledger.Plan{ID: stackID, Scope: stackScope(stackID), Schema: stackPlanSchema}); err != nil {
		t.Fatal(err)
	}
	seedStackTaskStatus(t, ledger, stackID, "c1", stackStatusReviewed)
	// The reviewed chunk needs a live run row (delivery_pending awaiting the
	// grant): reconcile reopens a reviewed chunk whose run vanished, which
	// would defeat the pause under test.
	seedDeliveryPendingRun(t, repo, workflowledger.RunSnapshot{RunID: "wfr-c1", InvocationKey: stackID + ":c1"}, []byte("snapshot"))

	var out strings.Builder
	done := make(chan error, 1)
	go func() {
		done <- waitForChunkMerges(context.Background(), &cliworkflow.PreparedWorkflowRun{Repo: repo}, ledger, neverMergedChecker{}, stackID, []ChunkPlan{{ID: "c1"}}, "", &out, io.Discard)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errStackAwaitsGrant) {
			t.Fatalf("waitForChunkMerges = %v, want errStackAwaitsGrant", err)
		}
		if !strings.Contains(out.String(), "--allow-publish") {
			t.Fatalf("output %q must carry the publish-grant guidance", out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitForChunkMerges kept polling; a grant-only stack must pause durably instead")
	}
}

// TestStackGrantHintLinesNameTheDeliverCommand pins the status surface: a
// reviewed chunk's row is followed by the exact ready-to-paste deliver
// command so the pause message and `mivia stack status` agree.
func TestStackGrantHintLinesNameTheDeliverCommand(t *testing.T) {
	lines := stackGrantHintLines([]workflowledger.Task{
		{ID: "c1", Status: stackStatusReviewed},
		{ID: "c2", Status: stackStatusMerged},
	}, func(chunkID string) string {
		if chunkID == "c1" {
			return "wfr-abc"
		}
		return ""
	})
	if len(lines) != 1 {
		t.Fatalf("hint lines = %v, want exactly one for the reviewed chunk", lines)
	}
	if !strings.Contains(lines[0], "mivia workflow deliver wfr-abc --allow-publish") || !strings.Contains(lines[0], "c1") {
		t.Fatalf("hint %q must name the chunk and the exact deliver command", lines[0])
	}
}

// TestStackGrantHintLinesWarnsAboutDeferredFollowUpOrder pins the ordering
// warning: under merge_policy=approve a parent chunk with an unmerged deferred
// follow-up must tell the human to merge the follow-up first.
func TestStackGrantHintLinesWarnsAboutDeferredFollowUpOrder(t *testing.T) {
	lines := stackGrantHintLines([]workflowledger.Task{
		{ID: "c1", Status: stackStatusPublished},
		{ID: "c1-deferred", Status: stackStatusPublished, Deps: []string{"c1"}},
	}, func(string) string { return "" })
	if len(lines) != 2 {
		t.Fatalf("hint lines = %v, want two", lines)
	}
	found := false
	for _, line := range lines {
		if strings.Contains(line, "c1") && strings.Contains(line, "follow-up") && strings.Contains(line, "merge the follow-up") {
			found = true
		}
	}
	if !found {
		t.Fatalf("hints %v must warn that c1's follow-up must merge first", lines)
	}
}
