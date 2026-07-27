package cli

import (
	"fmt"
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
func NewSessionDispatcher(reg *tools.Registry, comp provider.Completer, model string, cfg config.SubagentConfig, skillReg ...*skills.Registry) (*runtime.Dispatcher, error) {
	if reg == nil || comp == nil {
		return nil, fmt.Errorf("nil session dispatcher dependency")
	}
	policy := runtime.Policy{
		MaxDepth:  cfg.MaxDepth,
		MaxBudget: cfg.DefaultBudget,
	}
	d, err := runtime.NewToolDispatcher(reg, policy)
	if err != nil {
		return nil, fmt.Errorf("create tool dispatcher: %w", err)
	}

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
	if err := d.Register(runtime.Subagent, "delegate", handler); err != nil {
		return nil, fmt.Errorf("register delegate handler: %w", err)
	}
	if err := d.Register(runtime.Subagent, "oneshot", handler); err != nil {
		return nil, fmt.Errorf("register oneshot handler: %w", err)
	}

	// Register multi-step subagent handler (full agent loop with tools).
	multiStepHandler := &subagents.MultiStepHandler{
		Completer:    comp,
		FullRegistry: reg,
		Dispatcher:   d,
		Model:        model,
		SystemPrompt: sysPrompt,
		MaxSteps:     cfg.NestedSteps,
		ToolTimeout:  time.Duration(cfg.DefaultTimeout) * time.Second,
		TotalTimeout: time.Duration(cfg.DefaultTimeout) * time.Second * 3,
		MaxTokens:    4096,
	}
	if err := d.Register(runtime.Subagent, "multi_step", multiStepHandler); err != nil {
		return nil, fmt.Errorf("register multi-step handler: %w", err)
	}

	// Register skills as subagent handlers (if provided).
	if len(skillReg) > 0 && skillReg[0] != nil {
		if err := skillReg[0].RegisterAll(d); err != nil {
			return nil, fmt.Errorf("register skill tools: %w", err)
		}
		if err := skillReg[0].RegisterAllAsSubagents(d); err != nil {
			return nil, fmt.Errorf("register skills: %w", err)
		}
	}

	// Add delegation tools to both the model-visible registry and the already
	// constructed dispatcher. NewToolDispatcher snapshots the registry, so
	// registering only in reg would advertise tools that fail as unknown at
	// invocation time.
	delegate := &delegateTool{dispatcher: d, cfg: cfg}
	dispatchTasks := &dispatchTasksTool{dispatcher: d, cfg: cfg}
	if len(skillReg) > 0 {
		dispatchTasks.skillReg = skillReg[0]
	}
	if err := registerSessionTool(d, reg, delegate); err != nil {
		return nil, err
	}
	if err := registerSessionTool(d, reg, dispatchTasks); err != nil {
		return nil, err
	}

	return d, nil
}

func registerSessionTool(d *runtime.Dispatcher, reg *tools.Registry, tool tools.Tool) error {
	if _, exists := reg.Get(tool.Name()); exists {
		return fmt.Errorf("session tool %q already registered", tool.Name())
	}
	if err := d.RegisterTool(reg, tool); err != nil {
		return fmt.Errorf("register session tool %q: %w", tool.Name(), err)
	}
	reg.Register(tool)
	return nil
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
