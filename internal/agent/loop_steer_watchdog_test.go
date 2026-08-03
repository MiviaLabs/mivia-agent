package agent

// Watchdog / cooldown / stale-signal soft-interrupt loop tests (plan 54 §7).
// Shared scripted helpers (steerCompleter, steerStep, gateTool, runLoop) live
// in loop_steer_helpers_test.go; the core interrupt tests live in
// loop_steer_test.go.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestRequestTimeoutStillAborts: a provider DeadlineExceeded with the turn ctx
// alive must keep today's abort semantics — the soft-interrupt path must never
// swallow a timeout (Defect 1 regression).
func TestRequestTimeoutStillAborts(t *testing.T) {
	comp := &steerCompleter{
		steps: []steerStep{{err: context.DeadlineExceeded}},
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}
	_, err := loop.Run(context.Background(), "user", Options{Model: "m", MaxSteps: 5})
	if err == nil {
		t.Fatal("expected request timeout to abort the turn")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want DeadlineExceeded", err)
	}
}

// TestHardCancelStillAborts: canceling the turn ctx must abort exactly as
// today — soft interrupt is an LLM-only signal, never a replacement for
// hard cancel.
func TestHardCancelStillAborts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	comp := &steerCompleter{
		steps:   []steerStep{{blockCtx: true}},
		started: make(chan struct{}, 1),
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}

	go func() {
		<-comp.started
		cancel()
	}()

	_, err := runLoop(t, loop, ctx, "user", Options{Model: "m", MaxSteps: 5})
	if err == nil {
		t.Fatal("expected hard cancel to abort the turn")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
}

// TestWatchdogInterruptsOnlyWhenPending: the watchdog cancels only when
// MailboxPending() is true; an idle child (pending false) is never touched.
func TestWatchdogInterruptsOnlyWhenPending(t *testing.T) {
	t.Run("pending_true_interrupts", func(t *testing.T) {
		var pending atomic.Bool
		pending.Store(true)
		stepCalls := 0
		comp := &steerCompleter{
			steps: []steerStep{
				{blockCtx: true}, // canceled by the watchdog
				{resp: provider.Response{Content: "done", FinishReason: "stop"}},
			},
		}
		loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}

		text, err := runLoop(t, loop, context.Background(), "user", Options{
			Model:            "m",
			MaxSteps:         10,
			WatchdogInterval: 20 * time.Millisecond,
			MailboxPending:   func() bool { return pending.Load() },
			BeforeStep: func() []provider.Message {
				stepCalls++
				if stepCalls == 1 {
					return nil // keep pending true so the watchdog fires on call 1
				}
				pending.Store(false) // drained at the next boundary
				return nil
			},
		})
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
		if text != "done" {
			t.Fatalf("text=%q, want done", text)
		}
		if len(comp.canceled) != 1 || comp.canceled[0] != 0 {
			t.Fatalf("canceled calls=%v, want [0] (watchdog interrupted call 1 only)", comp.canceled)
		}
	})

	t.Run("pending_false_untouched", func(t *testing.T) {
		firstPoll := make(chan struct{}, 1)
		release := make(chan struct{})
		comp := &steerCompleter{steps: []steerStep{{gate: release}}}
		loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}

		go func() {
			// Deterministic: the watcher's first watchdog tick calls
			// MailboxPending, which signals firstPoll; then let the call
			// complete. time.After is a safety bound, not the sync.
			select {
			case <-firstPoll:
			case <-time.After(2 * time.Second):
				return // runLoop's timeout fails the test
			}
			close(release)
		}()

		text, err := runLoop(t, loop, context.Background(), "user", Options{
			Model:            "m",
			MaxSteps:         5,
			WatchdogInterval: 20 * time.Millisecond,
			MailboxPending: func() bool {
				select {
				case firstPoll <- struct{}{}:
				default:
				}
				return false
			},
		})
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
		if text != "done" {
			t.Fatalf("text=%q, want done", text)
		}
		if len(comp.canceled) != 0 {
			t.Fatalf("watchdog canceled an idle call: %v", comp.canceled)
		}
	})
}

