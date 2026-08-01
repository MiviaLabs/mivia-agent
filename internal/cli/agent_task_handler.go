package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// agentTaskHandler is registered once per immutable definition. Invoke creates
// fresh derived registry and loop state, so concurrent tasks cannot share a
// mutable prompt, tool registry, or skill policy.
type agentTaskHandler struct {
	definition agents.ResolvedAgent
	digest     string
	full       *tools.Registry
	dispatcher *runtime.Dispatcher
	opts       SessionDispatcherOpts
}

func registerAgentHandlers(d *runtime.Dispatcher, opts SessionDispatcherOpts) error {
	if opts.AgentRegistry == nil {
		return nil
	}
	for _, definition := range opts.AgentRegistry.List() {
		digest, err := definition.DefinitionDigest()
		if err != nil {
			return err
		}
		h := &agentTaskHandler{definition: definition, digest: digest, full: opts.Registry, dispatcher: d, opts: opts}
		if err := d.Register(runtime.Subagent, definition.Name, h); err != nil {
			return fmt.Errorf("register agent subagent %q: %w", definition.Name, err)
		}
	}
	return nil
}

func (h *agentTaskHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	if err := h.ValidateRequest(req); err != nil {
		return nil, err
	}
	systemPrompt := h.definition.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = h.opts.Config.SystemPrompt
	}
	if systemPrompt == "" {
		systemPrompt = subagents.MultiStepSystemPrompt
	}
	registry := tools.ScopedRegistry(h.full, tools.ScopeOptions{
		Mode: tools.ScopeSpawned, Allowlist: agents.AllowlistSet(h.definition.EffectiveTools),
	})
	if req.Skill != "" {
		if h.opts.SkillReg == nil {
			return nil, fmt.Errorf("agent %q may not invoke skill %q", h.definition.Name, req.Skill)
		}
		skill, ok := h.opts.SkillReg.Get(req.Skill)
		if !ok {
			return nil, fmt.Errorf("unknown skill %q", req.Skill)
		}
		if err := skillScopeFromAgent(&h.definition).checkSkill(skill.Name, skill.Tools); err != nil {
			return nil, err
		}
		systemPrompt = skill.Instructions
		if len(skill.Resources) > 0 {
			activation, err := skill.Activate()
			if err != nil {
				return nil, err
			}
			defer activation.Close()
			registry, err = injectSkillResourceTool(registry, activation)
			if err != nil {
				return nil, err
			}
			systemPrompt = activation.Prompt(true)
		}
		if strings.TrimSpace(skill.Description) != "" {
			systemPrompt = skill.Description + "\n\n" + systemPrompt
		}
	}
	model := h.opts.Model
	if h.definition.Model != "" {
		model = h.definition.Model
	}
	if h.definition.Model != "" && !modelInCatalog(h.opts.ModelCatalog, h.opts.ProviderName, model) {
		return nil, fmt.Errorf("agent %q model is not selectable for active provider", h.definition.Name)
	}
	instanceID := runtime.NewSessionID()
	generation := h.opts.ModelGeneration
	if h.opts.ModelGenerationFunc != nil {
		generation = h.opts.ModelGenerationFunc()
	}
	if generation == 0 {
		generation = 1
	}
	identity := routedIdentity(h.definition, instanceID, generation)
	maxSteps := h.opts.Config.NestedSteps
	if h.definition.MaxTurns != nil {
		maxSteps = *h.definition.MaxTurns
	}
	handler := &subagents.MultiStepHandler{
		Completer: h.opts.Completer, FullRegistry: registry,
		Dispatcher: h.dispatcher, Model: model, SystemPrompt: systemPrompt, MaxSteps: maxSteps,
		ToolTimeout: time.Duration(h.opts.Config.DefaultTimeout) * time.Second,
		MaxTokens:   defaultMaxTokens, MaxContextTokens: h.opts.MaxContextTokens,
		MaxContextTokensFunc: h.opts.Budget, MaxToolResultChars: h.opts.ToolResultCapBytes,
		OnEvent: OnEventForMultiStep(func(e agent.Event) {
			e.Identity = identity
			e.Origin.TaskID = instanceID
			emitSubagentProgress(e)
		}),
	}
	if h.opts.MaxTokens != nil && *h.opts.MaxTokens > 0 {
		handler.MaxTokens = *h.opts.MaxTokens
	}
	return handler.Invoke(ctx, req)
}

func modelInCatalog(catalog []config.ProviderModelGroup, providerName, model string) bool {
	if len(catalog) == 0 {
		return true
	}
	for _, group := range catalog {
		if group.Provider != providerName || !group.Selectable {
			continue
		}
		for _, profile := range group.Models {
			if profile.Name == model {
				return true
			}
		}
	}
	return false
}

func (h *agentTaskHandler) ValidateRequest(req runtime.Request) error {
	if req.Name != h.definition.Name || req.AgentName != h.definition.Name || req.AgentDigest != h.digest {
		return fmt.Errorf("agent routing snapshot mismatch for %q", h.definition.Name)
	}
	if req.Skill == "" {
		return nil
	}
	if h.opts.SkillReg == nil {
		return fmt.Errorf("agent %q may not invoke skill %q", h.definition.Name, req.Skill)
	}
	skill, ok := h.opts.SkillReg.Get(req.Skill)
	if !ok {
		return fmt.Errorf("unknown skill %q", req.Skill)
	}
	return skillScopeFromAgent(&h.definition).checkSkill(skill.Name, skill.Tools)
}

var _ runtime.Handler = (*agentTaskHandler)(nil)
