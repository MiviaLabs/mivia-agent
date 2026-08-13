package compiler

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

func wantSubstrings(t *testing.T, err error, subs []string) {
	t.Helper()
	if len(subs) == 0 {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatal("want rejection, got nil error")
	}
	for _, s := range subs {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q should contain %q", err.Error(), s)
		}
	}
}
func covCompile(t *testing.T, wf *definition.WorkflowFile) *CompiledWorkflow {
	t.Helper()
	cw, err := Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return cw
}

func TestCoverageValidateCyclesTable(t *testing.T) {
	cycle := func(loop string, max int, limits definition.Limits, partial bool) *definition.WorkflowFile {
		steps := []definition.Step{{ID: "implement", Kind: "agent", Agent: "go-engineer"}, {ID: "review", Kind: "agent", Agent: "go-engineer"}}
		trans := []definition.Transition{{From: "implement", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}}, {From: "review", To: "implement", Match: definition.MatchCriteria{Status: "succeeded"}}}
		if partial {
			// partial target on review->done: validateCycles skips terminal-bound edges, so the partial target is the only cycle-closing edge
			steps = append(steps, definition.Step{ID: "done", Kind: "agent", Agent: "go-engineer"})
			trans[1] = definition.Transition{From: "review", To: "done", Match: definition.MatchCriteria{Status: "succeeded"}, Loop: "repair", MaxIterations: -1, PartialTarget: "implement"}
			trans = append(trans, definition.Transition{From: "done", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}})
		} else {
			if loop != "" {
				trans[1].Loop, trans[1].MaxIterations = loop, max
			}
			trans = append(trans, definition.Transition{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}})
		}
		return &definition.WorkflowFile{Name: "cycle", Version: 1, InitialStep: "implement", Limits: limits, Steps: steps, Transitions: trans}
	}
	for _, tt := range []struct {
		name    string
		wf      *definition.WorkflowFile
		wantErr []string
	}{
		{"uncapped back-edge cycle without global limit", cycle("", 0, definition.Limits{}, false), []string{"unbounded", "global limit"}},
		{"capped edge bounds the cycle", cycle("repair", 3, definition.Limits{}, false), nil},
		{"unnamed cycle with max_step_attempts", cycle("", 0, definition.Limits{MaxStepAttempts: 16}, false), nil},
		{"unnamed cycle with max_duration_seconds", cycle("", 0, definition.Limits{MaxDurationSeconds: 3600}, false), nil},
		{"acyclic graph", &definition.WorkflowFile{Name: "acyclic", Version: 1, InitialStep: "plan", Steps: []definition.Step{{ID: "plan", Kind: "agent", Agent: "go-engineer"}, {ID: "implement", Kind: "agent", Agent: "go-engineer"}}, Transitions: []definition.Transition{{From: "plan", To: "implement", Match: definition.MatchCriteria{Status: "succeeded"}}, {From: "implement", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}}}}, nil},
		{"partial_target-only cycle without cap or global limit", cycle("", 0, definition.Limits{}, true), []string{"unbounded"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wantSubstrings(t, validateCycles(tt.wf), tt.wantErr)
		})
	}
}
func TestCoverageUnreachableStepsThroughCompile(t *testing.T) {
	orphan := func(name, extra string, trans []definition.Transition, delivery *definition.Delivery) *definition.WorkflowFile {
		return &definition.WorkflowFile{
			Name: name, Version: 1, InitialStep: "plan",
			Steps:       []definition.Step{{ID: "plan", Kind: "agent", Agent: "planner"}, {ID: extra, Kind: "agent", Agent: "engineer"}},
			Transitions: append([]definition.Transition{{From: "plan", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}}}, trans...),
			Delivery:    delivery,
		}
	}
	for _, tt := range []struct {
		name    string
		wf      *definition.WorkflowFile
		wantErr []string
	}{
		{"declared orphan step with no incoming edge", orphan("o", "orphan", nil, nil), []string{"unreachable steps: orphan"}},
		{"reachable only from undeclared source", orphan("o", "orphan", []definition.Transition{{From: "ghost", To: "orphan", Match: definition.MatchCriteria{Status: "succeeded"}}}, nil), []string{"unreachable steps: orphan"}},
		{"named only in inactive delivery on_failure", orphan("o", "repair", []definition.Transition{{From: "repair", To: "plan", Match: definition.MatchCriteria{Status: "succeeded"}}}, &definition.Delivery{Kind: "pull_request", Mode: "none", Provider: "github", Base: "main", OnFailure: "repair"}), []string{"unreachable steps: repair"}},
		{"all steps reachable control compiles", newMinimalWorkflow("all-reachable-control"), nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compile(tt.wf)
			wantSubstrings(t, err, tt.wantErr)
		})
	}
}
