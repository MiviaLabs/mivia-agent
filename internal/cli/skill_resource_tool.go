package cli

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// injectSkillResourceTool clones the given registry, checks for an existing
// skill resource tool (returning an error on conflict), registers a new
// scoped reader bound to the activation, and returns the augmented clone.
// The caller is responsible for calling activation.Close() when done.
func injectSkillResourceTool(
	registry *tools.Registry,
	activation *skills.SkillActivation,
) (*tools.Registry, error) {
	clone := registry.Clone()
	if _, exists := clone.Get(tools.SkillResourceToolName); exists {
		return nil, fmt.Errorf("skill resource capability conflict")
	}
	clone.Register(tools.NewSkillResourceTool(
		func(ctx context.Context, id string) (string, string, error) {
			content, err := activation.Read(ctx, id)
			if err != nil {
				return "", "", err
			}
			return content.Text, "skill resource loaded: " + content.ID, nil
		},
		activation.ToolKey(),
		activation.ToolResultBudget(),
	))
	return clone, nil
}
