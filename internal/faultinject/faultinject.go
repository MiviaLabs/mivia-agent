// Package faultinject provides a deterministic, counter-based fault and hang
// trigger for concurrency and failure-path tests.
//
// Sleep-based tests are nondeterministic: sleeps finish too early (false pass)
// or too late (slow wait). Gate replaces sleeps with a call counter. Gate
// counts every call through a seam. When the call ordinal matches FaultOn or
// HangOn, Gate returns an injected error or blocks on the caller context.
// All other calls pass untouched. The same FaultOn value always faults the
// same call on every run.
//
// A caller holds a *Gate and calls Check at the top of each method:
//
//	func (f *faultStore) Load(ctx context.Context, key string) (Value, error) {
//		if err := f.gate.Check(ctx, "store.Load"); err != nil {
//			return Value{}, err
//		}
//		return f.inner.Load(ctx, key)
//	}
//
// One Gate counts calls across every method wired into it. Callers that need
// independent counters per method hold a separate Gate per method.
package faultinject

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
)

// ErrFault is the error every Gate wraps on its injected fault call. A test
// asserts a failing call with errors.Is(err, faultinject.ErrFault).
var ErrFault = errors.New("faultinject: injected fault")

// Gate is a reusable, race-free fault and hang trigger keyed by a 1-based
// call counter. The zero value is a Gate that never faults or hangs: every
// call passes through. A Gate is safe for concurrent use; the counter is
// atomic, so concurrent calls each see a distinct, strictly increasing
// ordinal and the target ordinal triggers exactly once.
type Gate struct {
	// FaultOn is the 1-based call ordinal that fails. Zero disables
	// fault injection.
	FaultOn int32
	// HangOn is the 1-based call ordinal that blocks until the caller's
	// context is done. Zero disables hang injection.
	HangOn int32

	calls atomic.Int32
}

// Check counts one call through seam and reports how the caller should
// behave. It returns nil when the call should pass through to the wrapped
// implementation. It returns ctx.Err() when this is the HangOn-th call:
// Check blocks until ctx is done before returning, so the caller observes
// the hang synchronously. It returns an error wrapping ErrFault, naming
// seam, when this is the FaultOn-th call. HangOn takes precedence over
// FaultOn when both target the same ordinal, matching the order the SDK's
// FaultStore checks them in.
func (g *Gate) Check(ctx context.Context, seam string) error {
	n := g.calls.Add(1)
	if g.HangOn != 0 && n == g.HangOn {
		<-ctx.Done()
		return ctx.Err()
	}
	if g.FaultOn != 0 && n == g.FaultOn {
		return fmt.Errorf("faultinject: %s: %w", seam, ErrFault)
	}
	return nil
}

// Calls returns the number of calls Check has counted so far. It is safe
// to call concurrently with Check; a caller uses it to assert how many
// calls a scenario reached before it faulted or hung.
func (g *Gate) Calls() int32 {
	return g.calls.Load()
}

// Reset zeroes the call counter so a Gate can be reused across subtests
// without reconstructing it. FaultOn and HangOn are untouched.
func (g *Gate) Reset() {
	g.calls.Store(0)
}

// Block blocks until ctx is done, then returns ctx.Err(). It models a seam
// that never returns on its own, so a test asserts that a caller with a
// deadline or cancellation observes the timeout, not a hang. Use it for a
// seam that must hang on every call; use Gate.HangOn for a seam that must
// hang on one specific call among many that otherwise pass through.
func Block(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
