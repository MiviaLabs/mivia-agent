package clichat

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
)

// The multi-step re-wrap copies fields by name, so a field added to the
// producer is silently dropped here unless named too - exactly the sibling
// drift .agents/memories/sibling-implementations-drift.md records. The
// subagent tool events must carry the bodies the root's do.
func TestOnEventForMultiStepCarriesToolBodies(t *testing.T) {
	var got []agent.Event
	on := OnEventForMultiStep(func(e agent.Event) { got = append(got, e) })
	on(agent.Event{Kind: agent.EventToolStart, ToolCallID: "c", Name: "read_file", Input: "prev", InputBody: "prev and the rest"})
	on(agent.Event{Kind: agent.EventToolEnd, ToolCallID: "c", Name: "read_file", Output: "prev", OutputBody: "prev and the rest"})
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[0].Kind != agent.EventSubagentStart || got[0].InputBody != "prev and the rest" {
		t.Fatalf("subagent_start = %+v, want InputBody carried", got[0])
	}
	if got[1].Kind != agent.EventSubagentEnd || got[1].OutputBody != "prev and the rest" {
		t.Fatalf("subagent_end = %+v, want OutputBody carried", got[1])
	}
}
