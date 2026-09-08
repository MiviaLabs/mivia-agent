package conversation

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestLateDuplicateStartCannotResurrectSettledGroup pins the fence that
// keeps a settled dispatch group settled.
//
// The agent loop emits TWO tool.start events per call - "queued" (Args
// populated) then "running" (no Args). observeToolStartInto suppresses the
// second one with `if p.isDispatchGroup(b.ToolCallID) { return }`, and that
// map entry is the ONLY thing standing between a duplicate start and a
// re-fan-out.
//
// reconcileTerminal settles the rows when a turn ends without tool.end
// events. A superseded stream is still drained afterwards by design (see
// handleTurnEventFrom's stale-source re-arm), so the second start can
// legitimately arrive AFTER the settle. If the settle also erased the
// dedup memory, that late start re-fans the call out and
// observeAgentStart resets each terminal row to "running" with a fresh
// clock - phantom running subagents that no turn will ever end, holding
// activeAgentCount above zero and the spinner clock armed forever.
func TestLateDuplicateStartCannotResurrectSettledGroup(t *testing.T) {
	var p panel
	start := uievent.ToolStartBody{
		ToolCallID: "call-1",
		Name:       "dispatch_tasks",
		Args: map[string]any{
			"tasks": []any{
				map[string]any{"id": "task-a", "prompt": "a"},
				map[string]any{"id": "task-b", "prompt": "b"},
			},
		},
	}

	// The "queued" start fans the call out into per-task rows.
	observeToolStartInto(&p, nil, start)
	if got := p.activeAgentCount(); got != 2 {
		t.Fatalf("activeAgentCount after fan-out = %d, want 2", got)
	}

	// The turn is interrupted with no tool.end for the dispatch call.
	p.reconcileTerminal("interrupted")
	if got := p.activeAgentCount(); got != 0 {
		t.Fatalf("activeAgentCount after reconcile = %d, want 0", got)
	}

	// The superseded stream drains its buffered "running" start for the
	// SAME call id. It must be inert.
	observeToolStartInto(&p, nil, uievent.ToolStartBody{
		ToolCallID: "call-1",
		Name:       "dispatch_tasks",
	})

	if got := p.activeAgentCount(); got != 0 {
		t.Errorf("a late duplicate start resurrected %d settled row(s); the spinner clock never stops", got)
	}
	for _, a := range p.agents {
		if !isTerminalStatus(a.Status) {
			t.Errorf("row %q is %q after a late duplicate start, want a terminal status", a.ID, a.Status)
		}
	}
	if n := len(p.agents); n != 2 {
		t.Errorf("panel holds %d rows, want the original 2 (a stray row was appended)", n)
	}
}
