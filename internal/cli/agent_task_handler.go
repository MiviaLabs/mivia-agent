package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/agents"
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
	// binding is the immutable resolved execution target, computed once at
	// registration. bindingErr holds a resolution failure so one unusable
	// definition fails on invoke instead of refusing to build the session.
	binding    agentBinding
	bindingErr error
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
		h := newAgentTaskHandler(definition, digest, opts.Registry, d, opts)
		if h.bindingErr != nil {
			fmt.Fprintln(os.Stderr, "warning:", h.bindingErr)
		}
		if err := d.Register(runtime.Subagent, definition.Name, h); err != nil {
			return fmt.Errorf("register agent subagent %q: %w", definition.Name, err)
		}
	}
	return nil
}

// newAgentTaskHandler resolves an agent's execution binding once, at
// registration. Resolving eagerly moves a mistyped provider or model from
// twenty minutes into a run to session startup; the error is carried rather
// than returned so one unusable definition cannot stop the session from
// starting. The resolved binding is immutable afterwards, which is what makes
// it safe for concurrent Invokes to share this handler.
func newAgentTaskHandler(definition agents.ResolvedAgent, digest string, full *tools.Registry, d *runtime.Dispatcher, opts SessionDispatcherOpts) *agentTaskHandler {
	h := &agentTaskHandler{definition: definition, digest: digest, full: full, dispatcher: d, opts: opts}
	h.binding, h.bindingErr = resolveAgentBinding(definition, opts)
	return h
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
		scoped, prompt, closeActivation, err := h.activateSkill(req.Skill, registry)
		if err != nil {
			return nil, err
		}
		defer closeActivation()
		registry, systemPrompt = scoped, prompt
	}
	if h.bindingErr != nil {
		return nil, h.bindingErr
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
	// The routed agent runs on its own resolved binding: its provider's
	// completer, its model, and a prompt budget clamped to the tighter of the
	// live session budget and the routed model's own context window. Handing
	// it the session budget would replace local pruning with provider-side
	// context-overflow errors whenever the routed model is smaller.
	handler := &subagents.MultiStepHandler{
		Completer: h.binding.completer, FullRegistry: registry,
		Dispatcher: h.dispatcher, Model: h.binding.model, SystemPrompt: systemPrompt, MaxSteps: maxSteps,
		ToolTimeout: time.Duration(h.opts.Config.DefaultTimeout) * time.Second,
		MaxTokens:   h.binding.maxTokens, MaxContextTokens: h.binding.contextBudget(),
		MaxContextTokensFunc: h.binding.contextBudget, MaxToolResultChars: h.opts.ToolResultCapBytes,
		OnEvent: OnEventForMultiStep(func(e agent.Event) {
			e.Identity = identity
			e.Origin.TaskID = instanceID
			emitSubagentProgress(e)
		}),
	}
	// The agent's own wall-clock ceiling layers over the caller's task timeout
	// rather than replacing it, so unlimited turns still cannot produce an
	// unbounded run and a generous agent policy cannot loosen a tight task
	// deadline. Exhaustion carries a typed cause.
	ctx, cancel := h.binding.withWallClock(ctx)
	defer cancel()
	out, err := handler.Invoke(ctx, req)
	if err != nil && errors.Is(context.Cause(ctx), ErrAgentWallClockExceeded) {
		return out, fmt.Errorf("agent %q stopped after its %s wall-clock ceiling: %w",
			h.definition.Name, h.binding.wallClock, ErrAgentWallClockExceeded)
	}
	return out, err
}

// activateSkill checks that this agent may invoke the named skill and derives
// the skill's prompt and (when it declares resources) a registry carrying the
// scoped resource reader. The returned closer releases the activation and must
// be deferred by the caller for the lifetime of the run.
func (h *agentTaskHandler) activateSkill(name string, registry *tools.Registry) (*tools.Registry, string, func(), error) {
	noop := func() {}
	if h.opts.SkillReg == nil {
		return nil, "", noop, fmt.Errorf("agent %q may not invoke skill %q", h.definition.Name, name)
	}
	skill, ok := h.opts.SkillReg.Get(name)
	if !ok {
		return nil, "", noop, fmt.Errorf("unknown skill %q", name)
	}
	if err := skillScopeFromAgentAndRegistry(&h.definition, h.full).checkSkillDefinition(skill); err != nil {
		return nil, "", noop, err
	}
	systemPrompt := skill.Instructions
	closeActivation := noop
	if len(skill.Resources) > 0 {
		activation, err := skill.Activate()
		if err != nil {
			return nil, "", noop, err
		}
		closeActivation = func() { activation.Close() }
		registry, err = injectSkillResourceTool(registry, activation)
		if err != nil {
			closeActivation()
			return nil, "", noop, err
		}
		systemPrompt = activation.Prompt(true)
	}
	if strings.TrimSpace(skill.Description) != "" {
		systemPrompt = skill.Description + "\n\n" + systemPrompt
	}
	return registry, systemPrompt, closeActivation, nil
}

func (h *agentTaskHandler) ValidateRequest(req runtime.Request) error {
	if req.Name != h.definition.Name || req.AgentName != h.definition.Name {
		return fmt.Errorf("agent routing snapshot mismatch for %q", h.definition.Name)
	}
	if req.AgentDigest != h.digest {
		// Naming both digests matters on the resume path: the same mismatch is
		// produced by an edited agent file and by a mivia version that changed
		// the definition schema, and the operator cannot tell which without it.
		return fmt.Errorf("agent routing snapshot mismatch for %q: work was recorded against definition %s but this session resolved %s (the agent definition or the mivia version changed since the run started)",
			h.definition.Name, req.AgentDigest, h.digest)
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
	return skillScopeFromAgentAndRegistry(&h.definition, h.full).checkSkillDefinition(skill)
}

var _ runtime.Handler = (*agentTaskHandler)(nil)
