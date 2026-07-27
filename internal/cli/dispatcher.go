package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// NewSessionDispatcher builds a runtime.Dispatcher for agent sessions.
// It registers tool handlers from the tool registry and a one-shot subagent
// handler for delegation on top.
func NewSessionDispatcher(reg *tools.Registry, comp provider.Completer, model string, cfg config.SubagentConfig) *runtime.Dispatcher {
	policy := runtime.Policy{
		MaxDepth:  cfg.MaxDepth,
		MaxBudget: cfg.DefaultBudget,
	}
	d := runtime.NewToolDispatcher(reg, policy)

	sysPrompt := cfg.SystemPrompt
	if sysPrompt == "" {
		sysPrompt = subagents.DefaultSubagentSystemPrompt
	}
	handler := &subagents.OneShotHandler{
		Completer:    comp,
		Model:        model,
		SystemPrompt: sysPrompt,
	}
	_ = d.Register(runtime.Subagent, "delegate", handler)
	_ = d.Register(runtime.Subagent, "oneshot", handler)

	return d
}
