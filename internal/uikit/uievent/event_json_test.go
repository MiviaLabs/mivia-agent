package uievent

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestEventJSONRoundTrip(t *testing.T) {
	at := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	cases := []Event{
		{Kind: KindTurnStart, TurnID: "t1", Seq: 1, At: at, Body: TurnStartBody{Input: "hello"}},
		{Kind: KindTextDelta, TurnID: "t1", Seq: 2, At: at, Body: TextDeltaBody{Text: "chunk"}},
		{Kind: KindTextEnd, TurnID: "t1", Seq: 2, At: at, Body: TextEndBody{Text: "full text"}},
		{Kind: KindReasoning, TurnID: "t1", Seq: 2, At: at, Body: ReasoningDeltaBody{Text: "thinking", WordCount: 12}},
		{Kind: KindToolPending, TurnID: "t1", Seq: 2, At: at, Body: ToolPendingBody{
			ToolCallID: "c0", Name: "edit", Args: map[string]any{"path": "a.go"},
			Diff: &Diff{
				Path: "a.go", Added: 1,
				Hunks: []DiffHunk{{Header: "@@ -1 +1 @@", Lines: []DiffLine{{Kind: DiffLineAdd, Text: "x"}}}},
			},
		}},
		{Kind: KindToolStart, TurnID: "t1", Seq: 2, At: at, Body: ToolStartBody{
			ToolCallID: "c1", Name: "read_file", Args: map[string]any{"path": "a.go"},
		}},
		{Kind: KindError, TurnID: "t1", Seq: 2, At: at, Body: ErrorBody{Text: "boom", Fatal: true}},
		{Kind: KindUsage, TurnID: "t1", Seq: 2, At: at, Body: UsageBody{
			InputTokens: 1, OutputTokens: 2, CachedTokens: 3, CostUSD: 0.1, ElapsedSeconds: 1.5,
		}},
		{Kind: KindToolOutput, TurnID: "t1", Seq: 3, At: at, Body: ToolOutputBody{
			ToolCallID: "c1",
			Progress:   &Progress{Step: 2, TotalSteps: 3, ElapsedSeconds: 18, Status: "running", Log: []string{"+ did a thing"}},
		}},
		{Kind: KindToolEnd, TurnID: "t1", Seq: 4, At: at, Body: ToolEndBody{
			ToolCallID: "c2", OK: true, Diff: &Diff{
				Path: "a.go", Added: 4, Removed: 1,
				Hunks: []DiffHunk{{Header: "@@ -1,3 +1,4 @@", Lines: []DiffLine{{Kind: DiffLineAdd, Text: "+x"}}}},
			},
		}},
		{Kind: KindPlan, TurnID: "t1", Seq: 5, At: at, Body: PlanBody{
			Items: []PlanItem{{Text: "step one", Done: true}, {Text: "step two"}},
			Done:  1, Total: 2,
		}},
		{Kind: KindNotice, TurnID: "t1", Seq: 6, At: at, Body: NoticeBody{Text: "context 62%"}},
		{Kind: KindTurnEnd, TurnID: "t1", Seq: 7, At: at, Body: TurnEndBody{Reason: "completed"}},
	}

	for _, want := range cases {
		raw, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("marshal %s: %v", want.Kind, err)
		}
		var got Event
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal %s: %v", want.Kind, err)
		}
		if got.Kind != want.Kind || got.TurnID != want.TurnID || got.Seq != want.Seq || !got.At.Equal(want.At) {
			t.Fatalf("envelope mismatch for %s: got %+v want %+v", want.Kind, got, want)
		}
		gotBody, err := json.Marshal(got.Body)
		if err != nil {
			t.Fatalf("re-marshal body %s: %v", want.Kind, err)
		}
		wantBody, err := json.Marshal(want.Body)
		if err != nil {
			t.Fatalf("marshal want body %s: %v", want.Kind, err)
		}
		if string(gotBody) != string(wantBody) {
			t.Fatalf("body mismatch for %s: got %s want %s", want.Kind, gotBody, wantBody)
		}
	}
}

func TestEventUnmarshalUnknownKind(t *testing.T) {
	var e Event
	err := json.Unmarshal([]byte(`{"kind":"bogus","turn_id":"t1","seq":1,"at":"2026-08-19T12:00:00Z","body":{}}`), &e)
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestEventUnmarshalMalformedEnvelope(t *testing.T) {
	// A syntactically invalid document never reaches Event.UnmarshalJSON:
	// encoding/json rejects it in the scanner first. The envelope branch is
	// reachable only through a document that parses but whose envelope
	// FIELDS have the wrong types, so both inputs are pinned here.
	var e Event
	if err := json.Unmarshal([]byte(`not json`), &e); err == nil {
		t.Fatal("expected error for malformed envelope")
	}

	var typed Event
	err := json.Unmarshal([]byte(`{"kind":"turn.start","seq":"one"}`), &typed)
	if err == nil {
		t.Fatal("expected error for a non-numeric seq")
	}
	if !strings.Contains(err.Error(), "uievent: unmarshal envelope") {
		t.Errorf("error = %q, want it to name the envelope stage", err)
	}
	if typed.Kind != "" || typed.Body != nil {
		t.Errorf("event = %#v, want it left untouched on envelope failure", typed)
	}
}

func TestEventUnmarshalMalformedBody(t *testing.T) {
	// A valid Kind whose body doesn't decode into that Kind's struct
	// shape (a number instead of an object) must surface the per-kind
	// unmarshal error, not silently zero-value the body.
	var e Event
	raw := `{"kind":"turn.start","turn_id":"t1","seq":1,"at":"2026-08-19T12:00:00Z","body":123}`
	if err := json.Unmarshal([]byte(raw), &e); err == nil {
		t.Fatal("expected error for malformed body")
	}
}

func TestEventMarshalBodyFailure(t *testing.T) {
	// encoding/json refuses NaN/Inf floats; this is the simplest real Body
	// value that fails to marshal, exercising MarshalJSON's error path
	// without a contrived non-Body type.
	e := Event{Kind: KindUsage, Body: UsageBody{CostUSD: math.NaN()}}
	if _, err := json.Marshal(e); err == nil {
		t.Fatal("expected error marshalling a NaN field")
	}
}

// otherBody is a Body implementation outside the switch in derefBody,
// used only to exercise its default passthrough branch: unmarshalBody's
// own switch always constructs a listed *XxxBody, so that branch is dead
// in production but is still worth pinning directly.
type otherBody struct{}

func (otherBody) isBody() {}

func TestDerefBodyPassthroughForUnknownType(t *testing.T) {
	var b Body = otherBody{}
	if got := derefBody(b); got != b {
		t.Errorf("derefBody(otherBody{}) = %#v, want unchanged %#v", got, b)
	}
}
