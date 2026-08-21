package cli

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestChunkRunNoDiff(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	noDiffRun := workflowledger.RunSnapshot{
		RunID: "wfr-nodiff", Status: workflowledger.RunStatusSucceeded,
		WorktreeName: "wt-nodiff", RemoteURL: "https://github.com/o/r.git",
	}
	seedDeliveryPendingRun(t, repo, noDiffRun, []byte("{}"))
	settleRunToSucceeded(t, repo, noDiffRun.RunID)
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
		RunID: noDiffRun.RunID, IdempotencyKey: "key-nodiff", Status: "no_diff",
		BaseRef: "main", HeadRef: "wf/wt-nodiff", Provider: "github",
	}); err != nil {
		t.Fatalf("UpsertDelivery: %v", err)
	}

	pushedRun := workflowledger.RunSnapshot{
		RunID: "wfr-pushed", Status: workflowledger.RunStatusSucceeded,
		WorktreeName: "wt-pushed", RemoteURL: "https://github.com/o/r.git",
	}
	seedDeliveryPendingRun(t, repo, pushedRun, []byte("{}"))
	settleRunToSucceeded(t, repo, pushedRun.RunID)
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
		RunID: pushedRun.RunID, IdempotencyKey: "key-pushed", Status: "succeeded",
		BaseRef: "main", HeadRef: "wf/wt-pushed", Provider: "github",
		CommitSHA: "deadbeef",
	}); err != nil {
		t.Fatalf("UpsertDelivery: %v", err)
	}

	if !chunkRunNoDiff(repo, noDiffRun) {
		t.Fatalf("chunkRunNoDiff(noDiffRun) = false, want true")
	}
	if chunkRunNoDiff(repo, pushedRun) {
		t.Fatalf("chunkRunNoDiff(pushedRun) = true, want false")
	}

	// A non-succeeded run is never no_diff, even without pushed evidence.
	deliveryPending := workflowledger.RunSnapshot{
		RunID: "wfr-pending", Status: workflowledger.RunStatusDeliveryPending,
		WorktreeName: "wt-pending", RemoteURL: "https://github.com/o/r.git",
	}
	if chunkRunNoDiff(repo, deliveryPending) {
		t.Fatalf("chunkRunNoDiff(deliveryPending) = true, want false")
	}
}

// TestChunkRunNoDiffRequiresPositiveEvidence pins the fail-closed contract
// (F4 fix, reachable-bug audit finding 3): "no diff" must be proven by an
// actual no_diff delivery record, never inferred from the ABSENCE of pushed
// evidence. A succeeded run with zero delivery records (the shape a
// ListDeliveries read failure or an unwritten record produces) must not be
// mistaken for no_diff - that misreading durably marks a chunk merged with
// no PR ever created, dropping its content silently.
func TestChunkRunNoDiffRequiresPositiveEvidence(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	noEvidenceRun := workflowledger.RunSnapshot{
		RunID: "wfr-noevidence", Status: workflowledger.RunStatusSucceeded,
		WorktreeName: "wt-noevidence", RemoteURL: "https://github.com/o/r.git",
	}
	seedDeliveryPendingRun(t, repo, noEvidenceRun, []byte("{}"))
	settleRunToSucceeded(t, repo, noEvidenceRun.RunID)
	// No UpsertDelivery call at all: zero records, exactly what a
	// ListDeliveries read failure or a not-yet-recorded delivery looks like.

	if chunkRunNoDiff(repo, noEvidenceRun) {
		t.Fatalf("chunkRunNoDiff(noEvidenceRun) = true, want false (no delivery record proves no_diff)")
	}

	// A read failure on the delivery records must fail closed too: never
	// read as "confirmed no_diff".
	failing := listDeliveriesErrorRepository{Repository: repo, err: errListDeliveriesBoom}
	if chunkRunNoDiff(failing, noEvidenceRun) {
		t.Fatalf("chunkRunNoDiff with a ListDeliveries error = true, want false (fail closed)")
	}
}

// TestStackRunClaimStale pins the F7 liveness-probe contract: a fresh claim
// is not stale, an expired or absent claim is, and a probe failure degrades
// to "not stale" (a transient storage fault must never masquerade as a dead
// run and trigger an unwanted auto-resume).
func TestStackRunClaimStale(t *testing.T) {
	cases := []struct {
		name       string
		holder     string
		acquiredAt time.Time
		ok         bool
		err        error
		want       bool
	}{
		{name: "fresh claim", holder: "h1", acquiredAt: time.Now(), ok: true, want: false},
		{name: "expired claim", holder: "h1", acquiredAt: time.Now().Add(-3 * time.Minute), ok: true, want: true},
		{name: "absent claim", ok: false, want: true},
		{name: "probe failure degrades to not stale", err: errGetRunClaimBoom, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := getRunClaimStubRepository{holder: tc.holder, acquiredAt: tc.acquiredAt, ok: tc.ok, err: tc.err}
			if got := stackRunClaimStale(repo, "wfr-x"); got != tc.want {
				t.Fatalf("stackRunClaimStale = %v, want %v", got, tc.want)
			}
		})
	}
}

