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
	"sync"
	"sync/atomic"
	"time"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
)

// bridgeSteerSignals wires CLI interrupt signals onto one Steer handle.
// Each non-nil signal spawns one goroutine; all exit when runDone closes
// (RunSteerable returns) or ctx cancels, so a long-lived ctx leaks no pollers.
//
// Signal sources:
//   - InterruptCh: when MailboxPendingInterrupt is set, fires Trigger only
//     when an Interrupt-flagged steer is queued; otherwise fires Trigger
//     unconditionally (explicit interrupt).
//   - MailboxPendingInterrupt: strict poller; fires Trigger when an
//     Interrupt-flagged steer is queued.
//   - MailboxPending: watchdog poller (WatchdogInterval > 0); fires Trigger
//     when any message is waiting.
//
// All sources share one SoftInterruptCooldown gate. A positive cooldown caps
// Trigger fires to one per window; zero disables the gate. The shared
// cooldownUntil is intra-RunAgentLoopOnce (local atomic.Int64) and does not
// span multiple SDK turns (accepted semantic gap).
//
// Goroutines call wg.Add(1) before spawn and defer wg.Done(). The caller
// waits on wg after closing runDone so no goroutine outlives the call.
func bridgeSteerSignals(ctx context.Context, runDone <-chan struct{}, opts Options, steer *sdkagentloop.Steer,
	// wg lets the caller confirm every spawned goroutine has fully
	// exited (not just been signaled to). Required across a
	// prompt-too-long retry, which calls this twice in sequence
	// reusing the same opts.InterruptCh()-returned channel: without
	// the wait, a stale goroutine from the first attempt can still be
	// selecting on that channel when the retry's fresh goroutine also
	// is, and can win the race for an interrupt meant for the retry -
	// see docs/development/sdk-backend-field-mapping.md §4.
	wg *sync.WaitGroup) {
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
		wg.Add(1)
		go func() {
			defer wg.Done()
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
		wg.Add(1)
		go func() {
			defer wg.Done()
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
		wg.Add(1)
		go func() {
			defer wg.Done()
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
