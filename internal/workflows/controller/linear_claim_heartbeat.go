package controller

import (
	"context"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

var claimHeartbeatInterval = workflowledger.DefaultClaimLease / 3

// startClaimHeartbeat refreshes the claim while one step executes. If another
// holder takes the claim, it cancels the step context before more work starts.
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
				if err := c.Repo.ClaimRun(context.Background(), c.RunID, c.Holder); err != nil {
					cancel()
					return
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
