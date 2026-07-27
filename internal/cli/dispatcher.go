package cli

import (
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// NewSessionDispatcher builds a runtime.Dispatcher for agent sessions.
// It registers tool handlers from the tool registry, one-shot and multi-step
// subagent handlers for delegation, optionally wires skills as subagent
// handlers, and adds delegation tools to the tool registry.
//
// If skillReg is non-nil, each skill is registered as a Subagent kind
// handler, making it callable by name from dispatch_tasks.
//
// The onEvent callback is forwarded to the multi-step subagent handler
// so subagent-internal events (tool calls, steps) are visible in the
// parent's TUI.
func NewSessionDispatcher(reg *tools.Registry, comp provider.Completer, model string, cfg config.SubagentConfig, skillReg ...*skills.Registry) *runtime.Dispatcher {
	policy := runtime.Policy{
		MaxDepth:  cfg.MaxDepth,
		MaxBudget: cfg.DefaultBudget,
	}
	d := runtime.NewToolDispatcher(reg, policy)

	// Register one-shot subagent handler (single LLM call, no tools).
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

	// Register multi-step subagent handler (full agent loop with tools).
	multiStepHandler := &subagents.MultiStepHandler{
		Completer:    comp,
		FullRegistry: reg,
		Model:        model,
		SystemPrompt: sysPrompt,
		MaxSteps:     cfg.NestedSteps,
		ToolTimeout:  time.Duration(cfg.DefaultTimeout) * time.Second,
		TotalTimeout: time.Duration(cfg.DefaultTimeout) * time.Second * 3,
		MaxTokens:    4096,
	}
	_ = d.Register(runtime.Subagent, "multi_step", multiStepHandler)

	// Register skills as subagent handlers (if provided).
	if len(skillReg) > 0 && skillReg[0] != nil {
		_ = skillReg[0].RegisterAllAsSubagents(d)
	}

	// Add delegation tools to the tool registry.
	reg.Register(&delegateTool{dispatcher: d, cfg: cfg})
	reg.Register(&dispatchTasksTool{dispatcher: d, cfg: cfg})

	return d
}

// OnEventForMultiStep wraps a parent OnEvent callback for forwarding
// subagent events. It prefixes subagent events with EventSubagentStart/End
// so the TUI can distinguish them from parent-level events.
func OnEventForMultiStep(parentOnEvent func(agent.Event)) func(agent.Event) {
	if parentOnEvent == nil {
		return nil
	}
	return func(e agent.Event) {
		// Forward subagent tool events with Subagent event kinds.
		switch e.Kind {
		case agent.EventToolStart:
			parentOnEvent(agent.Event{
				Kind:       agent.EventSubagentStart,
				ToolCallID: e.ToolCallID,
				Name:       e.Name,
				Detail:     e.Detail,
				Input:      e.Input,
			})
		case agent.EventToolEnd:
			parentOnEvent(agent.Event{
				Kind:       agent.EventSubagentEnd,
				ToolCallID: e.ToolCallID,
				Name:       e.Name,
				Detail:     e.Detail,
				Output:     e.Output,
			})
		}
	}
}
