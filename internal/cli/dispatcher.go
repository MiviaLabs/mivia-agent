package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// SessionDispatcherOpts carries every input the session dispatcher needs.
// Repo and Budget are optional; their absence selects the legacy defaults
// (open a SQLite store from Config, no live budget provider).
type SessionDispatcherOpts struct {
	Registry        *tools.Registry
	Completer       provider.Completer
	Model           string
	ProviderName    string
	ModelGeneration uint64
	// ModelGenerationFunc is evaluated when a routed task starts. Candidate
	// dispatchers are built before a binding is published, so a fixed
	// generation here can be stale after a concurrent switch.
	ModelGenerationFunc func() uint64
	ModelCatalog        []config.ProviderModelGroup
	// CompleterFactory builds a completer bound to one provider. It is required
	// before a routed agent may execute on a provider other than the session's;
	// when it is absent such an agent fails closed rather than silently
	// downgrading onto the session completer. Completers are provider-scoped
	// (the model travels per request), so the model argument is advisory.
	CompleterFactory   func(providerName, model string) (provider.Completer, error)
	Config             config.SubagentConfig
	ToolResultCapBytes int
	// WorkspaceRoot is the directory lifecycle hooks execute in. Empty means
	// no hooks are wired, which is what every non-chat caller wants.
	WorkspaceRoot string

	// Repo, if set, is used as-is and its lifetime is caller-owned.
	// If nil, the constructor opens a store from Config (with the
	// memory-backend fallback) and owns its Close via dispatcher.OnClose.
	Repo ledger.LedgerRepository

	// MaxContextTokens / MaxTokens configure the nested subagent handlers.
	// Zero values mean "unset" (handler defaults apply).
	MaxContextTokens int
	MaxTokens        *int

	// Budget, if non-nil, is the live session budget provider read by nested
	// handlers when invoked (so /budget applies without rebuilding).
	Budget func() int

	// SharedSQLite is a caller-owned SQLite pointer. When supplied, the ledger
	// adapter borrows it and the dispatcher never closes it.
	SharedSQLite *storage.SQLite

	// ContextPreparationManager is a preparation-only capability for nested
	// loops. The dispatcher never receives a checkpoint publisher or store.
	ContextPreparationManager contextmgr.PreparationManager
	ContextPreparationInput   contextmgr.PrepareInput

	// SkillReg, if non-nil, registers each skill as a Subagent handler.
	SkillReg *skills.Registry

	// SkillScope is the immutable per-instance skill policy for the selected
	// root agent (plan 06). Zero value allows all skills (no agent selected).
	SkillScope agentSkillScope

	// AgentRegistry is the caller-authorized immutable catalogue whose names
	// are the only task routing targets.
	AgentRegistry *agents.AgentRegistry
}

// NewSessionDispatcher builds a runtime.Dispatcher for agent sessions from a
// single options struct. This is the only public constructor.
//
// It registers tool handlers from the tool registry, one-shot and multi-step
// subagent handlers for delegation, optionally wires skills as subagent
// handlers, and adds delegation tools to the tool registry.
//
// ToolResultCapBytes is the [tools] max_tool_result_bytes ceiling applied to
// every nested sub-agent loop (multi_step and skill handlers); 0 = uncapped.
func NewSessionDispatcher(opts SessionDispatcherOpts) (*runtime.Dispatcher, error) {
	repo := opts.Repo
	var ownedStore *ledger.StorageLedgerRepository
	if repo == nil {
		if opts.SharedSQLite != nil {
			repo = ledger.NewBorrowedStorageLedgerRepository(opts.SharedSQLite)
		} else {
			repo, ownedStore = openDurableLedgerRepo(opts.Config, os.Stderr)
		}
	}
	d, err := newSessionDispatcherCore(opts, repo)
	if err != nil {
		if ownedStore != nil {
			_ = ownedStore.Close()
		}
		return nil, err
	}
	if ownedStore != nil {
		d.OnClose(func() { _ = ownedStore.Close() })
	}
	initCoordinator(d, opts.Config, repo)
	return d, nil
}

// newSessionDispatcherMinimal is a test-only convenience for the common
// no-budget, no-repo, no-maxTokens case. Production must use NewSessionDispatcher
// with an explicit SessionDispatcherOpts.
func newSessionDispatcherMinimal(reg *tools.Registry, comp provider.Completer, model string, cfg config.SubagentConfig, toolResultCapBytes int, skillReg ...*skills.Registry) (*runtime.Dispatcher, error) {
	var skillsReg *skills.Registry
	if len(skillReg) > 0 {
		skillsReg = skillReg[0]
	}
	return NewSessionDispatcher(SessionDispatcherOpts{
		Registry:           reg,
		Completer:          comp,
		Model:              model,
		Config:             cfg,
		ToolResultCapBytes: toolResultCapBytes,
		SkillReg:           skillsReg,
	})
}

