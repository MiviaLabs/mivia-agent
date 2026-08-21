package definition

import (
	"testing"
)

// TestCompile_DeadSuccessEdgeFromTerminal pins that a transition to the
// success terminal must originate from a declared step. Reserved terminal
// IDs like failure and success are never used as match sources at runtime,
// so edges from them are dead and must not satisfy the success-path check.
func TestCompile_DeadSuccessEdgeFromTerminal(t *testing.T) {
	t.Run("from failure", func(t *testing.T) {
		wf := newMinimalWorkflow("dead-success-from-failure")
		wf.Transitions = []Transition{
			{From: "failure", To: "success", Match: MatchCriteria{Status: "succeeded"}},
		}
		assertCompileError(t, wf, "dead success edge from failure", "no transition leads to the success terminal")
	})

	t.Run("from success", func(t *testing.T) {
		wf := newMinimalWorkflow("dead-success-from-success")
		wf.Transitions = []Transition{
			{From: "success", To: "success", Match: MatchCriteria{Status: "succeeded"}},
		}
		assertCompileError(t, wf, "dead success edge from success", "no transition leads to the success terminal")
	})

	t.Run("from declared step", func(t *testing.T) {
		wf := newMinimalWorkflow("valid-success-from-step")
		wf.Transitions = []Transition{
			{From: "plan", To: "success", Match: MatchCriteria{Status: "succeeded"}},
		}
		if _, err := Compile(wf); err != nil {
			t.Fatalf("expected compile to succeed for success edge from declared step: %v", err)
		}
	})
}
