package agent

import (
	"context"
	"errors"
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

// steerWatcher is the per-call soft-interrupt watcher (plan 54 §4.3). It runs
// only while the LLM call is in flight, resolving the interrupt channel once
// at start and watching it plus the optional watchdog ticker. It cancels ONLY
// llmCtx — never the turn ctx, so a steer arriving during a tool batch cannot
// cancel a tool. The two gates are distinct: the SIGNAL branch fires only when
// an Interrupt-flagged steer is actually queued (MailboxPendingInterrupt), so
// a stale signal paired with a later non-interrupt message is never a cancel;
// the WATCHDOG branch fires when ANY message is pending (MailboxPending),
// bounding non-urgent steer latency. Both require the SoftInterruptCooldown to
// allow and the turn ctx to be alive. It exits on llmCtx.Done() or ctx.Done();
// requestStep's deferred llmCancel() guarantees it never leaks.
func (l *Loop) steerWatcher(ctx context.Context, llmCtx context.Context, llmCancel context.CancelFunc, opts Options, steerFired *atomic.Bool) {
	// Resolve the interrupt channel once per call. Nil InterruptCh disables
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
	// softInterruptAt, requestStep reads steerFired) and cancels only llmCtx.
	fire := func(now time.Time) {
		steerFired.Store(true)
		l.softInterruptAt.Store(now.UnixNano())
		llmCancel()
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
		case <-llmCtx.Done():
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
