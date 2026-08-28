package clichat

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
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
	registry, err := InjectSkillResourceTool(h.template.FullRegistry, activation)
	if err != nil {
		return nil, err
	}
	run := h.template
	run.FullRegistry = registry
	run.SystemPrompt = activation.Prompt(true)
	if description := strings.TrimSpace(h.definition.Description); description != "" {
		run.SystemPrompt = description + "\n\n" + run.SystemPrompt
	}
	// The resource-skill surface is tool-bearing too (post_message included via
	// ScopeSpawned + adoptSessionTools), so the final prompt must carry the
	// shared child-side messaging protocol block exactly once, and the
	// full report-budget block after the prompt replacement, mirroring
	// withMessagingProtocol there.
	run.SystemPrompt = withReportBudget(withMessagingProtocol(run.SystemPrompt), false)
	return run.Invoke(ctx, req)
}

var _ runtime.Handler = (*activatedSkillHandler)(nil)
