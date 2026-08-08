package coordinator

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// ErrIdempotencyKeyContended is returned by recoverByIdempotencyKey (and
// surfaced by Spawn after a bounded retry) when the idempotency key is
// mid-creation or mid-recovery by another process: either a live creator is
// between its durable CreateRun and its first durable CreateTask, or another
// process is reclaiming the abandoned run right now. The caller must back off
// and retry until the winner's run is durably visible, so the retry converges
// to dedup onto that run instead of racing a second execution of the keyed work.
var ErrIdempotencyKeyContended = errors.New("idempotency key is being created/recovered by another process; retry")

// abandonedRunGracePeriod is the minimum age of a 'created + zero tasks' run
// before it may be reclaimed as abandoned. A live creator holds a run in that
// exact state between the durable CreateRun and the first durable CreateTask,
// but completes the gap in milliseconds; a run still 'created' with no tasks
// after a full minute has no live creator left to lose.
const abandonedRunGracePeriod = 60 * time.Second

const (
	// contentionRetryInterval is the pause between bounded retries of a
	// contended idempotency key.
	contentionRetryInterval = 100 * time.Millisecond
	// contentionRetryTotal bounds the whole retry window. A live creator or
	// reclaimer's replacement run becomes durably visible within milliseconds,
	// so two seconds of retries converges to dedup with a wide margin while a
	// genuinely stuck key still surfaces the contention error promptly.
	contentionRetryTotal = 2 * time.Second
)

// reclaimAbandonedRun reclaims a run stranded in the abandoned state (status
// 'created', zero tasks, older than abandonedRunGracePeriod) so its idempotency
// key can be re-created and the work actually executed. It reports whether THIS
// process actually deleted the run.
//
// R3-2: a run is reclaimed only when it is PROVABLY abandoned. The claim is
// probed first - ClaimRun with our own holder succeeds only when no other
// holder owns the run. A claimed run is live (or recoverable by its holder) and
// is never deleted. DeleteRun requires no execution claim and the storage
// backend removes any claim row for the run regardless of holder, so holding
// the probe claim through the delete is safe.
//
// R4-1: reclaim is the atomicity boundary for the idempotency key across
// processes. Only the process whose probe claim succeeded AND whose DeleteRun
// succeeded has actually deleted the run, so only it may proceed to create the
// replacement. Everyone else must observe the contention and back off rather
// than racing into their own CreateRun - two creators would both execute the
// keyed work. On ErrClaimHeld, probe error, or delete failure the run is left
// untouched and the caller must treat the key as contended.
func (c *coordinator) reclaimAbandonedRun(runID string) bool {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Probe the claim: a successful ClaimRun proves the run was unclaimed.
	if err := c.repo.ClaimRun(cleanupCtx, runID, c.holderID); err != nil {
		// Claimed by another holder - the run is live or recoverable by its
		// holder, or another process is reclaiming it right now. Never delete
		// a run we cannot prove abandoned. A transient probe failure is
		// equally a no-reclaim: the caller treats the key as contended.
		if !errors.Is(err, ledger.ErrClaimHeld) {
			log.Printf("coordinator: reclaim abandoned run %q: claim probe: %v", runID, err)
		}
		return false
	}
	if err := c.repo.DeleteRun(cleanupCtx, runID); err != nil {
		log.Printf("coordinator: reclaim abandoned run %q: %v", runID, err)
		// Undo the probe claim so a failed reclaim leaves the run as found.
		_ = c.repo.ReleaseRun(cleanupCtx, runID, c.holderID)
		return false
	}
	// DeleteRun clears the claim unconditionally, so the release is a no-op
	// after a successful delete.
	_ = c.repo.ReleaseRun(cleanupCtx, runID, c.holderID)
	return true
}

// watchRecoveredRun monitors a recovered run and resolves the handle once the
// run is terminal. Non-terminal recovered runs produce errRecoveredRunNotResumable.
func (c *coordinator) watchRecoveredRun(h *RunHandle) {
	snap, err := c.repo.GetRun(context.Background(), h.runID)
	result := &RunResult{Snapshot: snap, Err: err}
	if err == nil {
		result.Results = c.resultsFromSnapshots(context.Background(), snap.Tasks)
		if !isTerminalRunStatus(snap.Status) {
			result.Err = errRecoveredRunNotResumable
		}
	}
	h.mu.Lock()
	h.result = result
	h.mu.Unlock()
	close(h.done)
}

// watchJoinedRun waits for the executor that won an atomic admission race.
// It does not execute work or claim the run.
func (c *coordinator) watchJoinedRun(h *RunHandle) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		snap, err := c.repo.GetRun(context.Background(), h.runID)
		if err != nil || isTerminalRunStatus(snap.Status) {
			result := &RunResult{Snapshot: snap, Err: err}
			if err == nil {
				result.Results = c.resultsFromSnapshots(context.Background(), snap.Tasks)
			}
			h.mu.Lock()
			h.result = result
			h.mu.Unlock()
			close(h.done)
			return
		}
		<-ticker.C
	}
}

// recoverIdempotentWithRetry resolves an idempotency key, retrying through the
// R4-1 contention window. When another process is mid-recovery - reclaiming an
// abandoned run, or between its own durable CreateRun and first CreateTask - the
// key reports ErrIdempotencyKeyContended, and the replacement run becomes
// visible within milliseconds. A bounded retry (contentionRetryInterval apart,
// up to contentionRetryTotal, respecting ctx) converges the caller onto the
// winner's run so the keyed work executes exactly once. Only after the budget is
// exhausted does the contention surface to the caller; the caller never falls
// through to create on contention.
func (c *coordinator) recoverIdempotentWithRetry(ctx context.Context, key, fingerprint string) (*RunHandle, bool, error) {
	deadline := time.Now().Add(contentionRetryTotal)
	for {
		h, found, err := c.recoverByIdempotencyKey(ctx, key, fingerprint)
		if !errors.Is(err, ErrIdempotencyKeyContended) {
			return h, found, err
		}
		if time.Now().Add(contentionRetryInterval).After(deadline) {
			return nil, false, err
		}
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-time.After(contentionRetryInterval):
		}
	}
}
