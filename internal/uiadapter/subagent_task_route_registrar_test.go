// subagent_task_route_registrar_test.go covers the seam that lets a live
// dispatch reach this package's route table: the package-level
// SubagentTaskRouteRegistrar and the one guard NewSubagentThreads applies
// to it.
package uiadapter

import (
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
)

// withRouteRegistrar installs reg for one test and restores whatever was
// there before. The var is package-level process state, so every test that
// touches it must restore it; no test here may run in parallel with
// another that does.
func withRouteRegistrar(t *testing.T, reg func(sink func(coordinator.Coordinator, string, string, string))) {
	t.Helper()
	prev := SubagentTaskRouteRegistrar
	SubagentTaskRouteRegistrar = reg
	t.Cleanup(func() { SubagentTaskRouteRegistrar = prev })
}

// TestNewSubagentThreads_HandsItsRouteSinkToTheRegistrar proves the
// positive control: with a registrar installed, NewSubagentThreads offers
// it a sink, and that sink IS this registry's RegisterTaskRoute - a route
// pushed through it resolves for CancelSubagentTask.
func TestNewSubagentThreads_HandsItsRouteSinkToTheRegistrar(t *testing.T) {
	var mu sync.Mutex
	var sink func(coordinator.Coordinator, string, string, string)
	withRouteRegistrar(t, func(s func(coordinator.Coordinator, string, string, string)) {
		mu.Lock()
		defer mu.Unlock()
		sink = s
	})

	threads := NewSubagentThreads()

	mu.Lock()
	got := sink
	mu.Unlock()
	if got == nil {
		t.Fatal("NewSubagentThreads did not hand a sink to SubagentTaskRouteRegistrar")
	}

	// A nil coordinator is enough to prove the route landed in THIS
	// registry: an unregistered callID is a clean miss (ok=false, nil
	// error), while a registered route with a nil coordinator errors. The
	// two outcomes are distinguishable without a live coordinator.
	got(nil, "call-1", "run-1", "task-1")
	if _, err := threads.CancelSubagentTask("call-1"); err == nil {
		t.Fatal("route pushed through the registrar's sink did not reach this registry")
	}
	if _, err := threads.CancelSubagentTask("call-2"); err != nil {
		t.Fatalf("unrelated callID must stay a clean miss, got: %v", err)
	}
}

// TestNewSubagentThreads_NilRegistrarIsNoop isolates NewSubagentThreads'
// single `SubagentTaskRouteRegistrar != nil` guard. With no registrar - a
// headless one-shot run, or any build that never wires internal/newtui -
// construction must succeed and produce a usable, route-less registry. A
// mutant that flipped the guard to `== nil` would call a nil func and
// panic here.
func TestNewSubagentThreads_NilRegistrarIsNoop(t *testing.T) {
	withRouteRegistrar(t, nil)

	threads := NewSubagentThreads()
	if threads == nil {
		t.Fatal("NewSubagentThreads returned nil with no registrar installed")
	}
	ok, err := threads.CancelSubagentTask("call-1")
	if err != nil {
		t.Fatalf("expected a clean miss with no registrar installed, got: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false with no routes registered")
	}
}
