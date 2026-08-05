package compiler

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// Cycle admission policy tests. Compile rejects a workflow whose graph has a
// cycle with no finite-capped loop edge (max_iterations > 0) when both global
// limits are 0. Unlimited loops (-1) and unlimited max_step_attempts (0)
// stay legal whenever at least one global limit is set.

func TestCompile_UnlimitedLoopWithoutGlobalLimitRejected(t *testing.T) {
	// Same single-step unlimited self-loop as TestCompile_UnlimitedLoop, but with no
	// global limits set, so the workflow could execute forever.
	wf := &definition.WorkflowFile{
		Name:        "unlimited-loop-no-limit-test",
		Version:     1,
		InitialStep: "implement",
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "go-engineer"},
		},
		Transitions: []definition.Transition{
			{
				From:          "implement",
				To:            "implement",
				Match:         definition.MatchCriteria{Status: "failed"},
				Loop:          "fix-loop",
				MaxIterations: -1,
			},
			{
				From:  "implement",
				To:    "success",
				Match: definition.MatchCriteria{Status: "succeeded"},
			},
		},
	}
	_, err := Compile(wf)
	if err == nil {
		t.Fatal("expected compile error for unlimited loop without a global limit")
	}
	if !strings.Contains(err.Error(), "unbounded") {
		t.Errorf("error %q should mention \"unbounded\"", err.Error())
	}
	if !strings.Contains(err.Error(), "global limit") {
		t.Errorf("error %q should mention \"global limit\"", err.Error())
	}
}

func TestCompile_UnnamedCycleWithoutGlobalLimitRejected(t *testing.T) {
	// implement <-> review cycle with unnamed (uncapped) edges and no global limits
	// could execute forever; only the review->success transition can terminate it.
	wf := &definition.WorkflowFile{
		Name:        "unnamed-cycle-no-limit-test",
		Version:     1,
		InitialStep: "implement",
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "go-engineer"},
			{ID: "review", Kind: "agent", Agent: "go-engineer"},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "implement", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
		},
	}
	_, err := Compile(wf)
	if err == nil {
		t.Fatal("expected compile error for unnamed cycle without a global limit")
	}
	if !strings.Contains(err.Error(), "unbounded") {
		t.Errorf("error %q should mention \"unbounded\"", err.Error())
	}
	if !strings.Contains(err.Error(), "global limit") {
		t.Errorf("error %q should mention \"global limit\"", err.Error())
	}
}

func TestCompile_UnnamedCycleWithGlobalLimitAccepted(t *testing.T) {
	// Same unnamed cycle as above, but a global max_step_attempts limit bounds it.
	wf := &definition.WorkflowFile{
		Name:        "unnamed-cycle-with-limit-test",
		Version:     1,
		InitialStep: "implement",
		Limits:      definition.Limits{MaxStepAttempts: 16},
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "go-engineer"},
			{ID: "review", Kind: "agent", Agent: "go-engineer"},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "implement", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
		},
	}
	if _, err := Compile(wf); err != nil {
		t.Fatalf("expected unnamed cycle with max_step_attempts=16 to compile, got: %v", err)
	}
}

func TestCompile_UnlimitedAttemptsWithUnlimitedLoopAccepted(t *testing.T) {
	// Unlimited loop plus unlimited max_step_attempts is legal because
	// max_duration_seconds=3600 still guarantees termination.
	wf := &definition.WorkflowFile{
		Name:        "unlimited-attempts-loop-test",
		Version:     1,
		InitialStep: "implement",
		Limits:      definition.Limits{MaxStepAttempts: 0, MaxDurationSeconds: 3600},
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "go-engineer"},
			{ID: "review", Kind: "agent", Agent: "go-engineer"},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "implement", Match: definition.MatchCriteria{Status: "succeeded"}, Loop: "repair", MaxIterations: -1},
			{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
		},
	}
	if _, err := Compile(wf); err != nil {
		t.Fatalf("expected unlimited loop with max_duration_seconds=3600 to compile, got: %v", err)
	}
}

