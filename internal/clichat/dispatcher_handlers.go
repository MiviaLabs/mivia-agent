package clichat

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
)

func registerOneShotHandlers(d *runtime.Dispatcher, comp provider.Completer, model string, dial sessionDial, cfg config.SubagentConfig, maxContextTokens int, maxTokens *int, budget func() int) error {
	sysPrompt := cfg.SystemPrompt
	if sysPrompt == "" {
		sysPrompt = subagents.DefaultSubagentSystemPrompt
	}
	handler := &subagents.OneShotHandler{
		Completer: comp, Model: model, SystemPrompt: sysPrompt,
		Reasoning: dial.static, ReasoningFunc: dial.live,
		MaxContextTokens: maxContextTokens, MaxTokens: maxTokens,
		MaxContextTokensFunc: budget,
	}
	if err := d.Register(runtime.Subagent, cliorchestrate.HandlerDelegate, handler); err != nil {
		return fmt.Errorf("register delegate handler: %w", err)
	}
	if err := d.Register(runtime.Subagent, cliorchestrate.HandlerOneshot, handler); err != nil {
		return fmt.Errorf("register oneshot handler: %w", err)
	}
	return nil
}

func registerMultiStepHandler(d *runtime.Dispatcher, reg *tools.Registry, comp provider.Completer, model string, dial sessionDial, cfg config.SubagentConfig, budgets resultBudgets, maxContextTokens int, maxTokens *int, budget func() int, preparation contextmgr.PreparationManager, preparationInput contextmgr.PrepareInput, spool *remainder.Spool) error {
	multiSysPrompt := cfg.SystemPrompt
	if multiSysPrompt == "" {
		multiSysPrompt = subagents.MultiStepSystemPrompt
	}
	// The plain multi_step handler is a tool-bearing subagent, so it carries
	// the child-side messaging protocol block too. Oneshot/delegate are
	// deliberately excluded: they have no post_message tool to teach.
	multiSysPrompt = withMessagingProtocol(multiSysPrompt)
	// When DefaultTimeout is 0, leave ToolTimeout 0 (handler defaults per-tool
	// to the long-command ceiling). TotalTimeout stays 0 so req.Timeout from
	// the pool is the bound, including explicit per-task overrides.
	toolTO := time.Duration(cfg.DefaultTimeout) * time.Second
	totalTO := time.Duration(0)
	// Per-request LLM timeout for subagent turns. Falls back to the
	// effective orchestration default (12h) when DefaultTimeout is 0,
	// matching requestTimeout() in agent_task_handler.go; the http.Client
	// transport backstop still bounds any single provider call.
	requestTO := requestTimeout(cfg.DefaultRequestTimeoutSec, cfg.DefaultTimeout)
	h := &subagents.MultiStepHandler{
		Completer: comp, FullRegistry: reg, Dispatcher: d, Model: model,
		Reasoning: dial.static, ReasoningFunc: dial.live,
		SystemPrompt: multiSysPrompt, MaxSteps: cfg.NestedSteps,
		ToolTimeout: toolTO, TotalTimeout: totalTO, MaxTokens: cliorchestrate.DefaultMaxTokens, MaxContextTokens: maxContextTokens,
		MaxToolResultChars:        budgets.perCall,
		BatchResultBudgetBytes:    budgets.perBatch,
		RefOnlyTools:              budgets.refOnlyTools,
		RemainderSpool:            spool,
		SchemaRetryMax:            cfg.SchemaRetryMax,
		MaxContextTokensFunc:      budget,
		RequestTimeout:            requestTO,
		SteerWatchdog:             time.Duration(cfg.Messaging.SteerWatchdogSecondsResolved()) * time.Second,
		ContextPreparationManager: preparation,
		ContextPreparationInput:   preparationInput,
		// Forward nested tool/heartbeat events to the session TUI sink
		// registered by startAI via SetSubagentProgress.
		OnEvent: OnEventForMultiStep(emitSubagentProgress),
	}
	if maxTokens != nil && *maxTokens > 0 {
		h.MaxTokens = *maxTokens
	}
	if err := d.Register(runtime.Subagent, cliorchestrate.HandlerMultiStep, h); err != nil {
		return fmt.Errorf("register multi-step handler: %w", err)
	}
	return nil
}

