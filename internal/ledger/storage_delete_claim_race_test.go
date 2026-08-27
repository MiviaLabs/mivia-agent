package ledger

// Regression test for ledger-delete-run-stale-fenced-claim: DeleteRun must
// clear the in-memory claim/watermark mirror even when a concurrent catch-up
// folded the run_deleted tombstone into the projection before DeleteRun's own
// mem.DeleteRun ran (mem.DeleteRun then returns ErrNotFound, and the old early
// return skipped the mandatory cleanup). With the bug, the stale fenced claim
// stayed in claimedRuns and a same-ID recreation failed with ErrClaimHeld
// forever on that instance. The raced ErrNotFound path is the negative path:
// mem.DeleteRun returning ErrNotFound must not skip the cleanup.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// deleteHookStore wraps a storage.Store and, after a successful
// AppendAndDeleteRun commit, folds the deletion tombstone into the repository
// projection before DeleteRun's own mem.DeleteRun runs - a deterministic
// reproduction of the concurrent catch-up race (no timing).
//
// The fold goes through repo.ensureBuilt so it exercises the real catch-up
// path (applyTail -> applyStoreEventLocked). applyTail deliberately skips
// events whose sequence is still in flight, so the hook first releases the
// writer's in-flight claim on the tombstone sequence (the same release
// DeleteRun's failure branch performs via clearInflight); that is what makes
// the tombstone visible to the concurrent reader's catch-up.
type deleteHookStore struct {
	storage.Store
	repo *StorageLedgerRepository
}

func (s *deleteHookStore) AppendAndDeleteRun(ctx context.Context, tombstone storage.Event, claim storage.Claim) error {
	if err := s.Store.AppendAndDeleteRun(ctx, tombstone, claim); err != nil {
		return err
	}
	s.repo.clearInflight(tombstone.RunID, uint64(tombstone.Sequence))
	return s.repo.ensureBuilt(ctx)
}

// FencedLeaseStore forwarding: the wrapper must present the fenced surface to
// the repository so ClaimRun stores a real fenced claim (and DeleteRun's
// AppendAndDeleteRun is authorized against it) instead of falling back to the
// zero-value claim path.
func (s *deleteHookStore) ClaimRunFenced(ctx context.Context, runID, holder string) (storage.Claim, error) {
	return s.Store.(storage.FencedLeaseStore).ClaimRunFenced(ctx, runID, holder)
}

func (s *deleteHookStore) TakeoverExpiredClaimFenced(ctx context.Context, runID, holder string, maxAge time.Duration) (storage.Claim, error) {
	return s.Store.(storage.FencedLeaseStore).TakeoverExpiredClaimFenced(ctx, runID, holder, maxAge)
}

func (s *deleteHookStore) AppendClaimedFenced(ctx context.Context, evt storage.Event, claim storage.Claim) error {
	return s.Store.(storage.FencedLeaseStore).AppendClaimedFenced(ctx, evt, claim)
}

func (s *deleteHookStore) ReleaseClaimFenced(ctx context.Context, claim storage.Claim) error {
	return s.Store.(storage.FencedLeaseStore).ReleaseClaimFenced(ctx, claim)
}

func (s *deleteHookStore) TakeoverClaimFenced(ctx context.Context, runID, holder string) (storage.Claim, error) {
	return s.Store.(storage.FencedLeaseStore).TakeoverClaimFenced(ctx, runID, holder)
}

func (s *deleteHookStore) RefreshClaimFenced(ctx context.Context, runID, holder string) (storage.Claim, error) {
	return s.Store.(storage.FencedLeaseStore).RefreshClaimFenced(ctx, runID, holder)
}

func TestDeleteRunCleansClaimsWhenCatchUpDroppedRunFirst(t *testing.T) {
	ctx := context.Background()
	runID := "run-race-delete"

	t.Run("memory", func(t *testing.T) {
		hook := &deleteHookStore{Store: storage.NewMemory()}
		repo := NewBorrowedStorageLedgerRepository(hook)
		hook.repo = repo
		runDeleteCatchUpRace(t, ctx, repo, runID)
	})
	t.Run("sqlite", func(t *testing.T) {
		underlying, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
		if err != nil {
			t.Fatal(err)
		}
		hook := &deleteHookStore{Store: underlying}
		repo := NewBorrowedStorageLedgerRepository(hook)
		hook.repo = repo
		t.Cleanup(func() { _ = underlying.Close() })
		runDeleteCatchUpRace(t, ctx, repo, runID)
	})
}

func runDeleteCatchUpRace(t *testing.T, ctx context.Context, repo *StorageLedgerRepository, runID string) {
	t.Helper()
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: runID, Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClaimRun(ctx, runID, "holder-a"); err != nil {
		t.Fatal(err)
	}
	// The store commit succeeds, then the hook folds the tombstone into the
	// projection. DeleteRun must return nil (not the raced ErrNotFound from
	// mem.DeleteRun) and must still run the claim/watermark cleanup.
	if err := repo.DeleteRun(ctx, runID); err != nil {
		t.Fatalf("DeleteRun after catch-up folded tombstone first: %v, want nil", err)
	}

	_, claimPresent := repo.claims.GetClaim(runID)
	appliedPresent := repo.engine.Watermarks().Applied(runID) != 0
	allocatedPresent := repo.engine.Watermarks().Allocated(runID) != 0
	if claimPresent {
		t.Fatal("claimedRuns still holds a stale fenced claim after DeleteRun")
	}
	if appliedPresent {
		t.Fatal("applied watermark retained the deleted run")
	}
	if allocatedPresent {
		t.Fatal("allocated watermark retained the deleted run")
	}

	// INV-AG-14: after durable deletion the instance must allow a same-ID
	// recreation; the stale claim would have made this fail with ErrClaimHeld
	// forever on this instance.
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: runID, Status: RunStatusCreated}); err != nil {
		t.Fatalf("recreate same run ID after raced delete: %v", err)
	}
	if _, err := repo.GetRun(ctx, runID); err != nil {
		t.Fatalf("GetRun after recreate: %v", err)
	}
}
