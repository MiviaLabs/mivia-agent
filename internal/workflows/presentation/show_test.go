package presentation

import (
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

func TestFormatWorkflowShow_NameAndHeader(t *testing.T) {
	c := &compiler.CompiledWorkflow{
		Name:        "feature-delivery",
		Description: "Plan, challenge plan, plan tests, review test plan, implement (with tests), review, validate tests, verify code, validate results, and request approval.",
		Version:     1,
		InitialStep: "plan",
	}
	result := FormatWorkflowShow(c)

	// Must contain the name.
	if !contains(result, "Name:        feature-delivery") {
		t.Errorf("output missing name:\n%s", result)
	}
	// Must contain description.
	if !contains(result, "Description: Plan, challenge plan, plan tests, review test plan, implement (with tests), review, validate tests, verify code, validate results, and request approval.") {
		t.Errorf("output missing description:\n%s", result)
	}
	// Must contain version.
	if !contains(result, "Version:     1") {
		t.Errorf("output missing version:\n%s", result)
	}
	// Must contain initial step.
	if !contains(result, "Initial:     plan") {
		t.Errorf("output missing initial step:\n%s", result)
	}
}

func TestFormatWorkflowShow_Inputs(t *testing.T) {
	c := &compiler.CompiledWorkflow{
		Name: "test",
		Inputs: map[string]definition.InputDef{
			"task": {Type: "string", Required: true, MaxBytes: 12000},
			"note": {Type: "string", Required: false},
		},
	}
	result := FormatWorkflowShow(c)

	if !contains(result, "Inputs:") {
		t.Error("output missing Inputs section")
	}
	if !contains(result, "task (string, required, max 12000 bytes)") {
		t.Errorf("output missing task input:\n%s", result)
	}
	if !contains(result, "note (string, optional)") {
		t.Errorf("output missing note input:\n%s", result)
	}
}

func TestFormatWorkflowShow_NoInputsSection(t *testing.T) {
	c := &compiler.CompiledWorkflow{
		Name:   "minimal",
		Inputs: nil,
	}
	result := FormatWorkflowShow(c)

	if contains(result, "Inputs:") {
		t.Errorf("should not show Inputs section when empty:\n%s", result)
	}
}

func TestFormatWorkflowShow_Limits(t *testing.T) {
	c := &compiler.CompiledWorkflow{
		Name: "limited",
		Limits: definition.Limits{
			MaxStepAttempts:    12,
			MaxDurationSeconds: 7200,
		},
	}
	result := FormatWorkflowShow(c)

	if !contains(result, "Limits:") {
		t.Error("output missing Limits section")
	}
	if !contains(result, "max_step_attempts:    12") {
		t.Errorf("output missing max_step_attempts:\n%s", result)
	}
	if !contains(result, "max_duration_seconds: 7200") {
		t.Errorf("output missing max_duration_seconds:\n%s", result)
	}
}

func TestFormatWorkflowShow_Steps(t *testing.T) {
	c := &compiler.CompiledWorkflow{
		Name: "steps-test",
		Steps: []definition.Step{
			{
				ID: "plan", Kind: "agent", Agent: "planner",
				Template:     "templates/plan.md",
				OutputSchema: "schemas/plan-v1.json",
				Context: []definition.ContextBinding{
					{From: "inputs.task", As: "task", MaxBytes: 12000},
				},
				OnFailure: "failure",
			},
			{
				ID: "verify", Kind: "evidence_gate", Verifier: "go-default",
				OutputSchema: "schemas/verify-v1.json",
			},
		},
	}
	result := FormatWorkflowShow(c)

	// Step header with count.
	if !contains(result, "Steps (2):") {
		t.Errorf("output missing step count:\n%s", result)
	}
	// Step line with kind and on_failure.
	if !contains(result, "plan [agent], on_failure=failure") {
		t.Errorf("output missing plan step line:\n%s", result)
	}
	if !contains(result, "  agent: planner") {
		t.Errorf("output missing agent:\n%s", result)
	}
	if !contains(result, "  template: templates/plan.md") {
		t.Errorf("output missing template:\n%s", result)
	}
	if !contains(result, "  output_schema: schemas/plan-v1.json") {
		t.Errorf("output missing output_schema:\n%s", result)
	}
	// Context bindings.
	if !contains(result, "    context:") {
		t.Errorf("output missing context header:\n%s", result)
	}
	if !contains(result, "      inputs.task → task, max_bytes=12000") {
		t.Errorf("output missing context binding:\n%s", result)
	}
	// Evidence gate step.
	if !contains(result, "verify [evidence_gate]") {
		t.Errorf("output missing verify step:\n%s", result)
	}
	if !contains(result, "  verifier: go-default") {
		t.Errorf("output missing verifier:\n%s", result)
	}
}

func TestFormatWorkflowShow_Transitions(t *testing.T) {
	c := &compiler.CompiledWorkflow{
		Name: "trans-test",
		Transitions: []definition.Transition{
			{
				From: "plan", To: "implement",
				Match: definition.MatchCriteria{Status: "succeeded"},
			},
			{
				From: "review", To: "implement",
				Match: definition.MatchCriteria{
					Status: "succeeded",
					Output: map[string]string{"verdict": "changes_requested"},
				},
				Loop: "review_repair", MaxIterations: 3,
			},
		},
	}
	result := FormatWorkflowShow(c)

	// Transition header.
	if !contains(result, "Transitions (2):") {
		t.Errorf("output missing transition count:\n%s", result)
	}
	// Simple transition.
	if !contains(result, "plan → implement [status=succeeded]") {
		t.Errorf("output missing simple transition:\n%s", result)
	}
	// Transition with output match and loop.
	if !contains(result, "review → implement [status=succeeded, verdict=changes_requested], loop=review_repair (max 3)") {
		t.Errorf("output missing loop transition:\n%s", result)
	}
}

func TestFormatWorkflowShow_Delivery(t *testing.T) {
	c := &compiler.CompiledWorkflow{
		Name: "del-test",
		Delivery: &definition.Delivery{
			Kind:     "pull_request",
			Mode:     "draft",
			Provider: "github",
			Base:     "main",
		},
	}
	result := FormatWorkflowShow(c)

	if !contains(result, "Delivery:") {
		t.Error("output missing Delivery section")
	}
	if !contains(result, "  kind:   pull_request") {
		t.Errorf("output missing delivery kind:\n%s", result)
	}
	if !contains(result, "  mode:   draft") {
		t.Errorf("output missing delivery mode:\n%s", result)
	}
	if !contains(result, "  provider: github") {
		t.Errorf("output missing delivery provider:\n%s", result)
	}
	if !contains(result, "  base:   main") {
		t.Errorf("output missing delivery base:\n%s", result)
	}
}

func TestFormatWorkflowShow_NoDelivery(t *testing.T) {
	c := &compiler.CompiledWorkflow{
		Name:     "no-del",
		Delivery: nil,
	}
	result := FormatWorkflowShow(c)

	if contains(result, "Delivery:") {
		t.Errorf("should not show Delivery section when nil:\n%s", result)
	}
}

func TestFormatWorkflowShow_ContextBindingNoMaxBytes(t *testing.T) {
	c := &compiler.CompiledWorkflow{
		Name: "cb-test",
		Steps: []definition.Step{
			{
				ID:   "step1",
				Kind: "agent",
				Context: []definition.ContextBinding{
					{From: "steps.plan.output", As: "plan"},
				},
			},
		},
	}
	result := FormatWorkflowShow(c)

	// Should not include max_bytes when it's 0.
	if !contains(result, "      steps.plan.output → plan") {
		t.Errorf("output missing context binding without max_bytes:\n%s", result)
	}
	// Should NOT include the ", max_bytes=0" suffix.
	if contains(result, "max_bytes=0") {
		t.Errorf("should not show max_bytes=0:\n%s", result)
	}
}

func TestFormatWorkflowValidate_Valid(t *testing.T) {
	c := &compiler.CompiledWorkflow{Name: "feature-delivery"}
	result := FormatWorkflowValidate("feature-delivery", c, nil)

	if result != "✓ feature-delivery: valid\n" {
		t.Errorf("unexpected valid output: %q", result)
	}
}

func TestFormatWorkflowValidate_Invalid(t *testing.T) {
	err := errors.New("compilation failed:\n  - step \"plan\": unknown agent")
	result := FormatWorkflowValidate("broken", nil, err)

	if !contains(result, "✗ broken: invalid") {
		t.Errorf("output missing invalid marker:\n%s", result)
	}
	if !contains(result, "compilation failed:") {
		t.Errorf("output missing error detail:\n%s", result)
	}
}

// newFullFixtureWorkflow builds a compiled workflow matching the
// valid-feature-delivery.toml fixture, used by integration-style tests.
func newFullFixtureWorkflow() *compiler.CompiledWorkflow {
	return &compiler.CompiledWorkflow{
		Name:        "feature-delivery",
		Description: "Plan, challenge plan, plan tests, review test plan, implement (with tests), review, validate tests, verify code, validate results, and request approval.",
		Version:     1,
		InitialStep: "plan",
		Inputs: map[string]definition.InputDef{
			"task": {Type: "string", Required: true, MaxBytes: 12000},
		},
		Limits: definition.Limits{
			MaxStepAttempts:    16,
			MaxDurationSeconds: 10800,
		},
		Steps:       fullFixtureSteps(),
		Transitions: fullFixtureTransitions(),
		Delivery:    fullFixtureDelivery(),
	}
}

func fullFixtureSteps() []definition.Step {
	return []definition.Step{
		{
			ID: "plan", Kind: "agent", Agent: "planner",
			Template: "templates/plan.md", OutputSchema: "schemas/plan-v1.json",
			Context: []definition.ContextBinding{
				{From: "inputs.task", As: "task", MaxBytes: 12000},
			},
			OnFailure: "failure",
		},
		{
			ID: "plan_review", Kind: "agent_gate", Agent: "reviewer",
			Template: "templates/review.md", OutputSchema: "schemas/review-v1.json",
			Context: []definition.ContextBinding{
				{From: "inputs.task", As: "task", MaxBytes: 12000},
				{From: "steps.plan.output", As: "plan", MaxBytes: 24000},
			},
			OnFailure: "failure",
		},
		{
			ID: "plan_tests", Kind: "agent", Agent: "go-engineer",
			Template: "templates/implement.md", OutputSchema: "schemas/change-summary-v1.json",
			Context: []definition.ContextBinding{
				{From: "inputs.task", As: "task", MaxBytes: 12000},
				{From: "steps.plan.output", As: "plan", MaxBytes: 24000},
			},
			OnFailure: "failure",
		},
		{
			ID: "test_plan_review", Kind: "agent_gate", Agent: "reviewer",
			Template: "templates/review.md", OutputSchema: "schemas/review-v1.json",
			Context: []definition.ContextBinding{
				{From: "inputs.task", As: "task", MaxBytes: 12000},
				{From: "steps.plan.output", As: "plan", MaxBytes: 24000},
				{From: "steps.plan_tests.output", As: "test_plan", MaxBytes: 16000},
			},
			OnFailure: "failure",
		},
		{
			ID: "implement", Kind: "agent", Agent: "go-engineer",
			Template: "templates/implement.md", OutputSchema: "schemas/change-summary-v1.json",
			Context: []definition.ContextBinding{
				{From: "inputs.task", As: "task", MaxBytes: 12000},
				{From: "steps.plan.output", As: "plan", MaxBytes: 24000},
				{From: "steps.plan_tests.output", As: "test_plan", MaxBytes: 16000},
			},
			OnFailure: "failure",
		},
		{
			ID: "review", Kind: "agent_gate", Agent: "reviewer",
			Template: "templates/review.md", OutputSchema: "schemas/review-v1.json",
			Context: []definition.ContextBinding{
				{From: "inputs.task", As: "task", MaxBytes: 12000},
				{From: "steps.implement.output", As: "implementation", MaxBytes: 16000},
				{From: "steps.plan_tests.output", As: "test_plan", MaxBytes: 16000},
			},
			OnFailure: "failure",
		},
		{
			ID: "test_validate", Kind: "evidence_gate", Verifier: "go-default",
			OutputSchema: "schemas/verification-v1.json", OnFailure: "failure",
		},
		{
			ID: "verify", Kind: "evidence_gate", Verifier: "go-default",
			OutputSchema: "schemas/verification-v1.json", OnFailure: "failure",
		},
		{
			ID: "code_validate", Kind: "evidence_gate", Verifier: "go-default",
			OutputSchema: "schemas/verification-v1.json", OnFailure: "failure",
		},
		{
			ID: "approval", Kind: "human_gate",
		},
	}
}

func fullFixtureTransitions() []definition.Transition {
	return []definition.Transition{
		{From: "plan", To: "plan_review", Match: definition.MatchCriteria{Status: "succeeded"}},
		{
			From: "plan_review", To: "plan_tests",
			Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}},
		},
		{
			From: "plan_review", To: "plan",
			Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "changes_requested"}},
			Loop:  "plan_review_repair", MaxIterations: -1,
		},
		{From: "plan_tests", To: "test_plan_review", Match: definition.MatchCriteria{Status: "succeeded"}},
		{
			From: "test_plan_review", To: "implement",
			Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}},
		},
		{
			From: "test_plan_review", To: "plan_tests",
			Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "changes_requested"}},
			Loop:  "test_plan_review_repair", MaxIterations: -1,
		},
		{From: "implement", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
		{
			From: "review", To: "test_validate",
			Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}},
		},
		{
			From: "review", To: "implement",
			Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "changes_requested"}},
			Loop:  "review_repair", MaxIterations: -1,
		},
		{
			From: "test_validate", To: "verify",
			Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}},
		},
		{
			From: "verify", To: "code_validate",
			Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}},
		},
		{
			From: "code_validate", To: "approval",
			Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}},
		},
		{
			From: "approval", To: "success",
			Match: definition.MatchCriteria{Status: "approved"},
		},
		{
			From: "approval", To: "failure",
			Match: definition.MatchCriteria{Status: "rejected"},
		},
	}
}

