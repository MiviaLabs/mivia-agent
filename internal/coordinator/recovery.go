package coordinator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

func (c *Coordinator) recoverByIdempotencyKey(ctx context.Context, key string) (*RunHandle, bool, error) {
	snap, err := c.repo.GetRunByIdempotencyKey(ctx, key)
	if errors.Is(err, ledger.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("recover idempotent run: %w", err)
	}
	h := c.newRunHandle(snap.RunID, key, nil)
	go c.watchRecoveredRun(h)
	return h, true, nil
}

func (c *Coordinator) watchRecoveredRun(h *RunHandle) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		snap, err := c.repo.GetRun(context.Background(), h.runID)
		if err != nil {
			h.mu.Lock()
			h.result = &RunResult{Snapshot: snap, Err: err}
			h.mu.Unlock()
			close(h.done)
			return
		}
		if isTerminalRunStatus(snap.Status) {
			h.mu.Lock()
			h.result = &RunResult{Snapshot: snap}
			h.mu.Unlock()
			close(h.done)
			return
		}
		<-ticker.C
	}
}

func isTerminalRunStatus(status ledger.RunStatus) bool {
	switch status {
	case ledger.RunStatusCompleted, ledger.RunStatusFailed, ledger.RunStatusCanceled:
		return true
	default:
		return false
	}
}
