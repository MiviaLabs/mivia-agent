package uievent

import "testing"

// TestAllBodyTypesSatisfyBody exercises isBody() on every concrete Body
// implementation. isBody has no logic (it is a sealing marker), so this
// is not asserting behaviour beyond "every listed type is a Body" - which
// the switch in unmarshalBody already depends on at compile time - but it
// pins the complete set of sealed types in one place: a new Body impl
// that forgets to appear here is a signal the Kind enum grew without a
// matching entry in this list.
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
	if len(bodies) != len(allKindsForTest()) {
		t.Fatalf("got %d Body types, want one per Kind (%d)", len(bodies), len(allKindsForTest()))
	}
}

// allKindsForTest lists every Kind, for the count check above.
func allKindsForTest() []Kind {
	return []Kind{
		KindTurnStart, KindTextDelta, KindTextEnd, KindReasoning,
		KindToolPending, KindToolStart, KindToolOutput, KindToolEnd,
		KindPlan, KindNotice, KindUsage, KindError, KindTurnEnd,
	}
}