func fullFixtureDelivery() *definition.Delivery {
	return &definition.Delivery{
		Kind:     "pull_request",
		Mode:     "draft",
		Provider: "github",
		Base:     "main",
	}
}

// fullFixtureChecks lists the expected substrings that must appear in the
// rendered output of newFullFixtureWorkflow.
var fullFixtureChecks = []string{
	"Name:        feature-delivery",
	"Description: Plan, challenge plan, plan tests, review test plan, implement (with tests), review, validate tests, verify code, validate results, and request approval.",
	"Version:     1",
	"Initial:     plan",
	"Inputs:",
	"task (string, required, max 12000 bytes)",
	"Limits:",
	"max_step_attempts:    16",
	"max_duration_seconds: 10800",
	"Steps (10):",
	"plan [agent], on_failure=failure",
	"  agent: planner",
	"  template: templates/plan.md",
	"verify [evidence_gate]",
	"  verifier: go-default",
	"approval [human_gate]",
	"Transitions (14):",
	"plan → plan_review [status=succeeded]",
	"review → implement [status=succeeded, verdict=changes_requested], loop=review_repair (unlimited)",
	"Delivery:",
	"  kind:   pull_request",
	"  provider: github",
	"  base:   main",
}

func TestFormatWorkflowShow_FullFixture(t *testing.T) {
	c := newFullFixtureWorkflow()
	result := FormatWorkflowShow(c)

	// Spot-check key elements across all sections.
	for _, want := range fullFixtureChecks {
		t.Run(want, func(t *testing.T) {
			if !contains(result, want) {
				t.Errorf("full fixture output missing %q:\n%s", want, result)
			}
		})
	}

	// Should NOT contain max_bytes=0 anywhere.
	if contains(result, "max_bytes=0") {
		t.Errorf("should not contain max_bytes=0:\n%s", result)
	}
}

func TestFormatWorkflowShow_UnlimitedLoop(t *testing.T) {
	wf := definition.WorkflowFile{
		Name:        "unlimited-loop-show",
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
	cw, err := compiler.Compile(&wf)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	out := FormatWorkflowShow(cw)

	if !strings.Contains(out, "unlimited") {
		t.Fatalf("expected 'unlimited' in output for MaxIterations=-1:\n%s", out)
	}
	if strings.Contains(out, "max -1") {
		t.Fatalf("should not render 'max -1' for unlimited loop:\n%s", out)
	}
}

// contains is a helper that checks if substr appears in s.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
