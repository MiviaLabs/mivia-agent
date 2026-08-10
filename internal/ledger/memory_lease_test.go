package ledger

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryClaimHeartbeatExtendsLease(t *testing.T) {
	repo := NewMemoryLedgerRepository()
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return now })
	if err := repo.ClaimRun(context.Background(), "run-1", "live"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(40 * time.Second)
	if err := repo.ClaimRun(context.Background(), "run-1", "live"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Second)
	if err := repo.TakeoverExpiredRunClaim(context.Background(), "run-1", "other", time.Minute); !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("takeover error = %v, want ErrClaimHeld", err)
	}
	now = now.Add(31 * time.Second)
	if err := repo.TakeoverExpiredRunClaim(context.Background(), "run-1", "other", time.Minute); err != nil {
		t.Fatalf("expired takeover: %v", err)
	}
}
