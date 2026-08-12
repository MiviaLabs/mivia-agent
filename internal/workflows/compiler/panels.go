package compiler

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

type providerModel struct {
	provider string
	model    string
}

// validatePanels validates static agent_panel member declarations.
func validatePanels(wf *definition.WorkflowFile) error {
	for _, step := range wf.Steps {
		if step.Kind != "agent_panel" {
			if step.Panel != nil {
				return fmt.Errorf("step %q: panel is only valid for kind %q", step.ID, "agent_panel")
			}
			continue
		}
		if step.Panel == nil {
			return fmt.Errorf("step %q: panel is required for kind %q", step.ID, step.Kind)
		}
		if strings.TrimSpace(step.Agent) == "" {
			return fmt.Errorf("step %q: agent is required for kind %q", step.ID, step.Kind)
		}
		if step.Panel.FailurePolicy != "require_all" {
			return fmt.Errorf("step %q: panel failure_policy must be \"require_all\"", step.ID)
		}
		if !step.Panel.RequireDistinctBindings {
			return fmt.Errorf("step %q: panel require_distinct_bindings must be true", step.ID)
		}
		if len(step.Panel.Members) < 2 || len(step.Panel.Members) > 4 {
			return fmt.Errorf("step %q: panel must have between 2 and 4 members", step.ID)
		}
		if err := validatePanelMembers(step.ID, step.Panel.Members); err != nil {
			return err
		}
	}
	return nil
}

func validatePanelMembers(stepID string, members []definition.PanelMember) error {
	memberIDs := make(map[string]struct{}, len(members))
	bindings := make(map[providerModel]struct{}, len(members))
	for index, member := range members {
		if strings.TrimSpace(member.ID) == "" {
			return fmt.Errorf("step %q: panel member[%d]: id is required", stepID, index)
		}
		if member.ID == "synthesis" {
			return fmt.Errorf("step %q: panel member id %q is reserved", stepID, member.ID)
		}
		// Wave 5's controller.sourceKeyDigest concatenates MemberID and
		// FindingID with 0x00/0x1e separators. A member ID carrying one of
		// those bytes (or any other control byte) could make two different
		// canonical source keys collide onto the same digest. A finding ID
		// gets this same check at decode time (host-decoded, model-authored);
		// a member ID is workflow-definition-authored, so it is checked here,
		// at compile time, once.
		if hasControlByte(member.ID) {
			return fmt.Errorf("step %q: panel member id %q contains a control character", stepID, member.ID)
		}
		if _, exists := memberIDs[member.ID]; exists {
			return fmt.Errorf("step %q: panel has duplicate member id %q", stepID, member.ID)
		}
		memberIDs[member.ID] = struct{}{}
		if strings.TrimSpace(member.Agent) == "" {
			return fmt.Errorf("step %q: panel member %q: agent is required", stepID, member.ID)
		}
		if strings.TrimSpace(member.Provider) == "" || strings.TrimSpace(member.Model) == "" {
			return fmt.Errorf("step %q: panel member %q: provider and model must both be set", stepID, member.ID)
		}
		if strings.TrimSpace(member.Skill) == "" {
			return fmt.Errorf("step %q: panel member %q: skill is required", stepID, member.ID)
		}
		if strings.TrimSpace(member.Template) == "" {
			return fmt.Errorf("step %q: panel member %q: template is required", stepID, member.ID)
		}
		if strings.TrimSpace(member.OutputSchema) == "" {
			return fmt.Errorf("step %q: panel member %q: output_schema is required", stepID, member.ID)
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
			return fmt.Errorf("step %q: panel has duplicate provider/model binding %q/%q", stepID, member.Provider, member.Model)
		}
		bindings[binding] = struct{}{}
	}
	return nil
}

// hasControlByte reports whether s contains a C0 control byte or DEL.
func hasControlByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}
