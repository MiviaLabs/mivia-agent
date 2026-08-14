package cli

// Pins a live deadlock found running the new e2e suite's DAG scenarios
// (dag-diamond, linear-chain-4): waitForChunkMerges only returns when
// allChunksMerged is true for the WHOLE chunk set decompose declared - but a
// dependent chunk (c3 depending on c1+c2) is never even ADMITTED until the
// outer driveStackToCompletion loop calls driveStack again, which only
// happens after waitForChunkMerges returns and the loop `continue`s. With
// c1 and c2 merged and c3/c4 still "planned", waitForChunkMerges's own exit
// condition is permanently false (c3/c4 will never show merged - they were
// never admitted), so it polls its own 20s loop forever and driveStack is
// never called again. Any dependency chain past the first wave hangs
// indefinitely, even under merge_policy=auto with a live merge queue.
//
// The fix: waitForChunkMerges must also return (not an error - a normal
// "this pass is done, re-drive") the moment a not-yet-admitted chunk's
// dependencies are all merged, so the outer loop's continue actually reaches
// driveStack.

import (
	"context"
	"io"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// alwaysMergedChecker reports every branch merged - stackReconcile promotes
// merged+pushed tasks to stackStatusMerged on its own, so this drives the
// scenario without a real git repo.
type alwaysMergedChecker struct{}

func (alwaysMergedChecker) Merged(context.Context, string, bool) (bool, error) { return true, nil }

func TestWaitForChunkMergesReturnsWhenADependentBecomesAdmissible(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := tasks.NewMemoryStore()
	stackID := "stack-diamond"
	if _, err := ledger.StorePlan(tasks.Plan{ID: stackID, Scope: stackScope(stackID), Schema: stackPlanSchema}); err != nil {
		t.Fatal(err)
	}
	// c1, c2 admitted and about to merge (checker reports merged=true,
	// reconcile promotes them once a run row shows pushed evidence).
	for _, id := range []string{"c1", "c2"} {
		if err := ledger.CreateTask(tasks.Task{ID: id, PlanRef: stackID, Scope: stackScope(stackID), Status: stackStatusRunning}); err != nil {
			t.Fatal(err)
		}
		seedDeliveryPendingRun(t, repo, workflowledger.RunSnapshot{RunID: "wfr-" + id, InvocationKey: stackID + ":" + id, WorktreeName: "wt-" + id, BaseCommit: "deadbeef"}, []byte("snapshot"))
		run, err := repo.GetRun(context.Background(), "wfr-"+id)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CompareAndSetRunStatus(context.Background(), "wfr-"+id, run.Version, workflowledger.RunStatusSucceeded, nil); err != nil {
			t.Fatal(err)
		}
		// stackRunPushed requires a delivery record with a commit SHA and a
		// pushed/succeeded status - a bare run status is not durable pushed
		// evidence.
		if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
			RunID: "wfr-" + id, IdempotencyKey: "wfr-" + id, CommitSHA: "deadbeef", Status: "succeeded",
		}); err != nil {
			t.Fatal(err)
		}
		// admitPendingFollowUps checks every merged chunk unconditionally;
		// pre-seed its (never-used) follow-up task id so its early-return
		// fires instead of resolving a real worktree - this test is about
		// admission readiness, not the deferred-split follow-up path.
		if err := ledger.CreateTask(tasks.Task{ID: deferredFollowUpChunkID(id), PlanRef: stackID, Scope: stackScope(stackID), Status: stackStatusMerged}); err != nil {
			t.Fatal(err)
		}
	}
	// c3 depends on both c1 and c2, but was NEVER ADMITTED (still planned) -
	// exactly nextAdmissionWave's job to pick up once c1/c2 merge.
	if err := ledger.CreateTask(tasks.Task{ID: "c3", PlanRef: stackID, Scope: stackScope(stackID), Status: stackStatusPlanned, Deps: []string{"c1", "c2"}}); err != nil {
		t.Fatal(err)
	}
	chunks := []ChunkPlan{{ID: "c1"}, {ID: "c2"}, {ID: "c3", DependsOn: []string{"c1", "c2"}}}

	done := make(chan error, 1)
	go func() {
		done <- waitForChunkMerges(context.Background(), &preparedWorkflowRun{repo: repo}, ledger, alwaysMergedChecker{}, stackID, chunks, "auto", io.Discard, io.Discard)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitForChunkMerges = %v, want nil (c1/c2 merged, c3 now admissible - the outer loop must re-drive)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitForChunkMerges did not return once c3 became admissible; it will poll forever and the dependent chunk is never admitted")
	}
}
