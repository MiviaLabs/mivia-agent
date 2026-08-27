package cliworkflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestClaimForCancelRefusesUnexpiredForeignClaim is a direct regression test
// for claimForCancel's ErrClaimHeld branch: when another holder already owns
// a fresh (unexpired) claim, ClaimRun fails with ErrClaimHeld,
// TakeoverExpiredRunClaim then also fails (the lease has not expired, so it
// is not ErrClaimNotHeld), and claimForCancel must refuse with a wrapped
// "claimed by another executor" error rather than silently taking over a
// live delivery claim.
func TestClaimForCancelRefusesUnexpiredForeignClaim(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	const runID = "wfr-claim-for-cancel-refuse"

	if err := repo.ClaimRun(ctx, runID, "holder-a"); err != nil {
		t.Fatalf("prime foreign claim: %v", err)
	}

	err := claimForCancel(ctx, repo, runID, "holder-b")
	if err == nil {
		t.Fatal("claimForCancel() error = nil, want a refusal for an unexpired foreign claim")
	}
	if !strings.Contains(err.Error(), "claimed by another executor") {
		t.Fatalf("claimForCancel() error = %q, want it to mention the run is claimed by another executor", err.Error())
	}

	// The foreign claim must be untouched: claimForCancel refused, it did not
	// clear or replace holder-a's claim.
	holder, _, ok, err := repo.GetRunClaim(ctx, runID)
	if err != nil {
		t.Fatalf("GetRunClaim: %v", err)
	}
	if !ok || holder != "holder-a" {
		t.Fatalf("claim after refused claimForCancel = (holder=%q, ok=%v), want holder-a still held", holder, ok)
	}
}

// TestClaimForCancelSucceedsWhenUnclaimed pins the ordinary success path:
// claimForCancel simply claims runID for holder when no other claim exists.
func TestClaimForCancelSucceedsWhenUnclaimed(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	const runID = "wfr-claim-for-cancel-fresh"

	if err := claimForCancel(ctx, repo, runID, "holder-a"); err != nil {
		t.Fatalf("claimForCancel() error = %v, want nil", err)
	}
	holder, _, ok, err := repo.GetRunClaim(ctx, runID)
	if err != nil {
		t.Fatalf("GetRunClaim: %v", err)
	}
	if !ok || holder != "holder-a" {
		t.Fatalf("claim after claimForCancel = (holder=%q, ok=%v), want holder-a", holder, ok)
	}
}

// TestClaimForCancelPropagatesNonClaimHeldError pins claimForCancel's other
// ClaimRun error branch: an empty holder makes the underlying ClaimRun fail
// with ErrClaimNotHeld (not ErrClaimHeld), so claimForCancel must return that
// error directly instead of attempting a takeover.
func TestClaimForCancelPropagatesNonClaimHeldError(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	err := claimForCancel(ctx, repo, "wfr-claim-for-cancel-empty-holder", "")
	if !errors.Is(err, workflowledger.ErrClaimNotHeld) {
		t.Fatalf("claimForCancel() error = %v, want ErrClaimNotHeld", err)
	}
}

// expiredAwayClaimRepository simulates a claim that existed when ClaimRun
// checked it (returning ErrClaimHeld) but was released by its holder before
// TakeoverExpiredRunClaim ran, so the takeover itself finds no claim row at
// all and reports ErrClaimNotHeld - a distinct outcome from both an
// unexpired held claim (ErrClaimHeld) and a genuinely expired one (success).
type expiredAwayClaimRepository struct {
	workflowledger.Repository
	claimCalls int
}

func (r *expiredAwayClaimRepository) ClaimRun(ctx context.Context, runID, holder string) error {
	r.claimCalls++
	if r.claimCalls == 1 {
		return workflowledger.ErrClaimHeld
	}
	return r.Repository.ClaimRun(ctx, runID, holder)
}

func (r *expiredAwayClaimRepository) TakeoverExpiredRunClaim(ctx context.Context, runID, holder string, maxAge time.Duration) error {
	return workflowledger.ErrClaimNotHeld
}

