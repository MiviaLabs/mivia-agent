package uievent

import "testing"

// TestAllBodyTypesSatisfyBody exercises isBody() on every concrete Body
// implementation. It does not assert exhaustiveness against the Kind
// enum: a second hand-maintained list in this file could drift from
// event.go's real Kind consts without either list catching the other.
// TestEventJSONRoundTrip's 13-case coverage is what pins exhaustiveness,
// since it round-trips through unmarshalBody's real production switch.
func TestAllBodyTypesSatisfyBody(t *testing.T) {
	bodies := []Body{
		TurnStartBody{},
		TextDeltaBody{},
		TextEndBody{},
		ReasoningDeltaBody{},
		ToolPendingBody{},
		ToolStartBody{},
		ToolOutputBody{},
		ToolEndBody{},
		PlanBody{},
		NoticeBody{},
		UsageBody{},
		ErrorBody{},
		TurnEndBody{},
	}
	for _, b := range bodies {
		b.isBody() // must not panic; the assertion is that this compiles and runs
	}
}

// TestEventMsg verifies that EventMsg holds an Event.
func TestEventMsg(t *testing.T) {
	ev := Event{
		Kind:   KindNotice,
		TurnID: "turn-1",
		Seq:    42,
		Body:   NoticeBody{Text: "test notice"},
	}
	msg := EventMsg{Event: ev}
	if msg.Event.Kind != KindNotice {
		t.Errorf("got Kind %v, want %v", msg.Event.Kind, KindNotice)
	}
	if msg.Event.TurnID != "turn-1" {
		t.Errorf("got TurnID %q, want %q", msg.Event.TurnID, "turn-1")
	}
	if msg.Event.Seq != 42 {
		t.Errorf("got Seq %d, want 42", msg.Event.Seq)
	}
	nb, ok := msg.Event.Body.(NoticeBody)
	if !ok || nb.Text != "test notice" {
		t.Errorf("got Body %+v, want NoticeBody with text", msg.Event.Body)
	}
}
