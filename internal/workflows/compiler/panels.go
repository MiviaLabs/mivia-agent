package compiler

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/textutil"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

type providerModel struct {
	provider string
	model    string
}

// validatePanels validates static agent_panel member declarations.
func validatePanels(wf *definition.WorkflowFile) []string {
	var errs []string
	for _, step := range wf.Steps {
		if step.Kind != "agent_panel" {
			if step.Panel != nil {
				errs = append(errs, fmt.Sprintf("step %q: panel is only valid for kind %q", step.ID, "agent_panel"))
			}
			continue
		}
		if step.Panel == nil {
			errs = append(errs, fmt.Sprintf("step %q: panel is required for kind %q", step.ID, step.Kind))
			continue
		}
		if strings.TrimSpace(step.Agent) == "" {
			errs = append(errs, fmt.Sprintf("step %q: agent is required for kind %q", step.ID, step.Kind))
		}
		if step.Panel.FailurePolicy != definition.PanelFailurePolicyRequireAll && step.Panel.FailurePolicy != definition.PanelFailurePolicyAllowPartial {
			errs = append(errs, fmt.Sprintf("step %q: panel failure_policy must be \"require_all\" or \"allow_partial\"", step.ID))
		}
		if !step.Panel.RequireDistinctBindings {
			errs = append(errs, fmt.Sprintf("step %q: panel require_distinct_bindings must be true", step.ID))
		}
		if len(step.Panel.Members) < 2 || len(step.Panel.Members) > 4 {
			errs = append(errs, fmt.Sprintf("step %q: panel must have between 2 and 4 members", step.ID))
		}
		errs = append(errs, validatePanelMembers(step.ID, step.Panel.Members)...)
	}
	return errs
}

func validatePanelMembers(stepID string, members []definition.PanelMember) []string {
	var errs []string
	memberIDs := make(map[string]struct{}, len(members))
	bindings := make(map[providerModel]struct{}, len(members))
	for index, member := range members {
		if strings.TrimSpace(member.ID) == "" {
			errs = append(errs, fmt.Sprintf("step %q: panel member[%d]: id is required", stepID, index))
		}
		if member.ID == "synthesis" {
			errs = append(errs, fmt.Sprintf("step %q: panel member id %q is reserved", stepID, member.ID))
		}
		// Wave 5's controller.sourceKeyDigest concatenates MemberID and
		// FindingID with 0x00/0x1e separators. A member ID carrying one of
		// those bytes (or any other control byte) could make two different
		// canonical source keys collide onto the same digest. A finding ID
		// gets this same check at decode time (host-decoded, model-authored);
		// a member ID is workflow-definition-authored, so it is checked here,
		// at compile time, once.
		if textutil.HasControlByte(member.ID) {
			errs = append(errs, fmt.Sprintf("step %q: panel member id %q contains a control character", stepID, member.ID))
		}
		if _, exists := memberIDs[member.ID]; exists {
			errs = append(errs, fmt.Sprintf("step %q: panel has duplicate member id %q", stepID, member.ID))
		}
		memberIDs[member.ID] = struct{}{}
		if strings.TrimSpace(member.Agent) == "" {
			errs = append(errs, fmt.Sprintf("step %q: panel member %q: agent is required", stepID, member.ID))
		}
		if strings.TrimSpace(member.Provider) == "" || strings.TrimSpace(member.Model) == "" {
			errs = append(errs, fmt.Sprintf("step %q: panel member %q: provider and model must both be set", stepID, member.ID))
		}
		if strings.TrimSpace(member.Skill) == "" {
			errs = append(errs, fmt.Sprintf("step %q: panel member %q: skill is required", stepID, member.ID))
		}
		if strings.TrimSpace(member.Template) == "" {
			errs = append(errs, fmt.Sprintf("step %q: panel member %q: template is required", stepID, member.ID))
		}
		if strings.TrimSpace(member.OutputSchema) == "" {
			errs = append(errs, fmt.Sprintf("step %q: panel member %q: output_schema is required", stepID, member.ID))
		}
		// Normalize the binding before comparing: the provider registry resolves
		// names via strings.ToLower(strings.TrimSpace(name)) (providerregistry)
		// and binding resolution lowercases the provider, so a case or
		// whitespace variant of an already-bound provider is the same provider.
		// Without this, 'DeepSeek' vs 'deepseek' bypasses require_distinct_bindings.
		// The model must NOT be lowercased: models are case-sensitive
		// (config.NormalizeModelName only trims, selectableModel matches
		// profile.Name == model exactly, and the model string reaches the
		// provider API unmodified), so 'GLM-5.2' vs 'glm-5.2' are distinct
		// bindings and both must be admitted.
		binding := providerModel{
			provider: strings.ToLower(strings.TrimSpace(member.Provider)),
			model:    strings.TrimSpace(member.Model),
		}
		if _, exists := bindings[binding]; exists {
			errs = append(errs, fmt.Sprintf("step %q: panel has duplicate provider/model binding %q/%q", stepID, member.Provider, member.Model))
		}
		bindings[binding] = struct{}{}
	}
	return errs
}
