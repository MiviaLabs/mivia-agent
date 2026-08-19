package uievent

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEventJSONRoundTrip(t *testing.T) {
	at := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	cases := []Event{
		{Kind: KindTurnStart, TurnID: "t1", Seq: 1, At: at, Body: TurnStartBody{Input: "hello"}},
		{Kind: KindTextDelta, TurnID: "t1", Seq: 2, At: at, Body: TextDeltaBody{Text: "chunk"}},
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
