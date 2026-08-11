package ledger

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// This file pins the read-only claim probe (GetRunClaim) and its degradation
// branches. The write-side claim lifecycle is covered in storage_test.go.

// TestStorageRepository_GetRunClaim pins the read-only claim probe: it
// reports the holder and a non-zero acquired_at while a claim is held, reads
// ok=false before any claim and after release, and never disturbs the claim
// (a second holder still fails while the first holds).
func TestStorageRepository_GetRunClaim(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			if _, _, ok, err := repo.GetRunClaim(ctx, run); err != nil || ok {
				t.Fatalf("GetRunClaim before claim: ok=%v err=%v, want ok=false err=nil", ok, err)
			}
			requireErr(t, repo.ClaimRun(ctx, run, "h1"), nil, "claim by h1")
			holder, at, ok, err := repo.GetRunClaim(ctx, run)
			if err != nil || !ok || holder != "h1" {
				t.Fatalf("GetRunClaim = holder %q ok %v err %v, want h1 true nil", holder, ok, err)
			}
			if at.IsZero() {
				t.Fatal("GetRunClaim acquiredAt is zero")
			}
			// The probe is read-only: the claim still fences a second holder.
			requireErr(t, repo.ClaimRun(ctx, run, "h2"), ErrClaimHeld, "claim by h2 while h1 holds")
			// Same-holder refresh keeps the holder visible to the probe.
			requireErr(t, repo.ClaimRun(ctx, run, "h1"), nil, "same holder refresh")
			holder, _, ok, err = repo.GetRunClaim(ctx, run)
			if err != nil || !ok || holder != "h1" {
				t.Fatalf("GetRunClaim after refresh = holder %q ok %v err %v, want h1 true nil", holder, ok, err)
			}
			requireErr(t, repo.ReleaseRun(ctx, run, "h1"), nil, "release by holder")
			if _, _, ok, err := repo.GetRunClaim(ctx, run); err != nil || ok {
				t.Fatalf("GetRunClaim after release: ok=%v err=%v, want ok=false err=nil", ok, err)
			}
		})
	}
}

// TestParseClaimAcquiredAt pins the acquired_at parser: the RFC3339-with-
// millisecond form the SQLite backend writes and the legacy space-separated
// form both parse; garbage errors.
func TestParseClaimAcquiredAt(t *testing.T) {
	rfc, err := parseClaimAcquiredAt("2026-08-11T17:01:47.362Z")
	if err != nil {
		t.Fatalf("parse RFC3339: %v", err)
	}
	if rfc.Year() != 2026 || rfc.Month() != time.August || rfc.Second() != 47 {
		t.Fatalf("parsed RFC3339 = %v, want 2026-08-11T17:01:47Z", rfc)
	}
	legacy, err := parseClaimAcquiredAt("2026-08-11 17:01:47")
	if err != nil {
		t.Fatalf("parse legacy: %v", err)
	}
	if legacy.Hour() != 17 || legacy.Minute() != 1 {
		t.Fatalf("parsed legacy = %v, want 17:01:47", legacy)
	}
	if _, err := parseClaimAcquiredAt("garbage"); err == nil {
		t.Fatal("parse garbage must error")
	}
}

// claimReadFailStore wraps a memory store and fails GetClaim with a plain
// backend error (not ErrClaimNotHeld), so GetRunClaim must propagate it.
type claimReadFailStore struct {
	*storage.Memory
}

func (s *claimReadFailStore) GetClaim(context.Context, string) (storage.Claim, error) {
	return storage.Claim{}, errors.New("claim read boom")
}

// claimReadGarbageStore wraps a memory store and returns a claim whose
// acquired_at cannot be parsed, so GetRunClaim must propagate the parse error.
type claimReadGarbageStore struct {
	*storage.Memory
}

func (s *claimReadGarbageStore) GetClaim(context.Context, string) (storage.Claim, error) {
	return storage.Claim{RunID: "wfr-x", Holder: "h1", AcquiredAt: "garbage"}, nil
}

// nonClaimReaderStore shadows the embedded GetClaim with a different
// signature, so the type satisfies the Store interface (all promoted methods)
// but not the optional ClaimReader extension: GetRunClaim must degrade to
// ok=false instead of erroring.
type nonClaimReaderStore struct {
	*storage.Memory
}

func (s *nonClaimReaderStore) GetClaim(context.Context, string) error { return nil }

// TestStorageRepository_GetRunClaimDegradations pins the read-only claim
// probe's degradation branches: a closed repository errors, a backend that
// cannot expose claims reads ok=false, a backend read failure propagates, and
// an unparsable acquired_at propagates. The happy path is covered by
// TestStorageRepository_GetRunClaim.
func TestStorageRepository_GetRunClaimDegradations(t *testing.T) {
	ctx := context.Background()

	t.Run("closed repository errors", func(t *testing.T) {
		repo := newMemoryRepo(t)
		repo.Close()
		if _, _, _, err := repo.GetRunClaim(ctx, "wfr-x"); !errors.Is(err, ErrClosed) {
			t.Fatalf("GetRunClaim on a closed repository = %v, want ErrClosed", err)
		}
	})

	t.Run("backend without claim reads degrades to no claim", func(t *testing.T) {
		repo := NewStorageRepository(&nonClaimReaderStore{Memory: storage.NewMemory()})
		if _, _, ok, err := repo.GetRunClaim(ctx, "wfr-x"); err != nil || ok {
			t.Fatalf("non-reader GetRunClaim = ok %v err %v, want false nil", ok, err)
		}
	})

	t.Run("backend read error propagates", func(t *testing.T) {
		repo := NewStorageRepository(&claimReadFailStore{Memory: storage.NewMemory()})
		if _, _, _, err := repo.GetRunClaim(ctx, "wfr-x"); err == nil {
			t.Fatal("GetRunClaim must propagate the backend read error")
		}
	})

	t.Run("unparsable acquired_at propagates", func(t *testing.T) {
		repo := NewStorageRepository(&claimReadGarbageStore{Memory: storage.NewMemory()})
		if _, _, _, err := repo.GetRunClaim(ctx, "wfr-x"); err == nil {
			t.Fatal("GetRunClaim must propagate the acquired_at parse error")
		}
	})
}
