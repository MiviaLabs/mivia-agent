package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// activatedSkillHandler creates a resource capability for exactly one nested
// skill invocation. It intentionally wraps only resource-bearing skills: the
// ordinary multi-step path remains unchanged and never sees this tool.
type activatedSkillHandler struct {
	definition skills.Definition
	template   subagents.MultiStepHandler
}

func (h *activatedSkillHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	activation, err := h.definition.Activate()
	if err != nil {
		return nil, err
	}
	defer activation.Close()
	registry := h.template.FullRegistry.Clone()
	if _, exists := registry.Get(tools.SkillResourceToolName); exists {
		return nil, fmt.Errorf("skill resource capability conflict")
	}
	registry.Register(tools.NewSkillResourceTool(func(ctx context.Context, id string) (string, string, error) {
		content, err := activation.Read(ctx, id)
		if err != nil {
			return "", "", err
		}
		return content.Text, "skill resource loaded: " + content.ID, nil
	}, activation.ToolKey(), activation.ToolResultBudget()))
	run := h.template
	run.FullRegistry = registry
	run.SystemPrompt = activation.Prompt(true)
	if description := strings.TrimSpace(h.definition.Description); description != "" {
		run.SystemPrompt = description + "\n\n" + run.SystemPrompt
	}
	return run.Invoke(ctx, req)
}

var _ runtime.Handler = (*activatedSkillHandler)(nil)