// newSessionDispatcherCore registers handlers on a dispatcher for the given
// options and repository. The caller owns repo lifetime and initCoordinator.
func newSessionDispatcherCore(opts SessionDispatcherOpts, repo ledger.LedgerRepository) (*runtime.Dispatcher, error) {
	if opts.Registry == nil || opts.Completer == nil {
		return nil, fmt.Errorf("nil session dispatcher dependency")
	}
	preHook, postHook := hookPolicyFuncs(opts.WorkspaceRoot)
	d, err := runtime.NewToolDispatcher(opts.Registry, runtime.Policy{
		MaxDepth:  opts.Config.MaxDepth,
		MaxBudget: opts.Config.DefaultBudget,
		// On Policy, not on the Dispatcher: Policy is copied to derived
		// dispatchers, so a scoped subagent inherits the gate. A PreToolUse
		// gate a subagent escapes is not a gate.
		PreInvokeHook:  preHook,
		PostInvokeHook: postHook,
	})
	if err != nil {
		return nil, fmt.Errorf("create tool dispatcher: %w", err)
	}
	maxTokens := sessionOutputCeiling(opts)
	if err := registerOneShotHandlers(d, opts.Completer, opts.Model, opts.Config, opts.MaxContextTokens, maxTokens, opts.Budget); err != nil {
		return nil, err
	}
	if err := registerMultiStepHandler(d, opts.Registry, opts.Completer, opts.Model, opts.Config, opts.ToolResultCapBytes, opts.MaxContextTokens, maxTokens, opts.Budget, opts.ContextPreparationManager, opts.ContextPreparationInput); err != nil {
		return nil, err
	}
	if err := registerAgentHandlers(d, opts); err != nil {
		return nil, err
	}
	if err := registerSkillHandlers(d, opts.Registry, opts.Completer, opts.Model, opts.Config, opts.ToolResultCapBytes, opts.MaxContextTokens, maxTokens, opts.Budget, opts.SkillReg, opts.SkillScope, opts.ContextPreparationManager, opts.ContextPreparationInput); err != nil {
		return nil, err
	}
	if err := registerDelegationTools(d, opts.Registry, opts.Config, opts.SkillReg, repo, opts.AgentRegistry, opts.ProviderName, opts.Model); err != nil {
		return nil, err
	}
	if err := registerOrchestrationTools(d, opts.Registry, opts.Config, repo, opts.SkillReg, opts.AgentRegistry, opts.ProviderName, opts.Model); err != nil {
		return nil, err
	}
	if err := registerLedgerTools(d, opts.Registry, repo, opts.ToolResultCapBytes); err != nil {
		return nil, err
	}
	return d, nil
}

func sessionOutputCeiling(opts SessionDispatcherOpts) *int {
	profile, ok := selectableModel(opts.ModelCatalog, opts.ProviderName, opts.Model)
	if !ok {
		return opts.MaxTokens
	}
	return config.EffectiveOutputTokens(profile, opts.MaxTokens)
}

func registerOneShotHandlers(d *runtime.Dispatcher, comp provider.Completer, model string, cfg config.SubagentConfig, maxContextTokens int, maxTokens *int, budget func() int) error {
	sysPrompt := cfg.SystemPrompt
	if sysPrompt == "" {
		sysPrompt = subagents.DefaultSubagentSystemPrompt
	}
	handler := &subagents.OneShotHandler{
		Completer: comp, Model: model, SystemPrompt: sysPrompt,
		MaxContextTokens: maxContextTokens, MaxTokens: maxTokens,
		MaxContextTokensFunc: budget,
	}
	if err := d.Register(runtime.Subagent, handlerDelegate, handler); err != nil {
		return fmt.Errorf("register delegate handler: %w", err)
	}
	if err := d.Register(runtime.Subagent, handlerOneshot, handler); err != nil {
		return fmt.Errorf("register oneshot handler: %w", err)
	}
	return nil
}

