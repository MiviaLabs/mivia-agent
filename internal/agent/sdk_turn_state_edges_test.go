package agent

import (
	"errors"
	"testing"
)

// TestPass1MapEmptyCallIDIsNotStorable pins that the empty call ID is
// refused by every pass1Map entry point rather than colliding on a "" key.
// Two parallel calls that both arrived without an ID would otherwise
// overwrite each other's pass-1 parts and the shaping wrapper would page
// the wrong tool's bytes.
func TestPass1MapEmptyCallIDIsNotStorable(t *testing.T) {
	m := &pass1Map{}
	m.store("", resultParts{cappedBody: "body", totalN: 4})
	m.mu.Lock()
	stored := len(m.parts)
	m.mu.Unlock()
	if stored != 0 {
		t.Fatalf("store with an empty call ID kept %d entries; want 0", stored)
	}
	if parts, ok := m.take("", "body"); ok || parts != (resultParts{}) {
		t.Fatalf("take with an empty call ID returned (%+v, %v); want (zero, false)", parts, ok)
	}
	// A real entry must survive a purge of the empty ID: purge must return
	// before it reaches the map, not delete the "" key of a live map.
	m.store("call-1", resultParts{cappedBody: "body", totalN: 4})
	m.purge("")
	if _, ok := m.take("call-1", "body"); !ok {
		t.Fatal("purge(\"\") disturbed the stored entry for call-1")
	}
}

// TestPass1MapTakeMissesOnEmptyCappedBody pins the cappedBody guard: an
// entry whose pass-1 body is empty carries nothing to re-page, so take
// must report a miss (the caller stays on its single-pass path) AND still
// drop the entry so it cannot accumulate.
func TestPass1MapTakeMissesOnEmptyCappedBody(t *testing.T) {
	m := &pass1Map{}
	m.store("call-1", resultParts{cappedBody: "", refA: "ref-a", totalN: 99})
	parts, ok := m.take("call-1", "")
	if ok {
		t.Fatalf("take returned found=true for an entry with no pass-1 body: %+v", parts)
	}
	if parts != (resultParts{}) {
		t.Fatalf("take returned %+v on the empty-body miss; want the zero resultParts", parts)
	}
	m.mu.Lock()
	left := len(m.parts)
	m.mu.Unlock()
	if left != 0 {
		t.Fatalf("empty-body miss left %d entries in the map; want 0", left)
	}
}

// TestPass1MapTakeOnUnusedMapIsAMiss covers take before any store: the
// parts map is still nil and take must report a miss instead of panicking
// on a nil-map read path.
func TestPass1MapTakeOnUnusedMapIsAMiss(t *testing.T) {
	m := &pass1Map{}
	if parts, ok := m.take("call-1", "body"); ok || parts != (resultParts{}) {
		t.Fatalf("take on an unused map returned (%+v, %v); want (zero, false)", parts, ok)
	}
}

// TestRecordBridgeErrorKeepsFirstAndIgnoresNil pins the two guards in
// recordBridgeError: a nil error never becomes a recorded failure (a
// successful surface rotation must not fail the run), and the FIRST real
// error wins so the operator sees the cause, not the consequence.
func TestRecordBridgeErrorKeepsFirstAndIgnoresNil(t *testing.T) {
	s := newSDKTurnState()
	s.recordBridgeError(nil)
	if got := s.bridgeError(); got != nil {
		t.Fatalf("a nil bridge error was recorded as %v; want no recorded error", got)
	}
	first := errors.New("surface bridge: registry rebuild failed")
	second := errors.New("surface bridge: later consequence")
	s.recordBridgeError(first)
	s.recordBridgeError(second)
	if got := s.bridgeError(); !errors.Is(got, first) {
		t.Fatalf("bridgeError() = %v; want the first error %v", got, first)
	}
	s.recordBridgeError(nil)
	if got := s.bridgeError(); !errors.Is(got, first) {
		t.Fatalf("a later nil cleared the recorded error to %v; want %v", got, first)
	}
}

// TestTurnShapeCounterLeaveAdvancesGatePastFinishedIndex pins that leave
// opens the ordering gate past the index that just finished, even when the
// call shaped nothing. A batch that skips indices (a duplicate plan, a
// denied call) would otherwise strand every later index behind a gate that
// never advances.
func TestTurnShapeCounterLeaveAdvancesGatePastFinishedIndex(t *testing.T) {
	c := newTurnShapeCounter()
	c.enter()
	c.leave(3)
	c.mu.Lock()
	next, inFlight := c.nextIndex, c.inFlight
	c.mu.Unlock()
	if next != 4 {
		t.Fatalf("nextIndex = %d after leave(3); want 4", next)
	}
	if inFlight != 0 {
		t.Fatalf("inFlight = %d after enter/leave; want 0", inFlight)
	}
	// An out-of-order leave for an ALREADY passed index must not rewind the
	// gate: index 4 and later are open and must stay open.
	c.enter()
	c.leave(1)
	c.mu.Lock()
	next = c.nextIndex
	c.mu.Unlock()
	if next != 4 {
		t.Fatalf("nextIndex = %d after a late leave(1); want it to stay 4", next)
	}
	// A negative index (a call with no batch position) must not move the gate.
	c.enter()
	c.leave(-1)
	c.mu.Lock()
	next = c.nextIndex
	c.mu.Unlock()
	if next != 4 {
		t.Fatalf("nextIndex = %d after leave(-1); want it to stay 4", next)
	}
}
