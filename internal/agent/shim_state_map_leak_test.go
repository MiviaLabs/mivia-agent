package agent

import (
	"strconv"
	"testing"
)

// TestPass1Map_NoLeakOnBodyMismatch pins the pass1Map contract: an
// intermediate shim that rewrites the body (e.g. ref-only notice)
// must NOT leave the pass-1 entry orphaned in the map. take's body
// check returns false on a mismatch; the entry must still be removed
// so the map does not grow monotonically across a long session.
func TestPass1Map_NoLeakOnBodyMismatch(t *testing.T) {
	m := &pass1Map{}
	m.store("call-1", resultParts{cappedBody: "pass-1-body", totalN: 12})
	// Caller (turnShapeWrapper) reads a DIFFERENT body, signalling
	// an intermediate shim rewrote it.
	parts, found := m.take("call-1", "rewritten-by-ref-only")
	if found {
		t.Fatalf("take must NOT return found=true on body mismatch; got %+v", parts)
	}
	m.mu.Lock()
	_, stillThere := m.parts["call-1"]
	m.mu.Unlock()
	if stillThere {
		t.Fatalf("take left entry in map on body mismatch; pass1Map leaks across iterations")
	}
	// Explicit purge is also a no-op on a missing key.
	m.purge("call-1")
	m.mu.Lock()
	got := len(m.parts)
	m.mu.Unlock()
	if got != 0 {
		t.Fatalf("after purge map has %d entries; want 0", got)
	}
}

// TestPass1Map_MapShrinksUnderRefOnlyRewrite is a focused integration
// regression for the leak: a turn-shape wrapper followed by a ref-only
// notice shim (ref-only wraps after dispatch, so it rewrites the body
// the wrapper observes). Repeated calls must NOT grow m.parts.
func TestPass1Map_MapShrinksUnderRefOnlyRewrite(t *testing.T) {
	turn := newSDKTurnState()
	for i := 0; i < 50; i++ {
		id := "call-" + strconv.Itoa(i)
		// Simulate the dispatcher shim storing pass-1 with id i.
		turn.pass1.store(id, resultParts{cappedBody: "x", totalN: 1, effectiveCap: 8 << 10})
		// Simulate the wrapper observing a DIFFERENT body (the
		// ref-only shim rewrote it before the wrapper ran).
		_, _ = turn.pass1.take(id, "rewritten")
	}
	turn.pass1.mu.Lock()
	got := len(turn.pass1.parts)
	turn.pass1.mu.Unlock()
	if got != 0 {
		t.Fatalf("pass1Map.parts grew to %d after 50 iterations; expected 0", got)
	}
}