func registerMultiStepHandler(d *runtime.Dispatcher, reg *tools.Registry, comp provider.Completer, model string, cfg config.SubagentConfig, toolResultCapBytes, maxContextTokens int, maxTokens *int, budget func() int, preparation contextmgr.PreparationManager, preparationInput contextmgr.PrepareInput) error {
	multiSysPrompt := cfg.SystemPrompt
	if multiSysPrompt == "" {
		multiSysPrompt = subagents.MultiStepSystemPrompt
	}
	// When DefaultTimeout is 0, leave ToolTimeout 0 (handler defaults per-tool
	// to the long-command ceiling). TotalTimeout stays 0 so req.Timeout from
	// the pool is the bound, including explicit per-task overrides.
	toolTO := time.Duration(cfg.DefaultTimeout) * time.Second
	totalTO := time.Duration(0)
	// Per-request LLM timeout for subagent turns. Falls back to 5 minutes
	// when DefaultTimeout is 0, preventing indefinite hangs on a hung
	// provider (the root session gets DefaultRequestTimeout = 15m, but
	// subagent calls are simpler and should not need that long).
	requestTO := requestTimeout(cfg.DefaultTimeout)
	h := &subagents.MultiStepHandler{
		Completer: comp, FullRegistry: reg, Dispatcher: d, Model: model,
		SystemPrompt: multiSysPrompt, MaxSteps: cfg.NestedSteps,
		ToolTimeout: toolTO, TotalTimeout: totalTO, MaxTokens: defaultMaxTokens, MaxContextTokens: maxContextTokens,
		MaxToolResultChars:        toolResultCapBytes,
		MaxContextTokensFunc:      budget,
		RequestTimeout:            requestTO,
		ContextPreparationManager: preparation,
		ContextPreparationInput:   preparationInput,
		// Forward nested tool/heartbeat events to the session TUI sink
		// registered by startAI via SetSubagentProgress.
		OnEvent: OnEventForMultiStep(emitSubagentProgress),
	}
	if maxTokens != nil && *maxTokens > 0 {
		h.MaxTokens = *maxTokens
	}
	if err := d.Register(runtime.Subagent, handlerMultiStep, h); err != nil {
		return fmt.Errorf("register multi-step handler: %w", err)
	}
	return nil
}

func registerSkillHandlers(d *runtime.Dispatcher, reg *tools.Registry, comp provider.Completer, model string, cfg config.SubagentConfig, toolResultCapBytes, maxContextTokens int, maxTokens *int, budget func() int, skillReg *skills.Registry, scope agentSkillScope, preparation contextmgr.PreparationManager, preparationInput contextmgr.PrepareInput) error {
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
	requestTO := requestTimeout(cfg.DefaultTimeout)
	for _, skill := range skillReg.List() {
		if err := scope.checkSkillDefinition(skill); err != nil {
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
		h := &subagents.MultiStepHandler{
			Completer:                 comp,
			FullRegistry:              reg,
			Dispatcher:                d,
			Model:                     model,
			SystemPrompt:              sysPrompt,
			MaxSteps:                  cfg.NestedSteps,
			ToolTimeout:               toolTO,
			RequestTimeout:            requestTO,
			MaxTokens:                 defaultMaxTokens,
			MaxContextTokens:          maxContextTokens,
			MaxContextTokensFunc:      budget,
			MaxToolResultChars:        toolResultCapBytes,
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

func registerDelegationTools(d *runtime.Dispatcher, reg *tools.Registry, cfg config.SubagentConfig, skillReg *skills.Registry, repo ledger.LedgerRepository, agentReg *agents.AgentRegistry, providerName, model string) error {
	// Register on both the model-visible registry and the dispatcher snapshot.
	delegate := &delegateTool{dispatcher: d, cfg: cfg, repo: repo}
	dispatchTasks := &dispatchTasksTool{dispatcher: d, cfg: cfg, skillReg: skillReg, repo: repo, agentReg: agentReg, providerName: providerName, model: model}
	if err := registerSessionTool(d, reg, delegate); err != nil {
		return err
	}
	return registerSessionTool(d, reg, dispatchTasks)
}

// gatedSkillHandler re-checks the selected agent's skill policy on every
// invocation so resume/retry cannot reuse a prior authority grant after an
// agent switch or model rebuild narrowed the allowlist.
type gatedSkillHandler struct {
	scope agentSkillScope
	skill skills.Definition
	inner runtime.Handler
}

func (h *gatedSkillHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	if err := h.scope.checkSkillDefinition(h.skill); err != nil {
		return nil, err
	}
	return h.inner.Invoke(ctx, req)
}

var _ runtime.Handler = (*gatedSkillHandler)(nil)

// registerSessionTool is the single entry point for session-control tools.
// Sub-agent registries exclude such tools by the tools.PrivilegedTool marker,
// which is a runtime assertion - so an unmarked control tool would silently
// become callable from a nested agent. Rejecting it here fails at startup
// instead.
func registerSessionTool(d *runtime.Dispatcher, reg *tools.Registry, tool tools.Tool) error {
	if _, privileged := tool.(tools.PrivilegedTool); !privileged {
		return fmt.Errorf("session tool %q must implement tools.PrivilegedTool", tool.Name())
	}
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
// subagent events. Tool start/end become SubagentStart/End; heartbeats and
// step progress are forwarded so long multi_step work is not silent.
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
			parentOnEvent(e)
		case agent.EventStep:
			// Nested agent steps surface as heartbeats in the parent chrome.
			parentOnEvent(agent.Event{
				Kind:   agent.EventSubagentHeartbeat,
				Detail: e.Detail,
				Origin: e.Origin,
			})
		}
	}
}
