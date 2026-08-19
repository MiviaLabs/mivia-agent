package faultinject

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
)

// TestGateZeroValuePassesThrough proves the zero-value Gate never faults or
// hangs, so a caller need not special-case "no injection configured".
func TestGateZeroValuePassesThrough(t *testing.T) {
	var g Gate
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := g.Check(ctx, "seam"); err != nil {
			t.Fatalf("call %d: got %v, want nil", i+1, err)
		}
	}
	if got := g.Calls(); got != 5 {
		t.Fatalf("Calls() = %d, want 5", got)
	}
}

// TestGateFaultOnTargetsExactCall is table-driven over which ordinal
// FaultOn targets among a fixed run of calls, proving the trigger is exact:
// every call before and after the target passes through untouched.
func TestGateFaultOnTargetsExactCall(t *testing.T) {
	const totalCalls = 6
	for target := 1; target <= totalCalls; target++ {
		t.Run(strconv.Itoa(target), func(t *testing.T) {
			g := &Gate{FaultOn: int32(target)}
			ctx := context.Background()
			for i := 1; i <= totalCalls; i++ {
				err := g.Check(ctx, "seam")
				if i == target {
					if !errors.Is(err, ErrFault) {
						t.Fatalf("call %d: err = %v, want ErrFault", i, err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("call %d: err = %v, want nil", i, err)
				}
			}
		})
	}
}

// TestGateFaultOnZeroDisablesFault proves FaultOn == 0 never triggers, even
// across many calls.
func TestGateFaultOnZeroDisablesFault(t *testing.T) {
	g := &Gate{FaultOn: 0}
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		if err := g.Check(ctx, "seam"); err != nil {
			t.Fatalf("call %d: got %v, want nil", i+1, err)
		}
	}
}

// TestGateHangOnBlocksUntilContextDone proves the HangOn-th call blocks
// until ctx is canceled and then returns ctx.Err(), instead of returning
// immediately or hanging forever. The test itself uses no time.Sleep: it
// starts the blocking call on a goroutine, confirms via a channel that the
// call has not returned while ctx is live, then cancels ctx and waits on
// the channel for the call to unblock.
func TestGateHangOnBlocksUntilContextDone(t *testing.T) {
	g := &Gate{HangOn: 1}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		done <- g.Check(ctx, "seam")
	}()
	<-started

	select {
	case err := <-done:
		t.Fatalf("Check returned %v before ctx was canceled, want it to block", err)
	default:
	}

	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Check() after cancel = %v, want context.Canceled", err)
	}
}

// TestGateHangOnZeroDisablesHang proves HangOn == 0 never blocks.
func TestGateHangOnZeroDisablesHang(t *testing.T) {
	g := &Gate{HangOn: 0}
	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- g.Check(ctx, "seam") }()
	if err := <-done; err != nil {
		t.Fatalf("Check() = %v, want nil", err)
	}
}

// TestGateHangOnPrecedesFaultOnSameOrdinal proves that when FaultOn and
// HangOn target the same call, the call hangs (and returns ctx.Err() on
// cancellation) rather than faulting immediately.
func TestGateHangOnPrecedesFaultOnSameOrdinal(t *testing.T) {
	g := &Gate{FaultOn: 1, HangOn: 1}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Check(ctx, "seam") }()

	select {
	case err := <-done:
		t.Fatalf("Check returned %v before ctx was canceled, want it to block (hang precedence)", err)
	default:
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Check() after cancel = %v, want context.Canceled", err)
	}
}

// TestGateConcurrentCallsTriggerExactlyOnce drives many goroutines through
// the same Gate at once and proves the FaultOn ordinal fires on exactly one
// caller, never zero and never more than one, under the race detector. This
// is the property that makes the counter safe as a fault-injection seam in
// concurrent test scenarios: two racing callers cannot both observe the
// injected fault, and none can miss it.
func TestGateConcurrentCallsTriggerExactlyOnce(t *testing.T) {
	const callers = 64
	const target = 37
	g := &Gate{FaultOn: target}
	ctx := context.Background()

	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var faulted int
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := g.Check(ctx, "seam")
			if err == nil {
				return
			}
			if !errors.Is(err, ErrFault) {
				t.Errorf("unexpected error: %v", err)
				return
			}
			mu.Lock()
			faulted++
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	if faulted != 1 {
		t.Fatalf("faulted = %d, want exactly 1", faulted)
	}
	if got := g.Calls(); got != callers {
		t.Fatalf("Calls() = %d, want %d", got, callers)
	}
}

// TestGateResetRestartsCounting proves Reset zeroes the counter so a Gate
// can be reused across subtests, and that FaultOn keeps targeting the same
// ordinal relative to the new count.
func TestGateResetRestartsCounting(t *testing.T) {
	g := &Gate{FaultOn: 2}
	ctx := context.Background()

	if err := g.Check(ctx, "seam"); err != nil {
		t.Fatalf("call 1: got %v, want nil", err)
	}
	if err := g.Check(ctx, "seam"); !errors.Is(err, ErrFault) {
		t.Fatalf("call 2: got %v, want ErrFault", err)
	}

	g.Reset()
	if got := g.Calls(); got != 0 {
		t.Fatalf("Calls() after Reset = %d, want 0", got)
	}
	if err := g.Check(ctx, "seam"); err != nil {
		t.Fatalf("post-reset call 1: got %v, want nil", err)
	}
	if err := g.Check(ctx, "seam"); !errors.Is(err, ErrFault) {
		t.Fatalf("post-reset call 2: got %v, want ErrFault", err)
	}
}

// TestBlockReturnsContextErrOnCancel proves Block waits for ctx and then
// surfaces ctx.Err(), without any time.Sleep in the assertion.
func TestBlockReturnsContextErrOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		done <- Block(ctx)
	}()
	<-started

	select {
	case err := <-done:
		t.Fatalf("Block returned %v before ctx was canceled, want it to block", err)
	default:
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Block() after cancel = %v, want context.Canceled", err)
	}
}
