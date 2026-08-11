package controller

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// durableHeartbeatInterval bounds durable heartbeat writes to AT MOST ONE per
// attempt per interval. The in-memory task-id registry (NoteStepHeartbeat)
// remains the fast liveness path; the ledger write is the durable trail and
// must never outpace the event log with per-tick noise. The evidence-gate
// ticker uses the same interval, so every tick is a fresh durable write.
// Package-level so tests can shorten it.
var durableHeartbeatInterval = 15 * time.Second

// durableHeartbeatTimeout bounds ONE best-effort ledger write so a stalled
// store can never park the controller behind a heartbeat.
const durableHeartbeatTimeout = 5 * time.Second

// durableHeartbeatThrottle tracks the last persisted heartbeat per attempt.
// It has its own mutex: emitProgress runs on the runner's watchdog goroutine
// while Advance holds the controller's c.mu, so the throttle must never touch
// c.mu (a second lock acquisition would deadlock).
type durableHeartbeatThrottle struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func newDurableHeartbeatThrottle() *durableHeartbeatThrottle {
	return &durableHeartbeatThrottle{last: make(map[string]time.Time)}
}

// shouldPersist reports whether a heartbeat at `at` may be written: the first
// heartbeat for an attempt always persists, later ones only when the interval
// has elapsed since the last persisted heartbeat. On a positive answer the
// throttle records `at` so concurrent callers cannot double-write.
func (t *durableHeartbeatThrottle) shouldPersist(attemptID string, at time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.last[attemptID]; ok && at.Sub(last) < durableHeartbeatInterval {
		return false
	}
	t.last[attemptID] = at
	return true
}

// attemptIDFor derives the ledger attempt ID for a (step, attemptNo) pair.
// It MUST agree with newAttempt's minted AttemptID, so the heartbeat emitted
// for a dispatched agent step resolves to the exact attempt the controller
// admitted into the ledger.
func attemptIDFor(stepID string, attemptNo int) string {
	return fmt.Sprintf("wfa-%s-%d", stepID, attemptNo)
}

// persistDurableHeartbeat writes one wf_attempt_heartbeat event for the
// attempt, best-effort and throttled: a ledger write error is logged and never
// fails the step, and at most one write per attempt per durableHeartbeatInterval
// reaches the store. The in-memory task-id registry is untouched.
func (c *LinearController) persistDurableHeartbeat(attemptID string, at time.Time) {
	if c == nil || c.Repo == nil || attemptID == "" || at.IsZero() {
		return
	}
	if !c.heartbeatThrottle.shouldPersist(attemptID, at) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), durableHeartbeatTimeout)
	defer cancel()
	ctx = workflowledger.ContextWithRunID(ctx, c.RunID)
	ctx = workflowledger.ContextWithClaimHolder(ctx, c.Holder)
	if err := c.Repo.SetStepAttemptHeartbeat(ctx, c.RunID, attemptID, at); err != nil {
		// Best-effort by contract: a ledger write error must never fail the
		// step. The in-memory registry keeps the liveness gate working.
		log.Printf("workflow: run %s attempt %s durable heartbeat failed (continuing): %v", c.RunID, attemptID, err)
	}
}

// startDurableHeartbeatTicker records and persists a durable heartbeat for
// the attempt every durableHeartbeatInterval while the caller's work runs. It
// is used around the SYNCHRONOUS evidence-gate verifier so a long-running
// gate stays observable in the durable ledger. The goroutine exits when the
// context is canceled or the returned stop function runs (stop waits for the
// goroutine, mirroring startClaimHeartbeat).
func (c *LinearController) startDurableHeartbeatTicker(ctx context.Context, attemptID string) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(durableHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.persistDurableHeartbeat(attemptID, time.Now())
			case <-ctx.Done():
				return
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
