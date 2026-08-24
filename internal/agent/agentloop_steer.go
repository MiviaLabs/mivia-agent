// Package agent - SDK agent-loop steer bridge.
//
// bridgeSteerSignals wires the CLI's InterruptCh / MailboxPending /
// MailboxPendingInterrupt onto the SDK's Steer. Each non-nil signal
// spawns one goroutine; all goroutines exit when runDone closes
// (RunSteerable returns) OR ctx cancels, whichever comes first. A
// long-lived caller ctx therefore leaks no pollers.
//
// Extracted from agentloop_adapter.go to keep that file under the
// .mivia/policy/go-structure.json LOC budget.

package agent

import (
	"context"
	"sync/atomic"
	"time"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
)

// bridgeSteerSignals wires the CLI's interrupt signals onto one Steer
// handle. Each non-nil signal spawns one goroutine; all exit when
// runDone closes (i.e. RunSteerable returns) OR ctx is canceled,
// whichever comes first, so a long-lived caller ctx leaks no pollers.
//
// The three signal sources model the CLI's three cancellation layers:
//   - InterruptCh resolves once per fire and, when MailboxPendingInterrupt
//     is also set, fires Trigger ONLY when an Interrupt-flagged steer
//     is queued. When MailboxPendingInterrupt is nil, InterruptCh fires
//     Trigger unconditionally - a bare InterruptCh with no mailbox gate
//     is an explicit interrupt (the legacy "fire on close" semantics).
//   - MailboxPendingInterrupt is the strict signal-branch poller:
//     the predicate reports whether an Interrupt-flagged steer is
//     queued. Trigger fires the moment it returns true.
//   - MailboxPending is the loose watchdog poller (gated on
//     WatchdogInterval > 0): the predicate reports whether ANY message
//     is waiting, so a stale signal after a drain can never cancel a call.
//
// All three sites share one SoftInterruptCooldown gate. A positive
// cooldown caps Trigger fires to one per window; a zero cooldown
// disables the gate, mirroring the legacy steerCooldownOK semantics.
// The shared cooldownUntil is intra-RunAgentLoopOnce only (a local
// atomic.Int64 here), so the gate does not span multiple SDK turns;
// the legacy's cross-call gate (Loop.softInterruptAt) is not portable
// to the SDK's per-RunSteerable Steer value and is recorded as an
// accepted semantic gap.
func bridgeSteerSignals(ctx context.Context, runDone <-chan struct{}, opts Options, steer *sdkagentloop.Steer) {
	var cooldownUntil atomic.Int64
	cooldownOK := func() bool {
		if opts.SoftInterruptCooldown <= 0 {
			return true
		}
		return time.Now().UnixNano() >= cooldownUntil.Load()
	}
	noteFire := func() {
		if opts.SoftInterruptCooldown <= 0 {
			return
		}
		cooldownUntil.Store(time.Now().UnixNano() + int64(opts.SoftInterruptCooldown))
	}
	fireSteer := func() {
		if !cooldownOK() {
			return
		}
		noteFire()
		steer.Trigger()
	}
	if ch := opts.InterruptCh; ch != nil {
		interrupt := opts.MailboxPendingInterrupt
		go func() {
			// Resolve the channel once, OUTSIDE the select: ch() may
			// return nil (a misconfigured InterruptCh), and `<-nil`
			// blocks forever. A nil return is treated as "no signal"
			// and the goroutine exits on runDone/ctx instead of
			// parking forever.
			ch := ch()
			if ch == nil {
				select {
				case <-runDone:
				case <-ctx.Done():
				}
				return
			}
			select {
			case <-ch:
				if interrupt == nil {
					fireSteer()
					return
				}
				if interrupt() {
					fireSteer()
				}
				// An Interrupt-flagged steer not yet queued: drain
				// the stale signal without firing. The strict
				// watchdog poller below will catch the
				// queued-and-flagged case.
			case <-runDone:
			case <-ctx.Done():
			}
		}()
	}
	pollInterval := opts.WatchdogInterval
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	if interrupt := opts.MailboxPendingInterrupt; interrupt != nil {
		go func() {
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()
			for {
				select {
				case <-runDone:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					// A trigger fired when no Completer.Chat call is
					// in flight sets the SDK's per-RunSteerable
					// trigger flag; the next arm observes it and
					// immediately cancels the next chat, the bridge
					// fires again, the next chat cancels, and the
					// run never makes progress. Guard on
					// HasActiveCall so bridge triggers fire only
					// when there is a live chat to cancel. A
					// pre-chat trigger that the bridge intentionally
					// means to "save for the next arm" is the
					// legacy semantics; the bridge mirrors the
					// legacy per-call watcher and must observe the
					// same gate.
					if interrupt() && steer.HasActiveCall() {
						fireSteer()
					}
				}
			}
		}()
	}
	// The loose watchdog poller mirrors the legacy SteerWatchdog: 0
	// disables it, so a non-urgent steer to a child configured without
	// a watchdog waits for the step boundary instead of canceling the
	// in-flight call (TestSteerLandsAtStepBoundaryUnchanged). A
	// positive interval keeps the legacy steer-latency bound.
	if pending := opts.MailboxPending; pending != nil && opts.WatchdogInterval > 0 {
		go func() {
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()
			for {
				select {
				case <-runDone:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					if pending() {
						fireSteer()
					}
				}
			}
		}()
	}
}
