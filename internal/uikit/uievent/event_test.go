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
