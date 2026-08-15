package definition

import "fmt"

// applyStepDefaults desugars wf.StepDefaults into wf.Steps and clears it.
// Called from ParseWorkflowTOML before validateWorkflowBasics, so every
// downstream validator and the compiler see only fully expanded steps.
//
// Merge rules, in order, per step:
//  1. An empty step.Kind is filled from defaults.Kind.
//  2. Only when the step's resolved kind is "agent": each empty scalar field
//     (Agent, Skill, Template, OutputSchema, OnFailure; MaxTurns when 0) is
//     filled from defaults. Non-agent kinds (agent_panel, agent_gate,
//     evidence_gate, human_gate) inherit nothing but Kind.
//  3. Context: the step's own bindings come first; each default binding
//     whose "as" name the step does not already bind is appended. The step
//     wins on an "as" collision.
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
	if s.Kind != "agent" {
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
