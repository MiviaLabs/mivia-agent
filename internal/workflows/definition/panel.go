package definition

import "fmt"

// validatePanelStep checks that the panel field matches the step kind.
func validatePanelStep(s *Step) error {
	if s.Kind == "agent_panel" {
		if s.Panel == nil {
			return fmt.Errorf("step %q: panel is required for kind %q", s.ID, s.Kind)
		}
		return nil
	}
	if s.Panel != nil {
		return fmt.Errorf("step %q: panel is only valid for kind %q", s.ID, "agent_panel")
	}
	return nil
}
