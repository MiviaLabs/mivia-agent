package cliworkflow

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestStackDriveCompletedAutoPolicyWaitsForIntegrationMerge pins the durable
// merge oracle gate in StackDriveCompleted: under merge_policy=auto, a
// succeeded integration run with pushed evidence is NOT enough to settle the
// plan run. The final PR must actually be merged in git; until then the stack
// reports incomplete so the sweep/operator keeps driving.
func TestStackDriveCompletedAutoPolicyWaitsForIntegrationMerge(t *testing.T) {
	ctx := context.Background()
	root, store, repo, stackID := seedUnmergedIntegrationStack(t)

	// Stub the merge oracle so git reports the PR is NOT merged.
	prevGit := WorkflowDeliverGit
	prevNewPR := WorkflowDeliverNewPR
	t.Cleanup(func() {
		WorkflowDeliverGit = prevGit
		WorkflowDeliverNewPR = prevNewPR
	})
	WorkflowDeliverGit = errorGitRunner{err: errors.New("test: branch not in base")}
	WorkflowDeliverNewPR = func() delivery.PRClient { return unmergedPRClient{} }

	if got := StackDriveCompleted(ctx, root, store, repo, stackID, "auto", true); got {
		t.Fatal("StackDriveCompleted = true for auto policy with succeeded+pushed+unmerged integration run; want false")
	}

	// Under the grant (approve) policy the same durable state IS complete: the
	// driver reports the stack complete awaiting the publish grant.
	if got := StackDriveCompleted(ctx, root, store, repo, stackID, "approve", true); !got {
		t.Fatal("StackDriveCompleted = false for grant policy with succeeded integration run; want true")
	}
}

// seedUnmergedIntegrationStack builds a two-chunk stack whose chunks are all
// merged and whose integration run settled succeeded with durable pushed
// evidence but has not actually merged in git.
func seedUnmergedIntegrationStack(t *testing.T) (root string, store *storage.SQLite, repo workflowledger.Repository, stackID string) {
	t.Helper()
	ctx := context.Background()
	mem := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = mem.Close() })
	repo = mem

	root = t.TempDir()
	storePath := filepath.Join(root, "workflowledger.db")
	store, err := OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	stackID = "wfr-f3-unmerged"

	// Plan run with the chunk plan output the driver reads.
	planSnap := workflowledger.RunSnapshot{
		RunID: stackID, WorkflowName: "stacked", Status: workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, planSnap, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	seedSucceededDecomposeAttempt(t, repo, stackID, []byte(multiChunkPlanOutput))

	_, chunks, _, _, err := ParseStackPlanOutputFunc([]byte(multiChunkPlanOutput))
	if err != nil || len(chunks) != 2 {
		t.Fatalf("parse chunks = %v, %v; want 2", chunks, err)
	}
	ledger := workflowledger.NewStore(store)
	if err := SeedStackLedgerFunc(ledger, stackID, chunks); err != nil {
		t.Fatal(err)
	}
	for _, c := range chunks {
		if err := ledger.TransitionTask(stackID, c.ID, delivery.StatusMerged); err != nil {
			t.Fatalf("transition chunk %s to merged: %v", c.ID, err)
		}
	}

	// Integration run settled succeeded with durable pushed evidence.
	run := seedIntegrationRunAdmitted(t, repo, stackID, false)
	stored, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	stored, err = repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, stored.Version, workflowledger.RunStatusSucceeded, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: "k1", Mode: "draft", BaseRef: "main",
		HeadRef: "wf/wt-integration", CommitSHA: "abc123", Status: "pushed", UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return root, store, repo, stackID
}

type errorGitRunner struct{ err error }

func (g errorGitRunner) Run(context.Context, delivery.GitContext, ...string) (string, error) {
	return "", g.err
}

// TestStackDriveCompletedFailedIntegrationNotComplete pins the hostile audit
// finding: a terminally failed integration run must NOT make the stacking plan
// run look complete. The function must fail closed for every terminal failure
// status.
func TestStackDriveCompletedFailedIntegrationNotComplete(t *testing.T) {
	ctx := context.Background()

	for _, status := range []workflowledger.RunStatus{
		workflowledger.RunStatusFailed,
		workflowledger.RunStatusCanceled,
		workflowledger.RunStatusTimedOut,
		workflowledger.RunStatusDeliveryFailed,
	} {
		t.Run(string(status), func(t *testing.T) {
			root, store, repo, stackID := seedFailedIntegrationStack(t, status)
			if got := StackDriveCompleted(ctx, root, store, repo, stackID, "auto", true); got {
				t.Fatalf("StackDriveCompleted = true for integration run status %q; want false", status)
			}
		})
	}
}

// seedFailedIntegrationStack builds a two-chunk stack whose chunks are all
// merged and whose integration run settled with the given terminal failure
// status.
func seedFailedIntegrationStack(t *testing.T, intStatus workflowledger.RunStatus) (root string, store *storage.SQLite, repo workflowledger.Repository, stackID string) {
	t.Helper()
	ctx := context.Background()
	mem := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = mem.Close() })
	repo = mem

	root = t.TempDir()
	storePath := filepath.Join(root, "workflowledger.db")
	store, err := OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	stackID = "wfr-f3-failed-" + string(intStatus)

	// Plan run with the chunk plan output the driver reads.
	planSnap := workflowledger.RunSnapshot{
		RunID: stackID, WorkflowName: "stacked", Status: workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, planSnap, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	seedSucceededDecomposeAttempt(t, repo, stackID, []byte(multiChunkPlanOutput))

	_, chunks, _, _, err := ParseStackPlanOutputFunc([]byte(multiChunkPlanOutput))
	if err != nil || len(chunks) != 2 {
		t.Fatalf("parse chunks = %v, %v; want 2", chunks, err)
	}
	ledger := workflowledger.NewStore(store)
	if err := SeedStackLedgerFunc(ledger, stackID, chunks); err != nil {
		t.Fatal(err)
	}
	for _, c := range chunks {
		if err := ledger.TransitionTask(stackID, c.ID, delivery.StatusMerged); err != nil {
			t.Fatalf("transition chunk %s to merged: %v", c.ID, err)
		}
	}

	// Integration run admitted and moved to the requested terminal failure status.
	run := seedIntegrationRunAdmitted(t, repo, stackID, false)
	setIntegrationRunStatus(t, ctx, repo, run.RunID, intStatus)
	return root, store, repo, stackID
}

// setIntegrationRunStatus moves an existing integration run to the requested
// terminal status through valid transitions.
func setIntegrationRunStatus(t *testing.T, ctx context.Context, repo workflowledger.Repository, runID string, want workflowledger.RunStatus) {
	t.Helper()

	// delivery_pending can go directly to delivery_failed; everything else is
	// reached through running.
	path := []workflowledger.RunStatus{want}
	if want != workflowledger.RunStatusDeliveryFailed {
		path = []workflowledger.RunStatus{workflowledger.RunStatusRunning, want}
	}

	for _, next := range path {
		stored, err := repo.GetRun(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, next, nil); err != nil {
			t.Fatalf("transition integration run to %q: %v", next, err)
		}
	}
}

type unmergedPRClient struct{}

func (unmergedPRClient) FindByHead(context.Context, string, string) (*delivery.PRRef, error) {
	return nil, nil
}
func (unmergedPRClient) Create(context.Context, string, delivery.PRInput) (delivery.PRRef, error) {
	return delivery.PRRef{}, nil
}
func (unmergedPRClient) IsMerged(context.Context, string, string) (bool, error) { return false, nil }
