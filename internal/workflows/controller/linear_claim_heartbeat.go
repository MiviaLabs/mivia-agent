package controller

import (
	"context"
	"errors"
	"log"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

var claimHeartbeatInterval = workflowledger.DefaultClaimLease / 3

// startClaimHeartbeat refreshes the claim while one step executes.
// When another holder takes the claim, or the claim row is gone (the step was
// resumed by another holder that released it), it cancels the step context: it
// never re-acquires the claim itself. A transient claim error does not cancel
// the step. The ticker retries on the next interval.
func (c *LinearController) startClaimHeartbeat(cancel context.CancelFunc) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(claimHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// Bound the best-effort claim refresh so a wedged store call cannot
				// deadlock Advance cleanup waiting on the heartbeat goroutine.
				refreshCtx, release := context.WithTimeout(context.Background(), durableHeartbeatTimeout)
				err := c.Repo.RefreshRunClaim(refreshCtx, c.RunID, c.Holder)
				release()
				if err != nil {
					if errors.Is(err, workflowledger.ErrClaimHeld) || errors.Is(err, workflowledger.ErrClaimNotHeld) {
						cancel()
						return
					}
					log.Printf("workflow: run %s claim refresh failed (continuing): %v", c.RunID, err)
				}
			case <-stop:
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}
