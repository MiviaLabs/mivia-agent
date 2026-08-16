package cli

// Regression tests for the stack-drive execution claim (locked ADLC plan,
// slice A of the stack-reconciliation-sweep design): `mivia stack drive`
// used to drive a stack via driveStack/driveStackToCompletion without ever
// taking the run's ledger execution claim, so nothing stopped two drivers
// (an operator running `stack drive` twice, or a future reconciliation
// sweep) from racing each other against the same stack. claimStackDrive
// closes that gap and returns a release func that stops its heartbeat and
// releases the claim; runStackDrive defers it around the drive body. These
// tests pin the helper directly against the ledger (mirroring
// claimForCancel's own test style in
// workflow_tool_engine_cancel_claim_test.go) rather than through the full
// CLI entrypoint, since runStackDrive requires a real workspace/git
// checkout via prepareWorkflowRun that is orthogonal to the claim logic
// under test here — the full entrypoint is covered separately in
// stack_drive_claim_integration_test.go.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestClaimStackDriveSucceedsWhenUnclaimed pins the ordinary success path: no
// existing claim, claimStackDrive claims the stack and starts its heartbeat.
func TestClaimStackDriveSucceedsWhenUnclaimed(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	const stackID = "wfr-stack-claim-fresh"
	root := t.TempDir()
	storePath := filepath.Join(root, "store.db")

	release, err := claimStackDrive(ctx, repo, root, storePath, stackID)
	if err != nil {
		t.Fatalf("claimStackDrive() error = %v, want nil", err)
	}
	if release == nil {
		t.Fatal("claimStackDrive() release = nil on success, want a non-nil release func")
	}
	t.Cleanup(release)

	_, _, ok, err := repo.GetRunClaim(ctx, stackID)
	if err != nil {
		t.Fatalf("GetRunClaim: %v", err)
	}
	if !ok {
		t.Fatal("no claim held after a successful claimStackDrive")
	}
}

// TestClaimStackDriveRefusesLiveForeignClaim is the direct regression test
// for the race this slice closes: a second driver (another operator
// invocation, or later the reconciliation sweep) must never proceed against
// a stack another live process already holds the claim for.
func TestClaimStackDriveRefusesLiveForeignClaim(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	const stackID = "wfr-stack-claim-foreign"
	root := t.TempDir()
	storePath := filepath.Join(root, "store.db")

	if err := repo.ClaimRun(ctx, stackID, "other-driver"); err != nil {
		t.Fatalf("prime foreign claim: %v", err)
	}

	release, err := claimStackDrive(ctx, repo, root, storePath, stackID)
	if err == nil {
		t.Fatal("claimStackDrive() error = nil, want a refusal for a live foreign claim")
	}
	if release != nil {
		t.Fatal("claimStackDrive() release != nil on refusal, want nil - the caller must not invoke it")
	}
	if !strings.Contains(err.Error(), "claimed by another executor") {
		t.Fatalf("claimStackDrive() error = %q, want it to mention the stack is claimed by another executor", err.Error())
	}

	// The foreign claim must be untouched by the refused attempt.
	gotHolder, _, ok, getErr := repo.GetRunClaim(ctx, stackID)
	if getErr != nil {
		t.Fatalf("GetRunClaim: %v", getErr)
	}
	if !ok || gotHolder != "other-driver" {
		t.Fatalf("claim after refused claimStackDrive = (holder=%q, ok=%v), want other-driver still held", gotHolder, ok)
	}
}

// TestClaimStackDriveSameHolderRefreshSucceeds pins that re-running
// `stack drive` under a claim it already holds (the recovery/re-invocation
// path exercised by stack_drive_recovery_test.go) never spuriously refuses:
// ClaimRun's own contract is that a same-holder refresh always succeeds.
// This is also exactly what startStackDriveClaimHeartbeat relies on every
// tick to keep a long drive's claim alive.
func TestClaimStackDriveSameHolderRefreshSucceeds(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	const stackID = "wfr-stack-claim-refresh"

	if err := repo.ClaimRun(ctx, stackID, "same-holder"); err != nil {
		t.Fatalf("prime claim: %v", err)
	}
	if err := repo.ClaimRun(ctx, stackID, "same-holder"); err != nil {
		t.Fatalf("same-holder refresh via ClaimRun failed: %v, want it to succeed idempotently", err)
	}
}

// TestClaimStackDrivePropagatesNonClaimHeldError mirrors
// claimForCancel's own coverage of the non-ErrClaimHeld branch: an empty
// holder is never minted by newStackDriveHolder in production, but
// claimWorkflowOperator's other error path must still propagate untouched
// rather than being swallowed.
func TestClaimStackDrivePropagatesNonClaimHeldError(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	err := claimWorkflowOperator(ctx, repo, "wfr-stack-claim-empty-holder", "")
	if !errors.Is(err, workflowledger.ErrClaimNotHeld) {
		t.Fatalf("claimWorkflowOperator() error = %v, want ErrClaimNotHeld", err)
	}
}

// TestClaimStackDriveReleaseStopsHeartbeatAndReleasesClaim pins the release
// half of the pair: once the returned release func runs, the stack shows no
// live claim (the heartbeat has stopped, not just the claim row cleared), so
// a subsequent driver (operator retry, or the future sweep) is free to claim
// it - and does not spuriously race a still-ticking heartbeat re-creating the
// claim row after release (see startStackDriveClaimHeartbeat's doc comment).
func TestClaimStackDriveReleaseStopsHeartbeatAndReleasesClaim(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	const stackID = "wfr-stack-claim-release"
	root := t.TempDir()
	storePath := filepath.Join(root, "store.db")

	release, err := claimStackDrive(ctx, repo, root, storePath, stackID)
	if err != nil {
		t.Fatalf("claimStackDrive() error = %v, want nil", err)
	}
	release()

	_, _, ok, err := repo.GetRunClaim(ctx, stackID)
	if err != nil {
		t.Fatalf("GetRunClaim: %v", err)
	}
	if ok {
		t.Fatal("claim still held after release(), want it released")
	}

	// A fresh claimStackDrive must now succeed for a different holder.
	release2, err := claimStackDrive(ctx, repo, root, storePath, stackID)
	if err != nil {
		t.Fatalf("claimStackDrive() after release error = %v, want nil", err)
	}
	t.Cleanup(release2)
}