func TestCompile_FiniteCappedLoopWithoutGlobalLimitAccepted(t *testing.T) {
	// A capped loop (max_iterations=3) terminates on its own, so no global limits
	// are required.
	wf := &definition.WorkflowFile{
		Name:        "capped-loop-no-limit-test",
		Version:     1,
		InitialStep: "implement",
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "go-engineer"},
			{ID: "review", Kind: "agent", Agent: "go-engineer"},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "implement", Match: definition.MatchCriteria{Status: "succeeded"}, Loop: "repair", MaxIterations: 3},
			{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
		},
	}
	if _, err := Compile(wf); err != nil {
		t.Fatalf("expected capped loop (max_iterations=3) without global limits to compile, got: %v", err)
	}
}

func TestCompile_MixedUnlimitedAndCappedCycleAccepted(t *testing.T) {
	// One edge of the cycle is capped (max_iterations=3), so the cycle as a whole
	// cannot run forever; no global limits are required.
	wf := &definition.WorkflowFile{
		Name:        "mixed-cycle-test",
		Version:     1,
		InitialStep: "implement",
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "go-engineer"},
			{ID: "review", Kind: "agent", Agent: "go-engineer"},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}, Loop: "cap", MaxIterations: 3},
			{From: "review", To: "implement", Match: definition.MatchCriteria{Status: "succeeded"}, Loop: "unlimited", MaxIterations: -1},
			{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
		},
	}
	if _, err := Compile(wf); err != nil {
		t.Fatalf("expected cycle with at least one capped edge to compile, got: %v", err)
	}
}

func TestCompileForResumeAcceptsPreviouslyAdmittedUnboundedWorkflow(t *testing.T) {
	// A workflow admitted before the unbounded-cycle policy must still
	// resume. CompileForResume skips only the cycle check.
	wf := &definition.WorkflowFile{
		Name: "resume-policy-test", Version: 1, InitialStep: "implement",
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "go-engineer"},
			{ID: "review", Kind: "agent", Agent: "go-engineer"},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "implement", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
		},
	}
	if _, err := Compile(wf); err == nil || !strings.Contains(err.Error(), "unbounded") {
		t.Fatalf("Compile = %v, want unbounded-cycle rejection", err)
	}
	cw, err := CompileForResume(wf)
	if err != nil {
		t.Fatalf("CompileForResume rejected an admitted definition: %v", err)
	}
	if cw.Digest == "" {
		t.Fatal("CompileForResume produced an empty digest")
	}
	cw2, err := CompileForResume(wf)
	if err != nil || cw2.Digest != cw.Digest {
		t.Fatalf("resume digest unstable: %q vs %q (err=%v)", cw.Digest, cw2.Digest, err)
	}
	// The digest is policy-independent: a valid workflow has the same digest
	// under both entry points.
	valid := newMinimalWorkflow("digest-policy-equal")
	a, err := Compile(valid)
	if err != nil {
		t.Fatal(err)
	}
	b, err := CompileForResume(valid)
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest != b.Digest {
		t.Fatalf("digests differ: %q vs %q", a.Digest, b.Digest)
	}
}

func TestCompileForResumeStillRejectsStructuralErrors(t *testing.T) {
	// CompileForResume skips only the cycle check; other validators run.
	wf := &definition.WorkflowFile{
		Name: "resume-structural-test", Version: 1, InitialStep: "s",
		Steps: []definition.Step{{ID: "s", Kind: "agent", Agent: "go-engineer"}},
		Transitions: []definition.Transition{
			{From: "s", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "s", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	if _, err := CompileForResume(wf); err == nil || !strings.Contains(err.Error(), "overlapping") {
		t.Fatalf("CompileForResume = %v, want overlap rejection", err)
	}
}
