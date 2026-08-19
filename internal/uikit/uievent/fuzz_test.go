package uievent

import (
	"encoding/json"
	"testing"
)

// FuzzEventUnmarshalJSON proves Event.UnmarshalJSON never panics on
// adversarial or malformed input. It decodes fixtures and --output json
// consumers will eventually feed it bytes from outside this process; the
// invariant it must hold is "error or valid Event, never a panic."
func FuzzEventUnmarshalJSON(f *testing.F) {
	seeds := []Event{
		{Kind: KindTurnStart, TurnID: "t1", Seq: 1, Body: TurnStartBody{Input: "hi"}},
		{Kind: KindToolEnd, TurnID: "t1", Seq: 2, Body: ToolEndBody{
			ToolCallID: "c1", OK: true,
			Diff: &Diff{Path: "a.go", Hunks: []DiffHunk{{Lines: []DiffLine{{Kind: DiffLineAdd, Text: "x"}}}}},
		}},
		{Kind: KindToolOutput, TurnID: "t1", Seq: 3, Body: ToolOutputBody{
			ToolCallID: "c2", Progress: &Progress{Step: 1, TotalSteps: 2, Log: []string{"a"}},
		}},
	}
	for _, ev := range seeds {
		raw, err := json.Marshal(ev)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	f.Add([]byte(`not json`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"kind":"turn.start","body":123}`))
	f.Add([]byte(`null`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var e Event
		_ = json.Unmarshal(data, &e) // error is expected and fine; a panic is not
	})
}
