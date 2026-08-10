package matcher

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// TestProbeDuplicateOutputCriteriaFailsClosed pins the matcher's fail-closed
// behavior for identical output criteria: two transitions from one step with
// the SAME status+output criteria admit no safe winner, so Match returns
// multi_match with transition index -1. The compiler rejects this shape at
// admission (transitionCriteriaHazard), but the matcher must still fail
// closed if such criteria ever reach it (resume of an older snapshot).
func TestProbeDuplicateOutputCriteriaFailsClosed(t *testing.T) {
	transitions := []definition.Transition{
		{From: "a", To: "b", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
		{From: "a", To: "c", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
	}
	d, err := Match("a", "succeeded", map[string]any{"verdict": "approved"}, transitions)
	if err == nil {
		t.Fatal("expected multi-match error for identical output criteria")
	}
	if d.Outcome != "multi_match" || d.TransitionIndex != -1 {
		t.Fatalf("decision = %+v, want multi_match with transition index -1", d)
	}
}

// TestProbeNullArrayAndObjectLeavesNeverMatch pins that non-scalar output
// leaves (null, array, object) never satisfy an output key: scalarString
// rejects them, so the transition does not match and Match fails closed with
// zero_match instead of routing on a coercion.
func TestProbeNullArrayAndObjectLeavesNeverMatch(t *testing.T) {
	transitions := []definition.Transition{
		{From: "a", To: "b", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"k": "x"}}},
	}
	for _, output := range []map[string]any{
		{"k": nil},
		{"k": []any{"x"}},
		{"k": map[string]any{"n": 1}},
	} {
		d, err := Match("a", "succeeded", output, transitions)
		if err == nil {
			t.Fatalf("Match with output %#v must not match a non-scalar leaf", output)
		}
		if d.Outcome != "zero_match" {
			t.Fatalf("decision = %+v, want zero_match for non-scalar leaf %#v", d, output["k"])
		}
	}
}
