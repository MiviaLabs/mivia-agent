package compiler

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// Transition-overlap admission tests: jointly satisfiable criteria from one
// step are allowed when one is strictly more specific (the matcher prefers
// it) and rejected when neither can win (runtime multi_match failure).

func TestCompile_TransitionsSameStatusDifferentOutputArity(t *testing.T) {
	// A status-only fallback plus a status+output special case from the same
	// step is the natural disambiguation pattern: the matcher prefers the
	// strictly more specific transition, so this compiles and routes.
	wf := &definition.WorkflowFile{
		Name:        "output-arity-test",
		Version:     1,
		InitialStep: "s",
		Steps:       []definition.Step{{ID: "s", Kind: "agent", Agent: "go-engineer"}},
		Transitions: []definition.Transition{
			{From: "s", To: "failure", Match: definition.MatchCriteria{Status: "failed", Output: map[string]string{"code": "1"}}},
			{From: "s", To: "success", Match: definition.MatchCriteria{Status: "failed"}},
		},
	}
	if _, err := Compile(wf); err != nil {
		t.Fatalf("expected status-only fallback plus specific case to compile: %v", err)
	}
}

func TestCompile_TransitionsSameStatusDisjointOutputKeysRejected(t *testing.T) {
	// Same status with disjoint output keys is jointly satisfiable with no
	// strictly more specific winner: one output carrying both keys matches
	// both transitions and the run fails with multi_match at runtime.
	wf := &definition.WorkflowFile{
		Name:        "disjoint-keys-test",
		Version:     1,
		InitialStep: "s",
		Steps:       []definition.Step{{ID: "s", Kind: "agent", Agent: "go-engineer"}},
		Transitions: []definition.Transition{
			{From: "s", To: "a", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"a": "1"}}},
			{From: "s", To: "b", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"b": "2"}}},
		},
	}
	if _, err := Compile(wf); err == nil {
		t.Fatal("expected disjoint-key same-status criteria to be rejected")
	} else if !strings.Contains(err.Error(), "ambiguous overlapping match criteria") {
		t.Fatalf("error %q should mention ambiguous overlapping match criteria", err.Error())
	}
}

func TestCompile_TransitionsSameStatusConflictingValuesAccepted(t *testing.T) {
	// Same status but conflicting values on a shared output key cannot both
	// match one output, so this is the legitimate special-case pattern.
	wf := &definition.WorkflowFile{
		Name:        "conflicting-values-test",
		Version:     1,
		InitialStep: "s",
		Steps:       []definition.Step{{ID: "s", Kind: "agent", Agent: "go-engineer"}},
		Transitions: []definition.Transition{
			{From: "s", To: "a", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
			{From: "s", To: "b", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "changes_requested"}}},
			{From: "s", To: "success", Match: definition.MatchCriteria{Status: "failed"}},
		},
	}
	if _, err := Compile(wf); err != nil {
		t.Fatalf("expected conflicting-value same-status criteria to compile: %v", err)
	}
}

func TestCompile_TransitionsDifferentStatusesAccepted(t *testing.T) {
	// Different statuses never overlap regardless of output criteria.
	wf := &definition.WorkflowFile{
		Name:        "different-status-test",
		Version:     1,
		InitialStep: "s",
		Steps:       []definition.Step{{ID: "s", Kind: "agent", Agent: "go-engineer"}},
		Transitions: []definition.Transition{
			{From: "s", To: "a", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"code": "1"}}},
			{From: "s", To: "success", Match: definition.MatchCriteria{Status: "failed"}},
		},
	}
	if _, err := Compile(wf); err != nil {
		t.Fatalf("expected different-status criteria to compile: %v", err)
	}
}

func TestCompile_TransitionsStrictSupersetAccepted(t *testing.T) {
	// A strictly more specific transition (superset output keys) plus a less
	// specific one routes to the specific match at runtime, so it compiles.
	wf := &definition.WorkflowFile{
		Name:        "superset-test",
		Version:     1,
		InitialStep: "s",
		Steps:       []definition.Step{{ID: "s", Kind: "agent", Agent: "go-engineer"}},
		Transitions: []definition.Transition{
			{From: "s", To: "specific", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"a": "1", "b": "2"}}},
			{From: "s", To: "fallback", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"a": "1"}}},
			{From: "s", To: "success", Match: definition.MatchCriteria{Status: "failed"}},
		},
	}
	if _, err := Compile(wf); err != nil {
		t.Fatalf("expected strict-superset criteria to compile: %v", err)
	}
}
