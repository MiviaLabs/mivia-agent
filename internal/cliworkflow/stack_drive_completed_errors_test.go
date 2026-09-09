package cliworkflow

// stack_drive_completed_errors_test.go covers the fail-closed resolution
// branches of workflow_stack_drive_completed.go: an unreadable run or
// snapshot, an incomplete decompose wave, an unreadable task ledger, and a
// merge oracle that errors.

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// resolveFailingRepo fails GetRun and/or GetRunSnapshot so the parked-delivery
// resolvers take their fail-closed paths.
type resolveFailingRepo struct {
	workflowledger.Repository
	failRun      bool
	failSnapshot bool
}

func (r *resolveFailingRepo) GetRun(ctx context.Context, runID string) (workflowledger.RunSnapshot, error) {
	if r.failRun {
		return workflowledger.RunSnapshot{}, errors.New("get run boom")
	}
	return r.Repository.GetRun(ctx, runID)
}

func (r *resolveFailingRepo) GetRunSnapshot(ctx context.Context, runID string) ([]byte, error) {
	if r.failSnapshot {
		return nil, errors.New("get snapshot boom")
	}
	return r.Repository.GetRunSnapshot(ctx, runID)
}

// newResolveFailingRepo returns a memory repository carrying one admitted run,
// wrapped so the named read fails.
func newResolveFailingRepo(t *testing.T, runID string, failRun, failSnapshot bool) workflowledger.Repository {
	t.Helper()
	mem := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = mem.Close() })
	snap := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: "stacked", Status: workflowledger.RunStatusPending,
	}
	if err := mem.CreateRun(context.Background(), snap, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	return &resolveFailingRepo{Repository: mem, failRun: failRun, failSnapshot: failSnapshot}
}

// TestSkipParkedPlanRunPublicationFailsClosedOnUnreadableLedger pins that an
// unreadable run row or snapshot never authorizes the sweep to skip
// publication: the run must fall through to DeliverRunWithStore, which reports
// the error, instead of being silently settled as a non-publishing plan run.
func TestSkipParkedPlanRunPublicationFailsClosedOnUnreadableLedger(t *testing.T) {
	ctx := context.Background()
	const runID = "wfr-parked"
	for _, tc := range []struct {
		name                  string
		failRun, failSnapshot bool
	}{
		{"run row unreadable", true, false},
		{"snapshot unreadable", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newResolveFailingRepo(t, runID, tc.failRun, tc.failSnapshot)
			if SkipParkedPlanRunPublication(ctx, nil, repo, runID) {
				t.Fatal("SkipParkedPlanRunPublication = true on an unreadable ledger; want false")
			}
		})
	}
}

// TestStackPlanMergePolicyFailsOpenToTheGrantDefaultOnUnreadableLedger pins the
// documented default: a merge policy that cannot be resolved is "" (the grant
// default), never a fabricated "auto" - reporting auto would let the sweep
// treat a delivery_pending integration run as incomplete forever.
func TestStackPlanMergePolicyFailsOpenToTheGrantDefaultOnUnreadableLedger(t *testing.T) {
	ctx := context.Background()
	const runID = "wfr-policy"
	for _, tc := range []struct {
		name                  string
		failRun, failSnapshot bool
	}{
		{"run row unreadable", true, false},
		{"snapshot unreadable", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newResolveFailingRepo(t, runID, tc.failRun, tc.failSnapshot)
			if got := StackPlanMergePolicy(ctx, repo, runID); got != "" {
				t.Fatalf("StackPlanMergePolicy = %q on an unreadable ledger; want the empty grant default", got)
			}
		})
	}
}

// TestStackDriveCompletedRefusesPendingDecomposeWave pins §12.1: a decompose
// wave that reports hasMore has chunks not yet admitted, so the stack is NOT
// complete even when every currently known chunk merged. Settling here would
// drop the outstanding chunks on the floor.
func TestStackDriveCompletedRefusesPendingDecomposeWave(t *testing.T) {
	root, store, repo, stackID := seedUnmergedIntegrationStack(t)

	prev := LoadAllStackChunksFunc
	t.Cleanup(func() { LoadAllStackChunksFunc = prev })
	// Same chunks the seeded stack has (all merged), but the wave is not done.
	LoadAllStackChunksFunc = func(repo workflowledger.Repository, id string) ([]delivery.ChunkPlan, bool, string, error) {
		chunks, _, scope, err := prev(repo, id)
		return chunks, true, scope, err
	}
	if StackDriveCompleted(context.Background(), root, store, repo, stackID, "approve", true) {
		t.Fatal("StackDriveCompleted = true with a pending decompose wave; want false")
	}

	// The same stack with hasMore=false IS complete under the grant policy, so
	// the refusal above is the hasMore branch and not a broken fixture.
	LoadAllStackChunksFunc = prev
	if !StackDriveCompleted(context.Background(), root, store, repo, stackID, "approve", true) {
		t.Fatal("StackDriveCompleted = false for the settled grant-policy stack; fixture is wrong")
	}
}

// TestStackDriveCompletedRefusesUnreadableTaskLedger pins that a task-ledger
// read failure is fail-closed: the merged set is unknown, so the stack must not
// be reported complete.
func TestStackDriveCompletedRefusesUnreadableTaskLedger(t *testing.T) {
	root, store, repo, stackID := seedUnmergedIntegrationStack(t)

	prev := StackTaskMapFunc
	t.Cleanup(func() { StackTaskMapFunc = prev })
	StackTaskMapFunc = func(_ *workflowledger.Store, _ string) (map[string]workflowledger.Task, error) {
		return nil, errors.New("task map boom")
	}
	if StackDriveCompleted(context.Background(), root, store, repo, stackID, "approve", true) {
		t.Fatal("StackDriveCompleted = true with an unreadable task ledger; want false")
	}
}

// TestStackDriveCompletedRefusesWhenMergeOracleErrors pins the auto-policy
// oracle contract: when git/gh cannot answer whether the integration PR
// merged, the answer is not "merged". Treating an oracle failure as a merge
// would settle the plan run with the final PR still open.
func TestStackDriveCompletedRefusesWhenMergeOracleErrors(t *testing.T) {
	root, store, repo, stackID := seedUnmergedIntegrationStack(t)

	prev := GitMergeCheckFunc
	t.Cleanup(func() { GitMergeCheckFunc = prev })
	called := false
	GitMergeCheckFunc = func(_ context.Context, _ delivery.GitRunner, _ delivery.PRClient, _ delivery.GitContext, _, _, _, _ string, _ bool) (bool, error) {
		called = true
		// Report merged AND an error: only honouring the error keeps this false.
		return true, errors.New("gh: rate limited")
	}
	if StackDriveCompleted(context.Background(), root, store, repo, stackID, "auto", true) {
		t.Fatal("StackDriveCompleted = true when the merge oracle errored; want false")
	}
	if !called {
		t.Fatal("the merge oracle was never consulted; the test proves nothing")
	}

	// The same oracle answering cleanly DOES settle, so the refusal above is
	// the error branch.
	GitMergeCheckFunc = func(_ context.Context, _ delivery.GitRunner, _ delivery.PRClient, _ delivery.GitContext, _, _, _, _ string, _ bool) (bool, error) {
		return true, nil
	}
	if !StackDriveCompleted(context.Background(), root, store, repo, stackID, "auto", true) {
		t.Fatal("StackDriveCompleted = false when the oracle confirmed the merge; want true")
	}
}