func registerSkillHandlers(d *runtime.Dispatcher, reg *tools.Registry, comp provider.Completer, model string, dial sessionDial, cfg config.SubagentConfig, budgets resultBudgets, maxContextTokens int, maxTokens *int, budget func() int, skillReg *skills.Registry, scope AgentSkillScope, preparation contextmgr.PreparationManager, preparationInput contextmgr.PrepareInput, spool *remainder.Spool) error {
	if skillReg == nil {
		return nil
	}
	// Register each allowed skill as a multi-step subagent with tool access,
	// NOT as a one-shot Chat call. Skills like bug-audit need read_file, grep,
	// list_dir, run_command to function. The MultiStepHandler creates a
	// restricted tool registry (no delegation tools) and runs the skill
	// instructions as the system prompt. Disallowed skills are not registered
	// and gatedSkillHandler re-checks on every invoke (resume/retry).
	toolTO := time.Duration(cfg.DefaultTimeout) * time.Second
	// Per-request LLM timeout for skill subagent turns. Same fallback
	// logic as registerMultiStepHandler above.
	requestTO := requestTimeout(cfg.DefaultRequestTimeoutSec, cfg.DefaultTimeout)
	for _, skill := range skillReg.List() {
		if err := scope.CheckSkillDefinition(skill); err != nil {
			// Skip registration for skills the selected agent may not invoke.
			// Task-build paths also reject so the model gets a clear error.
			continue
		}
		sysPrompt := skill.Instructions
		if sysPrompt == "" {
			sysPrompt = "You are a helpful assistant executing a workspace skill task."
		}
		if skill.Description != "" {
			sysPrompt = skill.Description + "\n\n" + sysPrompt
		}
		// A registered skill subagent is a tool-bearing surface (post_message
		// included via ScopeSpawned + adoptSessionTools), so its final prompt
		// carries the shared child-side messaging protocol block exactly once,
		// like the routed-agent and plain multi_step surfaces. The resource-skill
		// variant replaces this prompt in activatedSkillHandler.Invoke and
		// re-applies the block there.
		sysPrompt = withMessagingProtocol(sysPrompt)
		h := &subagents.MultiStepHandler{
			Completer:                 comp,
			FullRegistry:              reg,
			Dispatcher:                d,
			Model:                     model,
			Reasoning:                 dial.static,
			ReasoningFunc:             dial.live,
			SystemPrompt:              sysPrompt,
			MaxSteps:                  cfg.NestedSteps,
			ToolTimeout:               toolTO,
			RequestTimeout:            requestTO,
			SteerWatchdog:             time.Duration(cfg.Messaging.SteerWatchdogSecondsResolved()) * time.Second,
			MaxTokens:                 cliorchestrate.DefaultMaxTokens,
			MaxContextTokens:          maxContextTokens,
			MaxContextTokensFunc:      budget,
			MaxToolResultChars:        budgets.perCall,
			BatchResultBudgetBytes:    budgets.perBatch,
			RefOnlyTools:              budgets.refOnlyTools,
			RemainderSpool:            spool,
			OutputSchema:              skill.OutputSchema,
			SchemaRetryMax:            cfg.SchemaRetryMax,
			ContextPreparationManager: preparation,
			ContextPreparationInput:   preparationInput,
			OnEvent:                   OnEventForMultiStep(emitSubagentProgress),
		}
		if maxTokens != nil && *maxTokens > 0 {
			h.MaxTokens = *maxTokens
		}
		var handler runtime.Handler = h
		if len(skill.Resources) > 0 {
			handler = &activatedSkillHandler{definition: skill, template: *h}
		}
		handler = &gatedSkillHandler{scope: scope, skill: skill, inner: handler}
		if err := d.Register(runtime.Subagent, skill.Name, handler); err != nil {
			return fmt.Errorf("register skill subagent %q: %w", skill.Name, err)
		}
		d.Allow(runtime.Subagent, skill.Name)
	}
	return nil
}

// gatedSkillHandler re-checks the selected agent's skill policy on every
// invocation so resume/retry cannot reuse a prior authority grant after an
// agent switch or model rebuild narrowed the allowlist.
type gatedSkillHandler struct {
	scope AgentSkillScope
	skill skills.Definition
	inner runtime.Handler
}

func (h *gatedSkillHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	if err := h.scope.CheckSkillDefinition(h.skill); err != nil {
		return nil, err
	}
	return h.inner.Invoke(ctx, req)
}

var _ runtime.Handler = (*gatedSkillHandler)(nil)

// OnEventForMultiStep wraps a parent OnEvent callback for forwarding
// subagent events. Tool start/end become SubagentStart/End; heartbeats,
// step progress, and the run-level Done signal are forwarded so long
// multi_step work is not silent and finished agents can be retired.
func OnEventForMultiStep(parentOnEvent func(agent.Event)) func(agent.Event) {
	if parentOnEvent == nil {
		return func(agent.Event) {}
	}
	return func(e agent.Event) {
		switch e.Kind {
		case agent.EventToolStart:
			parentOnEvent(agent.Event{
				Kind: agent.EventSubagentStart, ToolCallID: e.ToolCallID,
				Name: e.Name, Detail: e.Detail, Input: e.Input,
				Origin: e.Origin,
			})
		case agent.EventToolEnd:
			parentOnEvent(agent.Event{
				Kind: agent.EventSubagentEnd, ToolCallID: e.ToolCallID,
				Name: e.Name, Detail: e.Detail, Output: e.Output,
				Origin: e.Origin,
			})
		case agent.EventSubagentHeartbeat:
			// Feed the workflow join liveness watchdog.
			controller.NoteStepHeartbeat(e.Origin.TaskID)
			parentOnEvent(e)
		case agent.EventSubagentDone:
			parentOnEvent(e)
		case agent.EventStep, agent.EventHeartbeat:
			// Nested agent steps surface as heartbeats in the parent chrome;
			// wall-clock heartbeat ticks (model thinking, tool batches) get
			// the same treatment so long multi_step work is not silent.
			parentOnEvent(agent.Event{
				Kind:   agent.EventSubagentHeartbeat,
				Detail: e.Detail,
				Origin: e.Origin,
			})
		case agent.EventThinking:
			parentOnEvent(e)
		case agent.EventAssistant:
			parentOnEvent(e)
		}
	}
}
