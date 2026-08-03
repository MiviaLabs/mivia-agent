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