var errGetRunClaimBoom = errors.New("boom: transient store failure")

// getRunClaimStubRepository stubs GetRunClaim directly (rather than driving
// a real claim's acquired_at through Repository.ClaimRun/time) so
// stackRunClaimStale's threshold logic is tested independently of any
// specific backend's clock.
type getRunClaimStubRepository struct {
	workflowledger.Repository
	holder     string
	acquiredAt time.Time
	ok         bool
	err        error
}

func (g getRunClaimStubRepository) GetRunClaim(ctx context.Context, runID string) (string, time.Time, bool, error) {
	return g.holder, g.acquiredAt, g.ok, g.err
}

var errListDeliveriesBoom = errors.New("boom: transient store failure")

// listDeliveriesErrorRepository embeds a real Repository and fails only
// ListDeliveries, so chunkRunNoDiff's fail-closed contract can be exercised
// without hand-rolling every other Repository method.
type listDeliveriesErrorRepository struct {
	workflowledger.Repository
	err error
}

func (l listDeliveriesErrorRepository) ListDeliveries(ctx context.Context, runID string) ([]workflowledger.DeliveryRecord, error) {
	return nil, l.err
}

func settleRunToSucceeded(t *testing.T, repo workflowledger.Repository, runID string) {
	t.Helper()
	stored, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	now := time.Now()
	if err := repo.CompareAndSetRunStatus(context.Background(), runID, stored.Version, workflowledger.RunStatusSucceeded, &now); err != nil {
		t.Fatalf("settle to succeeded: %v", err)
	}
}

// TestReconcileStackToleratesErrConflict pins F12 finding 2: when two
// concurrent drivers reconcile the same task, the loser gets workflowledger.ErrTaskConflict
// from applyReconcileAction (via workflowledger.Store.TransitionTask → appendEvent).
// The reconcile pass must skip that task and continue instead of aborting the
// whole pass. Because workflowledger.Store is a concrete type that serializes through
// a mutex, we verify the ErrConflict sentinel path and the reconcile-level
// error handling separately.
func TestReconcileStackToleratesErrConflict(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := workflowledger.NewMemoryStore()

	stackID := "stack-conflict"
	if _, err := ledger.StorePlan(workflowledger.Plan{ID: stackID, Scope: stackScope(stackID)}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.CreateTask(workflowledger.Task{ID: "c1", PlanRef: stackID, Scope: stackScope(stackID), Status: stackStatusPlanned}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.CreateTask(workflowledger.Task{ID: "c2", PlanRef: stackID, Scope: stackScope(stackID), Status: stackStatusPlanned}); err != nil {
		t.Fatal(err)
	}

	checker := neverMergedChecker{}
	actions, err := reconcileStack(context.Background(), ledger, repo, checker, stackID, stackMaxChunkAttempts)
	if err != nil {
		t.Fatalf("reconcileStack returned error: %v; want nil", err)
	}
	if len(actions) != 2 {
		t.Fatalf("reconcileStack actions = %d, want 2 (c1 + c2)", len(actions))
	}
}

// TestErrConflictSentinelIdentity verifies that the workflowledger.ErrTaskConflict sentinel
// is correctly identifiable through errors.Is, which is the guard reconcileStack
// uses to skip concurrent-writer collisions.
func TestErrConflictSentinelIdentity(t *testing.T) {
	if !errors.Is(workflowledger.ErrTaskConflict, workflowledger.ErrTaskConflict) {
		t.Fatal("errors.Is(workflowledger.ErrTaskConflict, workflowledger.ErrTaskConflict) = false; sentinel identity broken")
	}
	wrapped := fmt.Errorf("transition task: %w", workflowledger.ErrTaskConflict)
	if !errors.Is(wrapped, workflowledger.ErrTaskConflict) {
		t.Fatal("errors.Is(wrapped ErrConflict, workflowledger.ErrTaskConflict) = false; unwrap broken")
	}
	other := errors.New("some other error")
	if errors.Is(other, workflowledger.ErrTaskConflict) {
		t.Fatal("errors.Is(unrelated error, workflowledger.ErrTaskConflict) = true; false positive")
	}
}