// TestClaimForCancelRetriesClaimAfterTakeoverReportsClaimNotHeld pins
// claimForCancel's `if errors.Is(takeoverErr, ErrClaimNotHeld) { return
// repo.ClaimRun(...) }` retry branch: TakeoverExpiredRunClaim reporting
// ErrClaimNotHeld (the claim vanished between the two calls) must make
// claimForCancel retry a plain ClaimRun rather than treating that as a
// refusal.
func TestClaimForCancelRetriesClaimAfterTakeoverReportsClaimNotHeld(t *testing.T) {
	ctx := context.Background()
	base := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = base.Close() })
	repo := &expiredAwayClaimRepository{Repository: base}
	const runID = "wfr-claim-for-cancel-retry"

	if err := claimForCancel(ctx, repo, runID, "holder-b"); err != nil {
		t.Fatalf("claimForCancel() error = %v, want nil (the retried ClaimRun should succeed)", err)
	}
	if repo.claimCalls != 2 {
		t.Fatalf("ClaimRun calls = %d, want 2 (initial ErrClaimHeld, then the post-takeover retry)", repo.claimCalls)
	}
	holder, _, ok, err := base.GetRunClaim(ctx, runID)
	if err != nil {
		t.Fatalf("GetRunClaim: %v", err)
	}
	if !ok || holder != "holder-b" {
		t.Fatalf("claim after claimForCancel = (holder=%q, ok=%v), want holder-b", holder, ok)
	}
}

// TestSessionWorkflowEngineCancelPropagatesClaimForCancelError is an
// end-to-end regression test for Cancel's claimForCancel propagation (the
// `if err := claimForCancel(...); err != nil { return ...err }` branch): a
// run this process never started (e.active[runID] is nil) that is already
// claimed by another, unexpired holder in the durable ledger must make
// Cancel fail with claimForCancel's refusal, through the full
// openWorkflowResolutionContextBounded + real SQLite store path used by
// `mivia workflow cancel` and the chat-session cancel tool alike.
func TestSessionWorkflowEngineCancelPropagatesClaimForCancelError(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	const runID = "wfr-session-cancel-claim-refuse"
	// An explicit isolated config path (reused for setup and for the engine
	// below) avoids config.Load's ambient search, which - inside this repo's
	// own working tree - can find a real, locally-edited .mivia/mivia.toml
	// with provider state this test has no business depending on (see
	// workflowApprovalTestIsolatedConfigPath).
	configPath := workflowApprovalTestIsolatedConfigPath(t)

	release, repo, _, closeFn, err := openWorkflowResolutionContext(root, configPath, runID)
	if err != nil {
		t.Fatalf("setup openWorkflowResolutionContext: %v", err)
	}
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending}, []byte("{}")); err != nil {
		release()
		closeFn()
		t.Fatalf("CreateRun: %v", err)
	}
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		release()
		closeFn()
		t.Fatalf("GetRun: %v", err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		release()
		closeFn()
		t.Fatalf("CompareAndSetRunStatus: %v", err)
	}
	// A fresh, unexpired foreign claim: this simulates a live delivery claim
	// held by this or another host mid-publish, which Cancel must never
	// silently clear.
	if err := repo.ClaimRun(ctx, runID, "other-live-holder"); err != nil {
		release()
		closeFn()
		t.Fatalf("prime foreign claim: %v", err)
	}
	// Release the setup's own lock/store before Cancel opens its own: Cancel
	// re-derives everything from root, exactly like a cross-process operator
	// invocation would.
	release()
	closeFn()

	engine := NewSessionWorkflowEngine(root, configPath)
	_, err = engine.Cancel(ctx, runID)
	if err == nil {
		t.Fatal("Cancel() error = nil, want the claimForCancel refusal to propagate")
	}
	if !strings.Contains(err.Error(), "claimed by another executor") {
		t.Fatalf("Cancel() error = %q, want it to mention the run is claimed by another executor", err.Error())
	}
}
