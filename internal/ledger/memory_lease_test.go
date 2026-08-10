package ledger

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryLedgerDoesNotTakeOverLiveClaims(t *testing.T) {
	repo := NewMemoryLedgerRepository()
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return now })
	if err := repo.ClaimRun(context.Background(), "run-1", "live"); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClaimRun(context.Background(), "run-1", "other"); !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("second claim error = %v, want ErrClaimHeld", err)
	}
	if _, ok := any(repo).(LeaseRepository); ok {
		t.Fatal("memory ledger must not take over a live in-process claim")
	}
}
