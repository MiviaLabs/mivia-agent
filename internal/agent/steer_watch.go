package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// errSteerInterrupt marks an LLM call that a soft interrupt (plan 54) canceled
// for a pending steer. runStep soft-continues ONLY on this sentinel; every
// other error - provider timeout (DeadlineExceeded), unmarked Canceled, any
// provider error - keeps today's propagate-and-abort semantics.
var errSteerInterrupt = errors.New("agent: steer interrupt")

// steerCooldownOK reports whether a soft interrupt is allowed now. A zero
// cooldown disables the gate entirely; the production 5s default lives in
// the subagents wiring, not here.
func (l *Loop) steerCooldownOK(opts Options, now time.Time) bool {
	if opts.SoftInterruptCooldown <= 0 {
		return true
	}
	return now.Sub(time.Unix(0, l.softInterruptAt.Load())) >= opts.SoftInterruptCooldown
}

// steerScope holds the cancel func of the LLM call currently in flight inside
// one requestStep. requestStep arms it with the first attempt's llmCancel and
// re-arms it with the prompt-too-long retry's retryCancel, so the single
// per-step watcher always cancels the LIVE call — the retry included. The
// mutex guards the swap: set runs on the loop goroutine while fire runs on
// the watcher goroutine.
type steerScope struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

// set re-arms the scope to cancel the given LLM-call context. Nil-safe.
func (s *steerScope) set(cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel = cancel
}

// fire cancels the currently armed LLM call. It never touches the turn ctx, so
// a steer can cancel only the in-flight provider call, never a tool batch.
func (s *steerScope) fire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

// steerWatcher is the per-step soft-interrupt watcher (plan 54 §4.3). It is
// spawned once per requestStep and stays alive for the whole step, covering
// the first LLM attempt AND the prompt-too-long compaction retry. It resolves
// the interrupt channel once at start and watches it plus the optional
// watchdog ticker. It cancels ONLY the LLM call currently armed in scope —
// never the turn ctx, so a steer arriving during a tool batch cannot cancel a
// tool. The two gates are distinct: the SIGNAL branch fires only when an
// Interrupt-flagged steer is actually queued (MailboxPendingInterrupt), so a
// stale signal paired with a later non-interrupt message is never a cancel;
// the WATCHDOG branch fires when ANY message is pending (MailboxPending),
// bounding non-urgent steer latency. Both require the SoftInterruptCooldown to
// allow and the turn ctx to be alive. It exits on stepDone (requestStep's
// deferred close) or ctx.Done(); requestStep guarantees it never leaks.
func (l *Loop) steerWatcher(ctx context.Context, scope *steerScope, opts Options, steerFired *atomic.Bool, stepDone <-chan struct{}) {
	// Resolve the interrupt channel once per step. Nil InterruptCh disables
	// the signal branch (a nil channel never fires in select).
	var ch <-chan struct{}
	if opts.InterruptCh != nil {
		ch = opts.InterruptCh()
	}
	// Watchdog branch: WatchdogInterval 0 disables it (nil channel).
	var tick <-chan time.Time
	if opts.WatchdogInterval > 0 {
		ticker := time.NewTicker(opts.WatchdogInterval)
		defer ticker.Stop()
		tick = ticker.C
	}
	// fire records the soft interrupt (atomically: later calls' watchers read
	// softInterruptAt, requestStep reads steerFired) and cancels the currently
	// armed LLM call via scope — the first attempt's llmCtx, or the retry's
	// retryCtx once requestStep re-arms it. It never cancels the turn ctx.
	fire := func(now time.Time) {
		steerFired.Store(true)
		l.softInterruptAt.Store(now.UnixNano())
		scope.fire()
	}
	for {
		select {
		case <-ch:
			if now := time.Now(); l.steerCooldownOK(opts, now) && opts.MailboxPendingInterrupt != nil && opts.MailboxPendingInterrupt() && ctx.Err() == nil {
				fire(now)
			}
		case <-tick:
			if now := time.Now(); l.steerCooldownOK(opts, now) && opts.MailboxPending != nil && opts.MailboxPending() && ctx.Err() == nil {
				fire(now)
			}
		case <-stepDone:
			return
		case <-ctx.Done():
			return
		}
	}
}

// steerInterruptOutcome resolves runStep's soft-interrupt branch. It returns
// (stepOutcome{text: partial}, true) when the requestStep error is a soft
// steer interrupt (plan 54) with the turn ctx still alive — the partial the
// interrupted call already streamed is captured BEFORE recordInterruptedPartial
// (which appends to Messages but returns nothing) and carried as the outcome
// text so it survives into lastText even when the post-steer reply is tool-only
// or empty (Defect 5). It returns (stepOutcome{}, false) for every other
// error, INCLUDING a hard cancel racing the steer fire (the sentinel with the
// turn ctx already canceled): the caller must surface the real cause
// (context.Canceled / DeadlineExceeded), never the sentinel, which would
// misclassify a cancel as a soft interrupt / failure.
func (l *Loop) steerInterruptOutcome(err error, live *teeWriter, ctx context.Context) (stepOutcome, bool) {
	if !errors.Is(err, errSteerInterrupt) {
		return stepOutcome{}, false
	}
	// A hard cancel racing the steer fire must surface the real cause
	// (context.Canceled / DeadlineExceeded), not the sentinel: the turn is
	// over, and returning the sentinel would misclassify a cancel as a soft
	// interrupt / failure.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return stepOutcome{}, false
	}
	// Soft interrupt (plan 54): a steer canceled only the LLM call. Preserve
	// the streamed partial in history and soft-continue so the next step's
	// BeforeStep drains the steer.
	partial := ""
	if live != nil {
		partial = live.String()
	}
	l.recordInterruptedPartial(live)
	return stepOutcome{text: partial}, true
}
