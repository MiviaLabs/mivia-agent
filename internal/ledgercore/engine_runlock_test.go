package ledgercore

import (
	"fmt"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestForgetRunReleasesTheRunLock pins the leak fix.
//
// RunLock mints one mutex per run and nothing ever removed it, so a long-lived
// session process accumulated one forever - including for runs it had already
// deleted, whose projection, delivery sequences, and watermarks were all
// released. Deleting a run is the one moment no operation on it can still be
// in flight, so it is where the lock is released too.
func TestForgetRunReleasesTheRunLock(t *testing.T) {
	e := NewEngine(storage.NewMemory(), false, "holder")

	const runs = 500
	for i := 0; i < runs; i++ {
		_ = e.RunLock(fmt.Sprintf("wfr-%d", i))
	}
	if got := e.RunLockCount(); got != runs {
		t.Fatalf("RunLockCount after %d RunLock calls = %d, want %d", runs, got, runs)
	}

	for i := 0; i < runs; i++ {
		e.ForgetRun(fmt.Sprintf("wfr-%d", i))
	}
	if got := e.RunLockCount(); got != 0 {
		t.Fatalf("RunLockCount after forgetting every run = %d, want 0: the per-run mutexes leak", got)
	}
}

// TestRunLockIsStableWhileHeld keeps ForgetRun from changing the identity
// callers depend on: repeated RunLock calls for one run return the SAME mutex,
// which is what makes it serialize anything at all.
func TestRunLockIsStableWhileHeld(t *testing.T) {
	e := NewEngine(storage.NewMemory(), false, "holder")
	first := e.RunLock("wfr-1")
	if second := e.RunLock("wfr-1"); second != first {
		t.Fatal("RunLock returned a different mutex for the same run")
	}
	e.ForgetRun("wfr-1")
	if again := e.RunLock("wfr-1"); again == first {
		t.Fatal("RunLock returned the forgotten mutex; ForgetRun did not release it")
	}
}
