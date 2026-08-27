package ledger

import (
	"context"
	"testing"

	sdkdf "github.com/MiviaLabs/mivia-ai-sdk/durablefence"
)

// TestRunClaimPassesDurableFenceChecks drives the shared durable-ownership
// harness over the workflow run claim, both backends ("memory" and "sqlite"),
// once per RunAll. Both backends must pass every Check* function, because a
// claim that is exclusive in memory and not in SQLite (or the reverse) is the
// defect, not a backend difference (INV-DUR-2; defect class DC-2).
func TestRunClaimPassesDurableFenceChecks(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			repo := newFenceRepo(t, backend)
			run := runID(t, backend)
			snap, snapshotJSON := newRun(t, run)
			if err := repo.CreateRun(context.Background(), snap, snapshotJSON); err != nil {
				t.Fatalf("CreateRun: %v", err)
			}
			sdkdf.RunAll(t, context.Background(), newRunClaimScenario(repo, run))
		})
	}
}

// newRunClaimScenario adapts StorageRepository's run-claim surface to the SDK
// Scenario shape. The run ID is captured by closure so the scenario can be
// reused across RunAll's seven Check* calls without re-resolving the run.
//
// Claim and Takeover mint a fresh holder identity per call (counter-suffixed)
// so the SDK's CheckClaimRejectsWhileHeld sees a different holder on the
// second Claim than the first. The returned string is the SDK's opaque
// token: the binding plan treats it as opaque at the check level (threaded
// from one call to the next, never inspected) while the adapter owns the
// runID lookup. Mutate forwards the SDK-passed token into the same Repo
// call the existing fence gate already checks.
func newRunClaimScenario(repo *StorageRepository, run string) sdkdf.Scenario {
	var claims uint64
	var takeovers uint64
	return sdkdf.Scenario{
		Claim: func(ctx context.Context) (string, error) {
			claims++
			holder := holderForCall("owner-a", claims)
			if err := repo.ClaimRun(ctx, run, holder); err != nil {
				return "", err
			}
			return holder, nil
		},
		Takeover: func(ctx context.Context) (string, error) {
			takeovers++
			holder := holderForCall("owner-b", takeovers)
			if err := repo.TakeoverRunClaim(ctx, run, holder); err != nil {
				return "", err
			}
			return holder, nil
		},
		Mutate: func(ctx context.Context, holder string) error {
			return mutateAsHolder(ctx, repo, run, holder)
		},
		Release: func(ctx context.Context, holder string) error {
			return repo.ReleaseRun(ctx, run, holder)
		},
		IsHeld: func(ctx context.Context) (bool, error) {
			return repo.IsRunHeld(ctx, run)
		},
		IsFenced: func(ctx context.Context, token string) (bool, error) {
			return repo.IsRunTokenFenced(ctx, run, token)
		},
	}
}

// holderForCall returns "<prefix>-<n>" where <n> is a monotonic counter so
// every Claim/Takeover surfaces as a distinct holder identity to the
// harness's Check* functions.
func holderForCall(prefix string, n uint64) string {
	const digits = "0123456789"
	if n == 0 {
		return prefix
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return prefix + "-" + string(buf[i:])
}

// newFenceRepo returns a fresh repository for the named backend. Each
// RunAll iteration gets its own repo + run to keep state from leaking across
// the seven Check* calls.
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

// nextLegalStatus returns the next legal NON-TERMINAL run status after from,
// cycling running and waiting_approval so the harness can Mutate twice in one
// scenario without tripping the state machine.
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
