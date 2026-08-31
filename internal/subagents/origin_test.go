package subagents

import (
	"context"
	"encoding/json"
	"testing"
	"unicode/utf8"

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
	want := agent.EventOrigin{
		TaskID:          req.ID,
		Agent:           req.Name,
		Depth:           req.Depth + 1,
		TaskDescription: "review the patch",
	}
	for i, e := range events {
		if e.Origin != want {
			t.Fatalf("event %d (%s) origin=%+v want %+v", i, e.Kind, e.Origin, want)
		}
	}
}

func TestTaskDescriptionFromInputUnwrapsABareJSONString(t *testing.T) {
	// delegate's Input is always a bare JSON string (json.Marshal(params.Task)).
	got := taskDescriptionFromInput(json.RawMessage(`"analyze the auth module"`))
	if got != "analyze the auth module" {
		t.Fatalf("got %q, want unwrapped string", got)
	}
}

func TestTaskDescriptionFromInputFallsBackToRawJSONForStructuredInput(t *testing.T) {
	// dispatch_tasks/spawn_agent's per-task Input can be arbitrary JSON
	// shaped by that task's own input schema, not a bare string - still a
	// useful-enough preview even if it isn't natural language.
	got := taskDescriptionFromInput(json.RawMessage(`{"topic":"auth","depth":2}`))
	if got != `{"topic":"auth","depth":2}` {
		t.Fatalf("got %q, want raw JSON fallback", got)
	}
}

func TestTaskDescriptionFromInputEmptyForNoInput(t *testing.T) {
	if got := taskDescriptionFromInput(nil); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestTaskDescriptionFromInputBoundsALongDescription(t *testing.T) {
	long := ""
	for i := 0; i < 300; i++ {
		long += "a"
	}
	raw, _ := json.Marshal(long)
	got := taskDescriptionFromInput(raw)
	if len(got) != maxTaskDescriptionBytes+len("…") {
		t.Fatalf("got length %d, want %d (bound + ellipsis)", len(got), maxTaskDescriptionBytes+len("…"))
	}
	if got[len(got)-len("…"):] != "…" {
		t.Fatalf("got %q, want trailing ellipsis marking truncation", got)
	}
}

// TestTaskDescriptionFromInputStaysValidUTF8AcrossARuneSplit puts a 3-byte
// rune ("世") straddling the maxTaskDescriptionBytes cut point, so a naive
// s[:maxTaskDescriptionBytes] byte-offset truncation would split it (DC-6).
func TestTaskDescriptionFromInputStaysValidUTF8AcrossARuneSplit(t *testing.T) {
	prefix := ""
	for i := 0; i < maxTaskDescriptionBytes-1; i++ {
		prefix += "a"
	}
	long := prefix + "世" + "more text after the rune"
	raw, _ := json.Marshal(long)
	got := taskDescriptionFromInput(raw)
	if !utf8.ValidString(got) {
		t.Fatalf("bounded description is not valid UTF-8: %q", got)
	}
}

func TestStampEventOriginUsesTaskIdentityFromContext(t *testing.T) {
	// Coordinator calls stamp the workflow attempt's task id (wft-...) on the
	// context. Events must carry that id instead of the opaque runtime session
	// token so bus, ledger, and attempt events share one correlation key. The
	// identity needs a non-empty RunID: TaskIdentityFrom only reports ok when
	// both RunID and TaskID are present.
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
	ctx := runtime.ContextWithTaskIdentity(t.Context(), runtime.TaskIdentity{
		RunID:  "run-wft-1",
		TaskID: "wft-test-1",
		Agent:  req.Name,
	})
	if _, err := h.Invoke(ctx, req); err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("handler emitted no events")
	}
	for i, e := range events {
		if e.Origin.TaskID != "wft-test-1" {
			t.Fatalf("event %d (%s) TaskID=%q want %q", i, e.Kind, e.Origin.TaskID, "wft-test-1")
		}
	}
}

// TestOriginForRequestCarriesSessionAndTurn pins the identity a subagent's
// events need to survive the trip to a second live surface.
//
// The subagent publish path (clichat.emitSubagentProgress) is package-level and
// has no session context of its own, so it reads these off the origin. Without
// them every subagent event was published with an empty SessionID and dropped
// by internal/hub's receiver, which matches on SessionID - so a second surface
// saw the root loop's tool calls and none of its subagents'.
func TestOriginForRequestCarriesSessionAndTurn(t *testing.T) {
	req := runtimeRequestForOriginTest()
	req.SessionID = "sess-abc"
	req.TurnID = "turn:3"

	got := originForRequest(context.Background(), req)

	if got.SessionID != "sess-abc" {
		t.Errorf("SessionID = %q, want the dispatching session; a hub receiver drops this event", got.SessionID)
	}
	if got.TurnID != "turn:3" {
		t.Errorf("TurnID = %q, want the dispatching turn", got.TurnID)
	}
	if got.TaskID != "task-42" || got.Agent != "audit" || got.Depth != 1 {
		t.Errorf("attribution regressed: %+v", got)
	}
}

// TestOriginForRequestPrefersTheCoordinatorTaskID keeps the correlation key's
// own rule covered now that it lives in a named function: a coordinator call
// carries the workflow attempt id on the context and it must win over the
// request id, so bus, ledger, and attempt events share one key.
func TestOriginForRequestPrefersTheCoordinatorTaskID(t *testing.T) {
	ctx := runtime.ContextWithTaskIdentity(context.Background(), runtime.TaskIdentity{RunID: "run-1", TaskID: "wft-9"})

	got := originForRequest(ctx, runtimeRequestForOriginTest())

	if got.TaskID != "wft-9" {
		t.Errorf("TaskID = %q, want the coordinator's attempt id", got.TaskID)
	}
}
