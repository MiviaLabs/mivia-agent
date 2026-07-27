package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// NewSessionDispatcher builds a runtime.Dispatcher for agent sessions.
// It registers tool handlers from the tool registry, one-shot subagent
// handlers for delegation, and adds delegation tools (delegate, dispatch_tasks)
// to the tool registry so the model can call them.
func NewSessionDispatcher(reg *tools.Registry, comp provider.Completer, model string, cfg config.SubagentConfig) *runtime.Dispatcher {
	policy := runtime.Policy{
		MaxDepth:  cfg.MaxDepth,
		MaxBudget: cfg.DefaultBudget,
	}
	d := runtime.NewToolDispatcher(reg, policy)

	// Register one-shot subagent handlers for delegation.
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

	// Add delegation tools to the tool registry.
	// These tools implement tools.Tool and wrap the Pool + Dispatcher.
	reg.Register(&delegateTool{dispatcher: d, cfg: cfg})
	reg.Register(&dispatchTasksTool{dispatcher: d, cfg: cfg})

	return d
}
