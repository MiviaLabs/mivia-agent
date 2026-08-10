package coordinator

import (
	"context"
	"errors"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

const defaultRunClaimLease = 5 * time.Minute

func (c *coordinator) claimRun(ctx context.Context, runID string) error {
	err := c.repo.ClaimRun(ctx, runID, c.holderID)
	if !errors.Is(err, ledger.ErrClaimHeld) {
		return err
	}
	lease, ok := c.repo.(ledger.LeaseRepository)
	if !ok {
		return err
	}
	return lease.TakeoverExpiredRunClaim(ctx, runID, c.holderID, c.claimLease)
}

func (c *coordinator) startClaimHeartbeat(h *RunHandle) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(c.claimHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := c.repo.ClaimRun(ctx, h.runID, c.holderID); errors.Is(err, ledger.ErrClaimHeld) {
					h.cancel()
					return
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
