package ledger

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/durablefence"
)

// The run claim is the repository's durable-ownership surface. It carries
// INV-DUR-2 and defect classes DC-2 and DC-3. The shared harness states the
// scenarios once so a new ownership surface inherits them instead of
// re-deriving them, which is how this class produced a chain of repeat fixes.
//
// Both backends run: a claim that is exclusive in memory and not in SQLite (or
// the reverse) is the defect, not a backend difference.
func TestRunClaimPassesDurableFenceChecks(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		durablefence.Run(t, backend, func(tb testing.TB) durablefence.Scenario {
			return newRunClaimScenario(tb, backend)
		})
	}
}

func newRunClaimScenario(tb testing.TB, backend string) durablefence.Scenario {
	tb.Helper()
	t, ok := tb.(*testing.T)
	if !ok {
		tb.Fatalf("run claim scenario needs *testing.T, got %T", tb)
	}
	repo := newFenceRepo(t, backend)
	run := runID(t, backend)
	snap, snapshotJSON := newRun(t, run)
	if err := repo.CreateRun(context.Background(), snap, snapshotJSON); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return durablefence.Scenario{
		Name:     "workflow run claim (" + backend + ")",
		Claim:    func(ctx context.Context, holder string) error { return repo.ClaimRun(ctx, run, holder) },
		Takeover: func(ctx context.Context, holder string) error { return repo.TakeoverRunClaim(ctx, run, holder) },
		Release:  func(ctx context.Context, holder string) error { return repo.ReleaseRun(ctx, run, holder) },
		Mutate: func(ctx context.Context, holder string) error {
			return mutateAsHolder(ctx, repo, run, holder)
		},
		IsHeld: durablefence.ErrIs(ErrClaimHeld),
	}
}

func newFenceRepo(t *testing.T, backend string) *StorageRepository {
	t.Helper()
	if backend == "memory" {
		return newMemoryRepo(t)
	}
	repo, done := newSQLiteRepo(t)
	t.Cleanup(done)
	return repo
}

// mutateAsHolder performs one durable status mutation attributed to holder. It
// reads the live version, so a caller never sets against a stale one (DC-3).
//
// The target must be a legal transition from the live status, or the state
// machine refuses the write before the claim is ever consulted and the check
// would pass for the wrong reason. running and waiting_approval form a legal
// cycle, so repeated mutations stay valid.
func mutateAsHolder(ctx context.Context, repo *StorageRepository, run, holder string) error {
	current, err := repo.GetRun(ctx, run)
	if err != nil {
		return err
	}
	next, ok := nextLegalStatus(current.Status)
	if !ok {
		return ErrInvalidTransition
	}
	return repo.CompareAndSetRunStatus(ContextWithClaimHolder(ctx, holder), run, current.Version, next, nil)
}

func nextLegalStatus(from RunStatus) (RunStatus, bool) {
	switch from {
	case RunStatusPending:
		return RunStatusRunning, true
	case RunStatusRunning:
		return RunStatusWaitingApproval, true
	case RunStatusWaitingApproval:
		return RunStatusRunning, true
	default:
		return "", false
	}
}
