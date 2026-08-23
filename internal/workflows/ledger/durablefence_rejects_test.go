package ledger

import (
	"context"
	"testing"

	sdkdf "github.com/MiviaLabs/mivia-ai-sdk/durablefence"
)

// TestRunClaimRejectsDefectiveScenarios asserts the SDK harness still flags
// a broken Scenario. Three defect classes are exercised: a Claim that
// always succeeds on a held run, a Mutate that swallows the fenced error,
// and a Takeover that returns a new token without bumping the prior
// owner's fence. Each iteration of the backend loop builds a fresh repo +
// Scenario; each rejection is verified in a dedicated subtest that drives
// sdkdf.RunAll through a swallowTB which captures failures without
// bubbling them into THIS test.
func TestRunClaimRejectsDefectiveScenarios(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			repo := newFenceRepo(t, backend)
			run := runID(t, backend, "rejects")
			snap, snapshotJSON := newRun(t, run)
			if err := repo.CreateRun(context.Background(), snap, snapshotJSON); err != nil {
				t.Fatalf("CreateRun: %v", err)
			}

			t.Run("ClaimAlwaysSucceedsWhileHeld", func(t *testing.T) {
				expectRunAllFail(t, defectiveClaimAlwaysSucceeds(repo, run))
			})
			t.Run("MutateSwallowsFencedError", func(t *testing.T) {
				expectRunAllFail(t, defectiveMutateSwallowsFence(repo, run))
			})
			t.Run("TakeoverDoesNotBumpFence", func(t *testing.T) {
				expectRunAllFail(t, defectiveTakeoverNoFenceBump(repo, run))
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
// outward. Helper, Errorf, Fatalf, Fatal, Fail, and FailNow are captured
// locally; the embedded testing.TB handles Log/Skip-style methods that
// the SDK does not exercise.
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

// defectiveClaimAlwaysSucceeds builds a Scenario whose Claim never refuses
// a second holder on the same run. CheckClaimRejectsWhileHeld expects a
// non-nil error from a second Claim while held, so the check fires t.Fatal.
func defectiveClaimAlwaysSucceeds(repo *StorageRepository, run string) sdkdf.Scenario {
	_ = repo
	_ = run
	return sdkdf.Scenario{
		Claim:    func(_ context.Context) (string, error) { return "owner", nil },
		Takeover: func(_ context.Context) (string, error) { return "owner-b", nil },
		Mutate:   func(_ context.Context, _ string) error { return nil },
		Release:  func(_ context.Context, _ string) error { return nil },
		IsHeld:   func(_ context.Context) (bool, error) { return true, nil },
		IsFenced: func(_ context.Context, _ string) (bool, error) { return false, nil },
	}
}

// defectiveMutateSwallowsFence builds a Scenario whose Mutate returns nil
// even when called under a fenced token. CheckTakeoverFencesPreviousOwner
// expects Mutate(A) to error after Takeover, so the check fires t.Fatal.
func defectiveMutateSwallowsFence(repo *StorageRepository, run string) sdkdf.Scenario {
	return sdkdf.Scenario{
		Claim: func(ctx context.Context) (string, error) {
			if err := repo.ClaimRun(ctx, run, "owner-a"); err != nil {
				return "", err
			}
			return "owner-a", nil
		},
		Takeover: func(ctx context.Context) (string, error) {
			if err := repo.TakeoverRunClaim(ctx, run, "owner-b"); err != nil {
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

// defectiveTakeoverNoFenceBump builds a Scenario whose Takeover returns a
// new token but reports IsFenced(A) = false forever. The
// CheckTakeoverFencesPreviousOwner check asserts IsFenced(A) = true after
// Takeover, so the check fires t.Fatal.
func defectiveTakeoverNoFenceBump(repo *StorageRepository, run string) sdkdf.Scenario {
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
		Mutate:   func(ctx context.Context, holder string) error { return mutateAsHolder(ctx, repo, run, holder) },
		Release:  func(ctx context.Context, holder string) error { return repo.ReleaseRun(ctx, run, holder) },
		IsHeld:   func(ctx context.Context) (bool, error) { return repo.IsRunHeld(ctx, run) },
		IsFenced: func(_ context.Context, _ string) (bool, error) { return false, nil },
	}
}
