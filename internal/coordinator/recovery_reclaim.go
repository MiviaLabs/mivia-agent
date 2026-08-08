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
// R3-2: a run is reclaimed only when it is PROVABLY abandoned. The caller-side
// gate (recoverByIdempotencyKey) admits only 'created + zero tasks' runs older
// than the grace period - a state no live creator occupies (the gap between the
// durable CreateRun and the first durable CreateTask is milliseconds). The
// claim is probed first - ClaimRun with our own holder succeeds only when no
// other holder owns the run.
//
// R3-2 amendment (C4, DC-2/DC-4): because the run is provably abandoned, a
// claim row on it is provably stale. A holder can only have acquired the claim
// by crashing between ClaimRun and the first CreateTask (DC-4 - the run is
// 'created' with zero tasks, so no task was ever written), or by being another
// reclaimer in the act of deleting the run. Either way the claim does not
// protect live work, so on ErrClaimHeld the stale claim is cleared
// (ClearRunClaim) and the claim re-probed. If the re-probe succeeds, DeleteRun
// proceeds - its claim-fenced tombstone append is the backstop against a racing
// reclaimer that stole the re-probe, and the storage backend removes the claim
// row unconditionally. If the re-probe fails (another reclaimer won the race),
// the run is left to that winner and we report no-reclaim.
//
// R4-1: reclaim is the atomicity boundary for the idempotency key across
// processes. Only the process whose probe claim succeeded AND whose DeleteRun
// succeeded has actually deleted the run, so only it may proceed to create the
// replacement. Everyone else must observe the contention and back off rather
// than racing into their own CreateRun - two creators would both execute the
// keyed work. On probe error, re-probe failure, or delete failure the run is
// left untouched and the caller must treat the key as contended.
func (c *coordinator) reclaimAbandonedRun(runID string) bool {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Probe the claim: a successful ClaimRun proves the run was unclaimed.
	if err := c.repo.ClaimRun(cleanupCtx, runID, c.holderID); err != nil {
		if !errors.Is(err, ledger.ErrClaimHeld) {
			// A transient probe failure is a no-reclaim: the caller treats
			// the key as contended.
			log.Printf("coordinator: reclaim abandoned run %q: claim probe: %v", runID, err)
			return false
		}
		// The run is provably abandoned ('created', zero tasks, older than the
		// grace period), so its claim row is provably stale - the holder
		// crashed between ClaimRun and the first CreateTask (DC-4), or is
		// another reclaimer about to delete the run. Clear the stale claim and
		// re-probe. DeleteRun's claim fence is the backstop: if another
		// reclaimer wins the re-probe, our DeleteRun is refused, and if we win
		// it, theirs is.
		log.Printf("coordinator: reclaim abandoned run %q: claim held by another holder but run is provably abandoned; clearing stale claim and re-probing", runID)
		if err := c.repo.ClearRunClaim(cleanupCtx, runID); err != nil {
			log.Printf("coordinator: reclaim abandoned run %q: clear stale claim: %v", runID, err)
			return false
		}
		if err := c.repo.ClaimRun(cleanupCtx, runID, c.holderID); err != nil {
			// Another reclaimer won the clear+re-probe race; the run stays
			// with it and the caller treats the key as contended. Never
			// delete a run whose claim we do not hold.
			log.Printf("coordinator: reclaim abandoned run %q: re-probe after clearing stale claim: %v", runID, err)
			return false
		}
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
