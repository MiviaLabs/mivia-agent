package ledger

import (
	"context"
	"testing"
	"time"

	sdkdf "github.com/MiviaLabs/mivia-ai-sdk/durablefence"
)

// TestStorageLedgerRunClaimRejectsDefectiveScenarios asserts the SDK
// harness still flags a broken Scenario when wired to the durable
// coordinator ledger. Three defect classes are exercised: a Claim that
// always succeeds on a held run, a Mutate that swallows the fenced error,
// and a Takeover that returns a new token without bumping the prior
// owner's fence. Each iteration builds a fresh repo + Scenario; each
// rejection is verified in a dedicated subtest that drives sdkdf.RunAll
// through a swallowTB, which records Errorf/Fatal/FailNow without
// bubbling the failure back into THIS test.
func TestStorageLedgerRunClaimRejectsDefectiveScenarios(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			repoA, _, done := newReleaseHolderPair(t, backend)
			defer done()
			repo := repoA
			run := "run-rejects-" + backend
			if err := repo.CreateRun(context.Background(), "key-"+run, RunSnapshot{RunID: run, Status: RunStatusCreated, CreatedAt: time.Now()}); err != nil {
				t.Fatalf("CreateRun: %v", err)
			}

			t.Run("ClaimAlwaysSucceedsWhileHeld", func(t *testing.T) {
				expectRunAllFail(t, defectiveStorageClaimAlwaysSucceeds())
			})
			t.Run("MutateSwallowsFencedError", func(t *testing.T) {
				expectRunAllFail(t, defectiveStorageMutateSwallowsFence(repo, run))
			})
			t.Run("TakeoverDoesNotBumpFence", func(t *testing.T) {
				expectRunAllFail(t, defectiveStorageTakeoverNoFenceBump(repo, run))
			})
		})
	}
}

// expectRunAllFail asserts sdkdf.RunAll flags the broken scenario. The
// SDK's RunAll detects a non-*testing.T argument and skips its inner
// t.Run subtest wrapper; it calls the Check* functions directly against
// our swallowTB, which records the first Errorf/Fatal/FailNow without
// unwinding the goroutine the way a real *testing.T would.
func expectRunAllFail(t *testing.T, broken sdkdf.Scenario) {
	t.Helper()
	got := &swallowTB{TB: t, fail: false}
	func() {
		defer func() {
			_ = recover()
		}()
		sdkdf.RunAll(got, context.Background(), broken)
	}()
	if !got.fail {
		t.Fatal("RunAll passed a defective scenario")
	}
}

// swallowTB is a testing.TB that records whether any failure-shaping
// method was called and never propagates FailNow's runtime.Goexit
// outward. Helper, Errorf, Fatalf, Fatal are captured locally; the
// embedded testing.TB handles Log/Skip-style methods that the SDK does
// not exercise.
type swallowTB struct {
	testing.TB
	fail bool
}

func (s *swallowTB) Helper() {}

func (s *swallowTB) Errorf(format string, args ...any) {
	s.fail = true
}

func (s *swallowTB) Fatal(args ...any) {
	s.fail = true
}

func (s *swallowTB) Fatalf(format string, args ...any) {
	s.fail = true
}

func (s *swallowTB) FailNow() {
	s.fail = true
}

func (s *swallowTB) Fail() {
	s.fail = true
}

// defectiveStorageClaimAlwaysSucceeds builds a Scenario whose Claim never
// refuses a second holder on the same run. CheckClaimRejectsWhileHeld
// expects a non-nil error from a second Claim while held, so the check
// fires t.Fatal.
func defectiveStorageClaimAlwaysSucceeds() sdkdf.Scenario {
	return sdkdf.Scenario{
		Claim:    func(_ context.Context) (string, error) { return "owner", nil },
		Takeover: func(_ context.Context) (string, error) { return "owner-b", nil },
		Mutate:   func(_ context.Context, _ string) error { return nil },
		Release:  func(_ context.Context, _ string) error { return nil },
		IsHeld:   func(_ context.Context) (bool, error) { return true, nil },
		IsFenced: func(_ context.Context, _ string) (bool, error) { return false, nil },
	}
}

// defectiveStorageMutateSwallowsFence builds a Scenario whose Mutate
// returns nil even when called under a fenced token. The
// CheckTakeoverFencesPreviousOwner check expects Mutate(A) to error after
// Takeover, so the check fires t.Fatal.
func defectiveStorageMutateSwallowsFence(repo *StorageLedgerRepository, run string) sdkdf.Scenario {
	return sdkdf.Scenario{
		Claim: func(ctx context.Context) (string, error) {
			if err := repo.ClaimRun(ctx, run, "owner-a"); err != nil {
				return "", err
			}
			return "owner-a", nil
		},
		Takeover: func(ctx context.Context) (string, error) {
			if err := repo.TakeoverExpiredRunClaim(ctx, run, "owner-b", 0); err != nil {
				return "", err
			}
			return "owner-b", nil
		},
		Mutate:   func(_ context.Context, _ string) error { return nil },
		Release:  func(ctx context.Context, h string) error { return repo.ReleaseRun(ctx, run, h) },
		IsHeld:   func(ctx context.Context) (bool, error) { return repo.IsRunHeld(ctx, run) },
		IsFenced: func(_ context.Context, _ string) (bool, error) { return false, nil },
	}
}

// defectiveStorageTakeoverNoFenceBump builds a Scenario whose Takeover
// returns a new token but reports IsFenced(A) = false forever. The
// CheckTakeoverFencesPreviousOwner check asserts IsFenced(A) = true after
// Takeover, so the check fires t.Fatal.
func defectiveStorageTakeoverNoFenceBump(repo *StorageLedgerRepository, run string) sdkdf.Scenario {
	return sdkdf.Scenario{
		Claim: func(ctx context.Context) (string, error) {
			if err := repo.ClaimRun(ctx, run, "owner-a"); err != nil {
				return "", err
			}
			return "owner-a", nil
		},
		Takeover: func(ctx context.Context) (string, error) {
			if err := repo.ClaimRun(ctx, run, "owner-b"); err != nil {
				return "", err
			}
			return "owner-b", nil
		},
		Mutate: func(ctx context.Context, holder string) error {
			held, err := repo.IsRunHeld(ctx, run)
			if err != nil {
				return err
			}
			if !held {
				return ErrClaimHeld
			}
			if fenced, err := repo.IsRunTokenFenced(ctx, run, holder); err != nil {
				return err
			} else if fenced {
				return ErrClaimHeld
			}
			return nil
		},
		Release:  func(ctx context.Context, h string) error { return repo.ReleaseRun(ctx, run, h) },
		IsHeld:   func(ctx context.Context) (bool, error) { return repo.IsRunHeld(ctx, run) },
		IsFenced: func(_ context.Context, _ string) (bool, error) { return false, nil },
	}
}
