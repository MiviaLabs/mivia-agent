package coordinator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

type nonLeaseClaimRepository struct{ ledger.LedgerRepository }

type heldClaimRepository struct{ ledger.LedgerRepository }

func (heldClaimRepository) ClaimRun(context.Context, string, string) error {
	return ledger.ErrClaimHeld
}

func TestClaimRunRefusesHeldClaimWithoutLeaseSupport(t *testing.T) {
	base := ledger.NewMemoryLedgerRepository()
	if err := base.ClaimRun(context.Background(), "run", "other"); err != nil {
		t.Fatal(err)
	}
	c := New(nonLeaseClaimRepository{LedgerRepository: base}, nil).(*coordinator)
	if err := c.claimRun(context.Background(), "run"); !errors.Is(err, ledger.ErrClaimHeld) {
		t.Fatalf("claim error = %v, want ErrClaimHeld", err)
	}
}

func TestClaimHeartbeatCancelsAfterLeaseTheft(t *testing.T) {
	base := ledger.NewMemoryLedgerRepository()
	c := New(heldClaimRepository{LedgerRepository: base}, nil).(*coordinator)
	c.claimHeartbeat = time.Millisecond
	h := c.newRunHandle("run", "", nil, "", false)
	stop := c.startClaimHeartbeat(h)
	defer stop()
	select {
	case <-h.poolCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not cancel the stale executor")
	}
}
