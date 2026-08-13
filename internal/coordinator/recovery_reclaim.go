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
// claim is acquired lease-aware: a free claim is taken directly, and a held
// claim is taken over only when its lease has expired. A live holder refreshes
// its claim on its heartbeat, so an expired claim belongs to a creator that
// crashed between ClaimRun and the first CreateTask (DC-4) and never
// heartbeated - reclaiming it restores the key instead of refusing forever. A
// fresh, heartbeated live claim is refused so reclaim never clears a live
// owner and deletes live work.
//
// R4-1: reclaim is the atomicity boundary for the idempotency key across
// processes. Only the process whose probe claim succeeded AND whose DeleteRun
// succeeded has actually deleted the run, so only it may proceed to create the
// replacement. Everyone else must observe the contention and back off rather
// than racing into their own CreateRun - two creators would both execute the
// keyed work. On probe error or delete failure the run is left untouched and
// the caller must treat the key as contended.
func (c *coordinator) reclaimAbandonedRun(runID string) bool {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Acquire the claim lease-aware (probe, then takeover only when the
	// holder's lease expired): a successful claim proves the run was unclaimed
	// or that the previous holder is dead.
	if err := c.claimRun(cleanupCtx, runID); err != nil {
		if errors.Is(err, ledger.ErrClaimHeld) {
			log.Printf("coordinator: reclaim abandoned run %q: claim is held by a live owner; refusing", runID)
		} else {
			// A transient probe failure is a no-reclaim: the caller treats
			// the key as contended.
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

// watchedRunReclaimInterval is how often watchJoinedRun re-probes ownership
// of the run's execution claim while it waits. It is independent of the poll
// ticker below: reclaiming only ever succeeds once the held claim's lease has
// actually expired (defaultRunClaimLease, or a shorter c.claimLease), so
// probing faster than that would just be wasted repo calls.
const watchedRunReclaimInterval = 500 * time.Millisecond

// watchJoinedRun waits for the executor that won an atomic admission race. It
// does not execute work or claim the run itself UNLESS the winner's claim
// goes stale (its lease expires with no live holder refreshing it, i.e. the
// winner crashed): a stale claim means no other executor will ever move the
// run to a terminal status, so an unconditional wait-for-terminal here would
// hang forever. watchJoinedRun instead re-probes the claim on
// watchedRunReclaimInterval and, once it can take over an expired claim,
// resumes the run itself on this same handle via resumeExecutionOnHandle.
func (c *coordinator) watchJoinedRun(h *RunHandle) {
	pollTicker := time.NewTicker(25 * time.Millisecond)
	defer pollTicker.Stop()
	reclaimTicker := time.NewTicker(watchedRunReclaimInterval)
	defer reclaimTicker.Stop()
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
		select {
		case <-pollTicker.C:
		case <-reclaimTicker.C:
			ctx := context.Background()
			if claimErr := c.claimRun(ctx, h.runID); claimErr != nil {
				// Still held by a live owner (or a transient repo error): keep
				// watching. Either way this is not this goroutine's terminal
				// state, so it never closes h.done here.
				continue
			}
			failInterrupted := err == nil && snap.Policy.FailInterrupted
			if resumeErr := c.resumeExecutionOnHandle(ctx, h, h.runID, failInterrupted); resumeErr != nil {
				log.Printf("coordinator: watch joined run %q: reclaimed claim but resume failed: %v", h.runID, resumeErr)
				_ = c.repo.ReleaseRun(ctx, h.runID, c.holderID)
				continue
			}
			// Ownership of h now belongs to the executeResumedRun goroutine
			// resumeExecutionOnHandle started: it heartbeats the claim, runs
			// the work, and closes h.done itself.
			return
		}
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
