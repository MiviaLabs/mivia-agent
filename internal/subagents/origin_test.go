package subagents

import (
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

func runtimeRequestForOriginTest() runtime.Request {
	return runtime.Request{
		ID:    "task-42",
		Name:  "audit",
		Depth: 0,
		Input: json.RawMessage(`"review the patch"`),
	}
}

// Attribution: every event a subagent loop emits must carry the identity of
// the run that produced it, or the parent TUI cannot tell three parallel
// agents apart.

func TestStampEventOriginAttributes(t *testing.T) {
	var got agent.Event
	sink := func(e agent.Event) { got = e }
	origin := agent.EventOrigin{TaskID: "task-1", Agent: "audit", Depth: 1}

	stamped := StampEventOrigin(sink, origin)
	stamped(agent.Event{Kind: agent.EventToolStart, Name: "grep"})

	if got.Origin != origin {
		t.Fatalf("origin not stamped: %+v", got.Origin)
	}
	if got.Name != "grep" || got.Kind != agent.EventToolStart {
		t.Fatalf("event mutated beyond origin: %+v", got)
	}
}

func TestStampEventOriginPreservesDeeperOrigin(t *testing.T) {
	// The stamp closest to the producing loop wins: an event that already
	// carries an origin (deeper nesting) must not be overwritten.
	deeper := agent.EventOrigin{TaskID: "inner", Agent: "nested", Depth: 2}
	var got agent.Event
	stamped := StampEventOrigin(func(e agent.Event) { got = e },
		agent.EventOrigin{TaskID: "outer", Agent: "parent", Depth: 1})

	stamped(agent.Event{Kind: agent.EventToolEnd, Origin: deeper})

	if got.Origin != deeper {
		t.Fatalf("deeper origin overwritten: %+v", got.Origin)
	}
}

func TestStampEventOriginNilSink(t *testing.T) {
	if StampEventOrigin(nil, agent.EventOrigin{TaskID: "t"}) != nil {
		t.Fatal("nil sink must stay nil so callers keep their nil checks")
	}
}

func TestMultiStepHandlerStampsEventOrigin(t *testing.T) {
	// End-to-end through Invoke: every event the handler's loop emits must
	// carry the request identity so the parent can attribute parallel runs.
	reg := newTestRegistry()
	comp := &multiStepMockCompleter{name: "test", responses: []string{"done"}}
	var events []agent.Event
	h := &MultiStepHandler{
		Completer:    comp,
		FullRegistry: reg,
		Model:        "test-model",
		MaxSteps:     3,
		MaxTokens:    256,
		OnEvent:      func(e agent.Event) { events = append(events, e) },
	}

	req := runtimeRequestForOriginTest()
	if _, err := h.Invoke(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("handler emitted no events")
	}
	want := agent.EventOrigin{TaskID: req.ID, Agent: req.Name, Depth: req.Depth + 1}
	for i, e := range events {
		if e.Origin != want {
			t.Fatalf("event %d (%s) origin=%+v want %+v", i, e.Kind, e.Origin, want)
		}
	}
}
