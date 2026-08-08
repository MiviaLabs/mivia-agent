package coordinator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// claimProbeFailingRepo fails the reclaim claim probe with a non-ErrClaimHeld
// error, exercising the transient-probe-failure branch of reclaimAbandonedRun
// (recovery_reclaim.go:67-69): a run we cannot prove abandoned is never
// deleted, and the caller treats the key as contended.
type claimProbeFailingRepo struct {
	*ledger.MemoryLedgerRepository
}

func (claimProbeFailingRepo) ClaimRun(context.Context, string, string) error {
	return errors.New("simulated claim probe failure")
}

// TestReclaimAbandonedRunProbeFailureIsNoReclaim pins recovery_reclaim.go:68-69:
// a failed (non-ErrClaimHeld) claim probe is equally a no-reclaim - the run may
// be live or recoverable by its holder, so DeleteRun must never fire.
func TestReclaimAbandonedRunProbeFailureIsNoReclaim(t *testing.T) {
	repo := &claimProbeFailingRepo{MemoryLedgerRepository: ledger.NewMemoryLedgerRepository()}
	c := newIdempotencyCoordinator(repo).(*coordinator)
	if c.reclaimAbandonedRun("run-x") {
		t.Fatal("reclaim succeeded despite a failed claim probe; a run we cannot prove abandoned must never be deleted")
	}
}

// reclaimDeleteFailingRepo lets the claim probe succeed but makes DeleteRun
// fail, exercising the delete-failure branch of reclaimAbandonedRun
// (recovery_reclaim.go:72-77): the probe claim must be undone so the run is
// left exactly as it was found.
type reclaimDeleteFailingRepo struct {
	*ledger.MemoryLedgerRepository
	releases int
}

func (reclaimDeleteFailingRepo) DeleteRun(context.Context, string) error {
	return errors.New("simulated delete failure")
}

func (r *reclaimDeleteFailingRepo) ReleaseRun(ctx context.Context, runID, holder string) error {
	r.releases++
	return r.MemoryLedgerRepository.ReleaseRun(ctx, runID, holder)
}

// TestReclaimAbandonedRunDeleteFailureUndoesProbeClaim pins
// recovery_reclaim.go:73,75,76,77: when DeleteRun fails, the reclaim is a
// no-reclaim AND the probe claim is released so the run is left as found and
// no stale claim blocks the real owner.
func TestReclaimAbandonedRunDeleteFailureUndoesProbeClaim(t *testing.T) {
	repo := &reclaimDeleteFailingRepo{MemoryLedgerRepository: ledger.NewMemoryLedgerRepository()}
	c := newIdempotencyCoordinator(repo).(*coordinator)
	if c.reclaimAbandonedRun("run-x") {
		t.Fatal("reclaim succeeded despite a failed DeleteRun")
	}
	if repo.releases != 1 {
		t.Fatalf("ReleaseRun calls = %d, want 1 (the probe claim must be undone after a failed delete)", repo.releases)
	}
}

// staleReclaimReProbeFailingRepo fails the reclaim claim probe with
// ErrClaimHeld and then fails the re-probe after ClearRunClaim, exercising the
// re-probe-failure branch of reclaimAbandonedRun (recovery_reclaim.go:98-103):
// a second ErrClaimHeld means another reclaimer won the clear+re-probe race,
// so the reclaim backs off and never deletes the run.
type staleReclaimReProbeFailingRepo struct {
	*ledger.MemoryLedgerRepository
	probes int
}

func (r *staleReclaimReProbeFailingRepo) ClaimRun(context.Context, string, string) error {
	r.probes++
	// First probe sees the stale claim; the re-probe also fails because
	// another reclaimer won the race and re-claimed.
	return ledger.ErrClaimHeld
}

// TestReclaimAbandonedRunReProbeFailureIsNoReclaim pins the fix's step (e): a
// re-probe that fails after the stale claim was cleared is a no-reclaim - the
// run stays with the reclaimer that won the race, and the caller treats the key
// as contended. The stale claim must have been cleared (ClearRunClaim ran) so a
// dead holder's claim never bricks the key.
func TestReclaimAbandonedRunReProbeFailureIsNoReclaim(t *testing.T) {
	repo := &staleReclaimReProbeFailingRepo{MemoryLedgerRepository: ledger.NewMemoryLedgerRepository()}
	// Seed the run and a stale claim so the clear path has real state to act
	// on; the fake ClaimRun reports ErrClaimHeld regardless (both probe and
	// re-probe), so the seed must go through the embedded repository.
	ctx := context.Background()
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "run-x", Status: ledger.RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.MemoryLedgerRepository.ClaimRun(ctx, "run-x", "dead-process-holder"); err != nil {
		t.Fatal(err)
	}
	c := newIdempotencyCoordinator(repo).(*coordinator)
	if c.reclaimAbandonedRun("run-x") {
		t.Fatal("reclaim succeeded despite a failed re-probe; only the winner of the clear+re-probe race may delete")
	}
	if repo.probes != 2 {
		t.Fatalf("ClaimRun calls = %d, want 2 (one probe, one re-probe after the clear)", repo.probes)
	}
	// The stale claim was cleared; the run itself was NOT deleted.
	if _, err := repo.GetRun(ctx, "run-x"); err != nil {
		t.Fatalf("run %q deleted after a failed re-probe: %v; a no-reclaim must leave the run untouched", "run-x", err)
	}
	if err := repo.MemoryLedgerRepository.ClaimRun(ctx, "run-x", "winner-holder"); err != nil {
		t.Fatalf("stale claim not cleared: %v; the dead holder's claim must not survive the clear", err)
	}
}

// TestRecoverIdempotentWithRetryRespectsCanceledContext pins the mid-retry
// cancellation return in recoverIdempotentWithRetry (recovery_reclaim.go:120-124):
// a contended idempotency key plus an already-canceled caller context surfaces
// the context error instead of waiting out the bounded retry budget.
func TestRecoverIdempotentWithRetryRespectsCanceledContext(t *testing.T) {
	// A young 'created + zero tasks' run under K makes recoverByIdempotencyKey
	// report ErrIdempotencyKeyContended deterministically (R4-1), so the retry
	// loop reaches its select with contention on the first pass.
	c, _ := reclaimGuardCoordinator(t, time.Now(), "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, found, err := c.recoverIdempotentWithRetry(ctx, "K", "fingerprint")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("recoverIdempotentWithRetry on canceled ctx: found=%v err=%v, want context.Canceled", found, err)
	}
}
