package conversation

import (
	"testing"
)

// TestReconcileTerminalReleasesTrackedDispatchGroups pins that ending a
// turn without tool.end events releases the dispatch-group registry's
// retained memory.
//
// p.dispatchGroups is deleted in exactly one place - observeAgentGroupEnd,
// reached only from a real ToolEndBody. reconcileTerminal exists precisely
// for the case where that event never arrives (cancel, error, interrupt:
// see its own doc comment), and it settled the rows but left the member
// list behind, retaining it for the lifetime of the session.
//
// It releases the VALUE and not the KEY on purpose. Key presence is also
// the fence observeToolStartInto uses to suppress the agent loop's second
// tool.start for one call; dropping the key reopens that fence and lets a
// late start resurrect the rows this call just settled (see
// TestLateDuplicateStartCannotResurrectSettledGroup, which is the
// regression for exactly that). So the assertion here is bounded
// retention - no member ids held - not an empty map.
func TestReconcileTerminalReleasesTrackedDispatchGroups(t *testing.T) {
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
	for callID, ids := range p.dispatchGroups {
		if len(ids) != 0 {
			t.Errorf("group %q still retains %d member id(s) after the turn ended, want 0", callID, len(ids))
		}
	}
	if !p.isDispatchGroup("call-1") {
		t.Error("the settled call id was forgotten, so a late duplicate start can re-fan it out")
	}

	// A real tool.end still resolves the group and drops the key, and must
	// not disturb the rows this settle already terminated.
	p.observeAgentGroupEnd("call-1", nil, true)
	if p.isDispatchGroup("call-1") {
		t.Error("a real tool.end did not release the tracked group")
	}
	if got := p.activeAgentCount(); got != 0 {
		t.Errorf("activeAgentCount after the real tool.end = %d, want 0", got)
	}
}
