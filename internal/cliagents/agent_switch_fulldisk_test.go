package cliagents

import "testing"

// TestAgentSessionState_FullDiskNilReceiverIsSafe covers the nil-receiver
// guards on every fullDiskState accessor (seedFullDisk, FullDiskOn,
// SetFullDiskReArm, ApplyFullDisk): a *AgentSessionState with no chat
// workspace configured is a legitimate zero value (see ApplyFullDisk's own
// doc comment), so every one of these must be a safe no-op/false rather
// than panicking on the nil pointer.
func TestAgentSessionState_FullDiskNilReceiverIsSafe(t *testing.T) {
	var s *AgentSessionState

	s.seedFullDisk(true) // must not panic

	if got := s.FullDiskOn(); got != false {
		t.Fatalf("FullDiskOn() on a nil state = %v, want false", got)
	}

	called := false
	s.SetFullDiskReArm(func(bool) { called = true })
	if called {
		t.Fatal("SetFullDiskReArm must not invoke fn on a nil state")
	}

	if got := s.ApplyFullDisk(true); got != false {
		t.Fatalf("ApplyFullDisk() on a nil state = %v, want false", got)
	}
}

// TestAgentSessionState_ForkNilReceiverReturnsFreshState covers Fork's own
// nil-receiver branch: forking a nil state (no session has configured a
// workspace yet) must still return a usable, non-nil AgentSessionState with
// its own fullDiskState, not panic or return nil.
func TestAgentSessionState_ForkNilReceiverReturnsFreshState(t *testing.T) {
	var s *AgentSessionState
	forked := s.Fork()
	if forked == nil {
		t.Fatal("Fork on a nil state returned nil")
	}
	if forked.fullDisk == nil {
		t.Fatal("Fork on a nil state did not seed a fresh fullDiskState")
	}
	// The forked state must be independently usable.
	if got := forked.FullDiskOn(); got != false {
		t.Fatalf("forked.FullDiskOn() = %v, want false", got)
	}
}
