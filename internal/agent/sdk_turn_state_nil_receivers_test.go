package agent

import "testing"

// TestSDKTurnStateNilReceiverRecordersAreNoOps pins the nil-receiver
// guards on the turn-state recorders: the dispatcher shim can hold a nil
// turn state on paths that never adopted one, and both recorders must
// no-op instead of panicking.
func TestSDKTurnStateNilReceiverRecordersAreNoOps(t *testing.T) {
	var s *sdkTurnState
	panicked := func() (p any) {
		defer func() { p = recover() }()
		s.recordChangedSurface("write")
		s.recordToolResultEvidence("write", 42)
		return nil
	}()
	if panicked != nil {
		t.Fatalf("nil-receiver recorders panicked: %v, want safe no-op", panicked)
	}
}
