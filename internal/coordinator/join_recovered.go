package coordinator

import (
	"context"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// JoinAsRecovered returns a recovered, wait-only handle for an already
// admitted run without resuming it as a local actor and without dispatching
// its handler. A caller that wants to cancel a child it does not itself own
// obtains a handle this way, then calls Cancel on it: the recovered path
// (cancelRecovered) refuses to act on a task whose persisted status looks
// nonterminal with no verifiable live owner, rather than guessing.
func (c *coordinator) JoinAsRecovered(ctx context.Context, req EnsureRunRequest) (*RunHandle, error) {
	if len(req.Tasks) != 1 {
		return nil, fmt.Errorf("join as recovered: want one task")
	}
	var err error
	req, err = c.resolveEnsurePolicy(ctx, req)
	if err != nil {
		return nil, err
	}
	fingerprint, key, err := c.validateEnsureRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	c.spawnMu.Lock()
	defer c.spawnMu.Unlock()
	if h := c.lookupHandle(key); h != nil {
		if h.runID != req.RunID || h.requestFingerprint != fingerprint {
			return nil, ErrIdempotencyConflict
		}
		return h, nil
	}
	run, err := c.repo.GetRunByIdempotencyKey(ctx, key)
	if errors.Is(err, ledger.ErrNotFound) {
		return nil, ledger.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("join as recovered: get idempotent run: %w", err)
	}
	if run.RunID != req.RunID || run.RequestFingerprint != fingerprint || run.Policy != req.Policy {
		return nil, ErrIdempotencyConflict
	}
	tasks, err := c.repo.ListTasks(ctx, run.RunID)
	if err != nil {
		return nil, fmt.Errorf("join as recovered: list tasks: %w", err)
	}
	if len(tasks) != 1 || !sameStoredWork(req.Tasks[0], tasks[0]) {
		return nil, ErrIdempotencyConflict
	}
	h := c.newRunHandle(run.RunID, key, latestAttempts(tasks), fingerprint, true, nonInteractiveRunOpts(req)...)
	go c.watchRecoveredRun(h)
	return h, nil
}