// TestSoftInterruptCooldownCapsFlood: N urgent signals inside one cooldown
// window cause at most one soft interrupt; the next LLM call completes
// uncanceled (no starvation).
func TestSoftInterruptCooldownCapsFlood(t *testing.T) {
	interrupt := make(chan struct{}, 8)
	var pending atomic.Bool
	pending.Store(true) // stays true: the cooldown, not an empty queue, protects call 2
	release := make(chan struct{})
	stepCalls := 0

	comp := &steerCompleter{
		steps: []steerStep{
			{blockCtx: true}, // call 1: softly interrupted
			{gate: release},  // call 2: must NOT be canceled within the cooldown
		},
		started: make(chan struct{}, 8),
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}

	go func() {
		<-comp.started // call 1 in flight
		for i := 0; i < 3; i++ {
			interrupt <- struct{}{}
		}
		<-comp.started // call 2 in flight
		close(release) // release promptly: call 2 finishes inside the cooldown window
	}()

	text, err := runLoop(t, loop, context.Background(), "user", Options{
		Model:                   "m",
		MaxSteps:                10,
		InterruptCh:             func() <-chan struct{} { return interrupt },
		MailboxPendingInterrupt: func() bool { return pending.Load() },
		SoftInterruptCooldown:   100 * time.Millisecond,
		BeforeStep: func() []provider.Message {
			stepCalls++
			if stepCalls == 1 {
				return nil
			}
			return []provider.Message{{Role: provider.RoleUser, Content: FrameParentMessage("steer body")}}
		},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if text != "done" {
		t.Fatalf("text=%q, want done", text)
	}
	if len(comp.canceled) != 1 || comp.canceled[0] != 0 {
		t.Fatalf("canceled calls=%v, want [0] (at most one soft interrupt within the cooldown)", comp.canceled)
	}
}

// TestStaleSignalNoCancelWithoutInterruptPending: a signal left in the channel
// after the interrupt steer drained must never cancel the next LLM call — even
// when a NON-interrupt message is queued. The signal branch gates on
// MailboxPendingInterrupt (false here), NOT on the len-based MailboxPending
// (true here, to prove the distinction): the old gate would have canceled.
func TestStaleSignalNoCancelWithoutInterruptPending(t *testing.T) {
	interrupt := make(chan struct{}, 1)
	interrupt <- struct{}{} // stale signal; no Interrupt-flagged steer is queued
	gatePolled := make(chan struct{}, 1)
	release := make(chan struct{})
	comp := &steerCompleter{steps: []steerStep{{gate: release}}}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}

	go func() {
		// Deterministic: wait until the watcher has consulted the
		// interrupt-pending gate with the stale signal present — then release.
		select {
		case <-gatePolled:
		case <-time.After(2 * time.Second):
			return // runLoop's timeout fails the test
		}
		close(release)
	}()

	text, err := runLoop(t, loop, context.Background(), "user", Options{
		Model:       "m",
		MaxSteps:    5,
		InterruptCh: func() <-chan struct{} { return interrupt },
		MailboxPendingInterrupt: func() bool {
			select {
			case gatePolled <- struct{}{}:
			default:
			}
			return false
		},
		MailboxPending: func() bool { return true }, // a plain steer is queued
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if text != "done" {
		t.Fatalf("text=%q, want done", text)
	}
	if len(comp.canceled) != 0 {
		t.Fatalf("stale signal canceled a call: %v", comp.canceled)
	}
}

// TestStaleSignalWithPendingNonInterruptDoesNotCancel: a stale interrupt signal
// with a plain (non-interrupt) steer queued must not cancel the LLM call — the
// signal branch gates on MailboxPendingInterrupt (false), while MailboxPending
// (true) proves the len-based gate alone cannot trigger the signal path. The
// completer finishes normally on its release channel.
func TestStaleSignalWithPendingNonInterruptDoesNotCancel(t *testing.T) {
	interrupt := make(chan struct{}, 1)
	interrupt <- struct{}{} // stale interrupt signal; only a plain steer is queued
	gatePolled := make(chan struct{}, 1)
	release := make(chan struct{})
	comp := &steerCompleter{steps: []steerStep{{gate: release}}}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}

	go func() {
		select {
		case <-gatePolled:
		case <-time.After(2 * time.Second):
			return // runLoop's timeout fails the test
		}
		close(release) // let the completer finish normally
	}()

	text, err := runLoop(t, loop, context.Background(), "user", Options{
		Model:       "m",
		MaxSteps:    5,
		InterruptCh: func() <-chan struct{} { return interrupt },
		MailboxPendingInterrupt: func() bool {
			select {
			case gatePolled <- struct{}{}:
			default:
			}
			return false
		},
		MailboxPending: func() bool { return true }, // a plain steer is queued
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if text != "done" {
		t.Fatalf("text=%q, want done", text)
	}
	if len(comp.canceled) != 0 {
		t.Fatalf("stale interrupt signal with a pending non-interrupt steer canceled the call: %v", comp.canceled)
	}
}

// TestWatchdogZeroDisabled: WatchdogInterval 0 disables the watchdog — a call
// that blocks is not canceled even with MailboxPending true.
func TestWatchdogZeroDisabled(t *testing.T) {
	var pending atomic.Bool
	pending.Store(true)
	release := make(chan struct{})
	comp := &steerCompleter{
		steps:   []steerStep{{gate: release}},
		started: make(chan struct{}, 1),
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}

	go func() {
		<-comp.started
		// Give a wrongly-enabled watchdog a bounded chance to fire before
		// release (time.After, not time.Sleep).
		select {
		case <-time.After(40 * time.Millisecond):
		}
		close(release)
	}()

	text, err := runLoop(t, loop, context.Background(), "user", Options{
		Model:            "m",
		MaxSteps:         5,
		WatchdogInterval: 0,
		MailboxPending:   func() bool { return pending.Load() },
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if text != "done" {
		t.Fatalf("text=%q, want done", text)
	}
	if len(comp.canceled) != 0 {
		t.Fatalf("watchdog with zero interval canceled the call: %v", comp.canceled)
	}
}
