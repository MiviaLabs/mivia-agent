package conversation

import (
	"testing"
)

// TestReconcileTerminalClearsTrackedDispatchGroups pins that ending a turn
// without tool.end events also releases the dispatch-group registry.
//
// p.dispatchGroups is deleted in exactly one place - observeAgentGroupEnd,
// reached only from a real ToolEndBody. reconcileTerminal exists precisely
// for the case where that event never arrives (cancel, error, interrupt:
// see its own doc comment), and it settled the rows but left the group
// entry behind. Every interrupted dispatch therefore leaked one entry for
// the lifetime of the session, and a late tool.end draining a superseded
// stream could still match that stale entry and flip a settled row back to
// a non-terminal status, holding activeAgentCount() above zero - which is
// what keeps the spinner clock running with nothing left to animate.
func TestReconcileTerminalClearsTrackedDispatchGroups(t *testing.T) {
	var p panel
	p.observeAgentGroupStart("call-1", []string{"call-1:task-a", "call-1:task-b"}, nil)

	if got := p.activeAgentCount(); got != 2 {
		t.Fatalf("activeAgentCount after fan-out = %d, want 2", got)
	}
	if !p.isDispatchGroup("call-1") {
		t.Fatal("group was not tracked after fan-out")
	}

	// The turn ends with no ToolEndBody for the dispatch call at all.
	p.reconcileTerminal("interrupted")

	if got := p.activeAgentCount(); got != 0 {
		t.Errorf("activeAgentCount after reconcile = %d, want 0", got)
	}
	if len(p.dispatchGroups) != 0 {
		t.Errorf("dispatchGroups retained %d entry/entries after the turn ended, want 0", len(p.dispatchGroups))
	}
	if p.isDispatchGroup("call-1") {
		t.Error("a settled group is still tracked, so a late tool.end can resurrect its rows")
	}
}
