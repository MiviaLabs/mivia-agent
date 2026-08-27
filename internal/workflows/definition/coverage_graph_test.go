package definition

import (
	"strings"
	"testing"
)

func wantSubstrings(t *testing.T, errs []string, subs []string) {
	t.Helper()
	if len(subs) == 0 {
		if len(errs) > 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		return
	}
	if len(errs) == 0 {
		t.Fatal("want rejection, got no errors")
	}
	joined := strings.Join(errs, "; ")
	for _, s := range subs {
		if !strings.Contains(joined, s) {
			t.Errorf("errors %q should contain %q", joined, s)
		}
	}
}

// errToSlice converts a single error to a string slice for use with
// wantSubstrings when the function under test still returns error instead
// of []string.
func errToSlice(err error) []string {
	if err == nil {
		return nil
	}
	return []string{err.Error()}
}
func covCompile(t *testing.T, wf *WorkflowFile) *CompiledWorkflow {
	t.Helper()
	cw, err := Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return cw
}

func TestCoverageValidateCyclesTable(t *testing.T) {
	cycle := func(loop string, max int, limits Limits, partial bool) *WorkflowFile {
		steps := []Step{{ID: "implement", Kind: "agent", Agent: "go-engineer"}, {ID: "review", Kind: "agent", Agent: "go-engineer"}}
		trans := []Transition{{From: "implement", To: "review", Match: MatchCriteria{Status: "succeeded"}}, {From: "review", To: "implement", Match: MatchCriteria{Status: "succeeded"}}}
		if partial {
			// partial target on review->done: validateCycles skips terminal-bound edges, so the partial target is the only cycle-closing edge
			steps = append(steps, Step{ID: "done", Kind: "agent", Agent: "go-engineer"})
			trans[1] = Transition{From: "review", To: "done", Match: MatchCriteria{Status: "succeeded"}, Loop: "repair", MaxIterations: -1, PartialTarget: "implement"}
			trans = append(trans, Transition{From: "done", To: "success", Match: MatchCriteria{Status: "succeeded"}})
		} else {
			if loop != "" {
				trans[1].Loop, trans[1].MaxIterations = loop, max
			}
			trans = append(trans, Transition{From: "review", To: "success", Match: MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}})
		}
		return &WorkflowFile{Name: "cycle", Version: 1, InitialStep: "implement", Limits: limits, Steps: steps, Transitions: trans}
	}
	for _, tt := range []struct {
		name    string
		wf      *WorkflowFile
		wantErr []string
	}{
		{"uncapped back-edge cycle without global limit", cycle("", 0, Limits{}, false), []string{"unbounded", "global limit"}},
		{"capped edge bounds the cycle", cycle("repair", 3, Limits{}, false), nil},
		{"unnamed cycle with max_step_attempts", cycle("", 0, Limits{MaxStepAttempts: 16}, false), nil},
		{"unnamed cycle with max_duration_seconds", cycle("", 0, Limits{MaxDurationSeconds: 3600}, false), nil},
		{"acyclic graph", &WorkflowFile{Name: "acyclic", Version: 1, InitialStep: "plan", Steps: []Step{{ID: "plan", Kind: "agent", Agent: "go-engineer"}, {ID: "implement", Kind: "agent", Agent: "go-engineer"}}, Transitions: []Transition{{From: "plan", To: "implement", Match: MatchCriteria{Status: "succeeded"}}, {From: "implement", To: "success", Match: MatchCriteria{Status: "succeeded"}}}}, nil},
		{"partial_target-only cycle without cap or global limit", cycle("", 0, Limits{}, true), []string{"unbounded"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wantSubstrings(t, validateCycles(tt.wf), tt.wantErr)
		})
	}
}
func TestCoverageUnreachableStepsThroughCompile(t *testing.T) {
	orphan := func(name, extra string, trans []Transition, delivery *Delivery) *WorkflowFile {
		return &WorkflowFile{
			Name: name, Version: 1, InitialStep: "plan",
			Steps:       []Step{{ID: "plan", Kind: "agent", Agent: "planner"}, {ID: extra, Kind: "agent", Agent: "engineer"}},
			Transitions: append([]Transition{{From: "plan", To: "success", Match: MatchCriteria{Status: "succeeded"}}}, trans...),
			Delivery:    delivery,
		}
	}
	for _, tt := range []struct {
		name    string
		wf      *WorkflowFile
		wantErr []string
	}{
		{"declared orphan step with no incoming edge", orphan("o", "orphan", nil, nil), []string{"unreachable steps: orphan"}},
		{"reachable only from undeclared source", orphan("o", "orphan", []Transition{{From: "ghost", To: "orphan", Match: MatchCriteria{Status: "succeeded"}}}, nil), []string{"unreachable steps: orphan"}},
		{"named only in inactive delivery on_failure", orphan("o", "repair", []Transition{{From: "repair", To: "plan", Match: MatchCriteria{Status: "succeeded"}}}, &Delivery{Kind: "pull_request", Mode: "none", Provider: "github", Base: "main", OnFailure: "repair"}), []string{"unreachable steps: repair"}},
		{"all steps reachable control compiles", newMinimalWorkflow("all-reachable-control"), nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compile(tt.wf)
			wantSubstrings(t, errToSlice(err), tt.wantErr)
		})
	}
}
