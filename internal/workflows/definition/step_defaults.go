package definition

import "fmt"

// applyStepDefaults desugars wf.StepDefaults into wf.Steps and clears it.
// Called from ParseWorkflowTOML before validateWorkflowBasics, so downstream
// validators and the compiler see only fully expanded steps.
//
// Merge rules, in order, per step:
//  1. An empty step.Kind is filled from defaults.Kind.
//  2. When resolved kind is "agent" or "agent_panel": each empty scalar field
//     (Agent, Skill, Template, OutputSchema, OnFailure; MaxTurns when 0) is
//     filled from defaults, and unbound default context bindings are appended.
//     For "agent_panel" this fills only step top-level fields (synthesis
//     agent/skill/template/schema and shared context), never PanelMember
//     entries. Other kinds inherit only Kind.
//  3. Context: step bindings come first; each default binding whose "as" name
//     is not yet bound is appended. Step wins on "as" collision.
//
// MaxTurns cannot express "explicitly unlimited" once step_defaults sets a
// positive value: 0 is Step.MaxTurns' zero-value sentinel for "unlimited", so
// 0 is indistinguishable from "omitted, inherit default." A step requiring
// unlimited turns while step_defaults sets a bound must set an explicit bound,
// or step_defaults.max_turns must be omitted in favor of per-step max_turns.
func applyStepDefaults(wf *WorkflowFile) error {
	d := wf.StepDefaults
	if d == nil {
		return nil
	}
	if d.Kind != "" && !ValidStepKinds[d.Kind] {
		return fmt.Errorf("step_defaults: unknown kind %q", d.Kind)
	}
	if d.MaxTurns < 0 {
		return fmt.Errorf("step_defaults: max_turns must be >= 0 (got %d)", d.MaxTurns)
	}
	for i := range wf.Steps {
		applyStepDefaultsTo(&wf.Steps[i], d)
	}
	wf.StepDefaults = nil
	return nil
}

func applyStepDefaultsTo(s *Step, d *StepDefaults) {
	if s.Kind == "" {
		s.Kind = d.Kind
	}
	if s.Kind != "agent" && s.Kind != "agent_panel" {
		return
	}
	if s.Agent == "" {
		s.Agent = d.Agent
	}
	if s.Skill == "" {
		s.Skill = d.Skill
	}
	if s.Template == "" {
		s.Template = d.Template
	}
	if s.OutputSchema == "" {
		s.OutputSchema = d.OutputSchema
	}
	if s.OnFailure == "" {
		s.OnFailure = d.OnFailure
	}
	if s.MaxTurns == 0 {
		s.MaxTurns = d.MaxTurns
	}
	if len(d.Context) == 0 {
		return
	}
	bound := make(map[string]bool, len(s.Context))
	for _, c := range s.Context {
		bound[c.As] = true
	}
	for _, c := range d.Context {
		if !bound[c.As] {
			s.Context = append(s.Context, c)
		}
	}
}
