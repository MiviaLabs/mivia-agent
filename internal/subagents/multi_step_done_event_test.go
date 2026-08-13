package subagents

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// Run-level lifecycle: a subagent that stops running must say so. Nested tool
// events only report tool lifecycle, so without a terminal signal the parent
// UI cannot tell a finished agent from one thinking between two tool calls.

func doneEventRequest() runtime.Request {
	return runtime.Request{
		ID:    "task-done",
		Name:  "audit",
		Depth: 0,
		Input: json.RawMessage(`"review the patch"`),
	}
}

func TestMultiStepHandlerEmitsSubagentDoneOnSuccess(t *testing.T) {
	h := &MultiStepHandler{
		Completer:    &multiStepMockCompleter{name: "test", responses: []string{"done"}},
		FullRegistry: newTestRegistry(),
		Model:        "test-model",
		MaxSteps:     3,
		MaxTokens:    256,
	}
	var got []agent.Event
	h.OnEvent = func(e agent.Event) { got = append(got, e) }

	req := doneEventRequest()
	if _, err := h.Invoke(t.Context(), req); err != nil {
		t.Fatal(err)
	}

	if len(got) == 0 {
		t.Fatal("handler emitted no events")
	}
	last := got[len(got)-1]
	if last.Kind != agent.EventSubagentDone {
		t.Fatalf("last event kind=%s want %s (done must be terminal: anything after it revives a retired agent row)", last.Kind, agent.EventSubagentDone)
	}
	want := agent.EventOrigin{
		TaskID:          req.ID,
		Agent:           req.Name,
		Depth:           req.Depth + 1,
		TaskDescription: "review the patch",
	}
	if last.Origin != want {
		t.Fatalf("done origin=%+v want %+v - unattributed done retires nothing", last.Origin, want)
	}
	if n := countKind(got, agent.EventSubagentDone); n != 1 {
		t.Fatalf("done emitted %d times, want exactly 1", n)
	}
}

func TestMultiStepHandlerEmitsSubagentDoneOnFailure(t *testing.T) {
	// A run that dies still stops running: the row must not outlive it.
	h := &MultiStepHandler{
		Completer:    &multiStepMockCompleter{name: "test", chatTurnErr: context.DeadlineExceeded},
		FullRegistry: newTestRegistry(),
		Model:        "test-model",
		MaxSteps:     3,
		MaxTokens:    256,
	}
	var got []agent.Event
	h.OnEvent = func(e agent.Event) { got = append(got, e) }

	if _, err := h.Invoke(t.Context(), doneEventRequest()); err == nil {
		t.Fatal("expected the mock completer error to surface")
	}
	if countKind(got, agent.EventSubagentDone) != 1 {
		t.Fatalf("failed run must still emit exactly one done event, got %d", countKind(got, agent.EventSubagentDone))
	}
}

func countKind(evts []agent.Event, kind agent.EventKind) int {
	n := 0
	for _, e := range evts {
		if e.Kind == kind {
			n++
		}
	}
	return n
}
