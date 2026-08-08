package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/jschema"
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

// requestTimeout returns the per-LLM-request timeout for subagent turns.
// configured wins when positive; otherwise fallback applies, and when both
// are 0 (unlimited), EffectiveTimeoutSec(fallback) is used - the same
// 12-hour default that bounds orchestration - so the model-facing
// timeout_seconds knob feeds every subagent request. The 15-minute
// http.Client transport backstop remains the real per-request ceiling, so
// a single hung provider call still cannot block the entire subagent.
// This mirrors registerMultiStepHandler in dispatcher.go.
func requestTimeout(configured, fallback int) time.Duration {
	if configured > 0 {
		return time.Duration(configured) * time.Second
	}
	return time.Duration(config.EffectiveTimeoutSec(fallback)) * time.Second
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
		h := newAgentTaskHandler(definition, digest, opts.authority(), d, opts)
		warnBindingOnce(h.bindingErr)
		if err := d.Register(runtime.Subagent, definition.Name, h); err != nil {
			return fmt.Errorf("register agent subagent %q: %w", definition.Name, err)
		}
	}
	return nil
}

// reportedBindingWarnings dedupes binding diagnostics across dispatcher
// rebuilds. registerAgentHandlers runs again on every /model switch, /agent
// switch, and session restore, so without this a single unusable agent file
// reprints its warning on each one - straight to stderr, which corrupts the
// rendered frame while the TUI owns the terminal.
var reportedBindingWarnings sync.Map

func warnBindingOnce(err error) {
	if err == nil {
		return
	}
	if _, seen := reportedBindingWarnings.LoadOrStore(err.Error(), struct{}{}); seen {
		return
	}
	fmt.Fprintln(os.Stderr, "warning:", err)
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
	binding, err := h.validateRequest(req)
	if err != nil {
		return nil, err
	}
	systemPrompt, registry, closeAct, err := h.prepareInvokeSurface(req)
	if err != nil {
		return nil, err
	}
	defer closeAct()
	handler := h.newMultiStepHandler(binding, registry, systemPrompt, req)
	// The agent's own wall-clock ceiling layers over the caller's task timeout
	// rather than replacing it, so unlimited turns still cannot produce an
	// unbounded run and a generous agent policy cannot loosen a tight task
	// deadline. Exhaustion carries a typed cause.
	ctx, cancel, ceilingCause := binding.withWallClock(ctx, h.definition.Name)
	defer cancel()
	out, err := handler.Invoke(ctx, req)
	// Identity, not errors.Is: an ancestor that breached its own ceiling
	// propagates that cause to this context, and only the invocation that
	// minted this cause may claim the breach. The underlying error is kept -
	// a provider failure racing the deadline still carries its own detail.
	if err != nil && ceilingCause != nil && context.Cause(ctx) == ceilingCause {
		return out, fmt.Errorf("%w (last error: %v)", ceilingCause, err)
	}
	return out, err
}

func (h *agentTaskHandler) prepareInvokeSurface(req runtime.Request) (string, *tools.Registry, func(), error) {
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
	// Baseline messaging: inject post_message after allowlist filter unless
	// the agent opted out via disallowed_tools = ["post_message"] (plan 53.02).
	// tools_remove alone does not opt out — resolve maps messaging opt-out
	// through DisallowedTools when agents list disallowed_tools.
	disallowed := messagingDisallowed(h.definition.DisallowedTools)
	injectBaselineMessaging(h.full, registry, h.opts.Config, disallowed)
	noop := func() {}
	// The resolved output schema must outrank skill report-shape text: skill
	// instructions replace the agent system prompt, and without the schema in
	// the system prompt a skill that demands its own report format wins over
	// the workflow step's output contract (observed as schema_violation runs).
	// The user-turn appendix stays too, but the system prompt is authoritative.
	schemaBlock := schemaSystemAppendix(h.resolveOutputSchema(req))
	if req.Skill == "" {
		return withMessagingProtocol(systemPrompt) + schemaBlock, registry, noop, nil
	}
	scoped, prompt, closeActivation, err := h.activateSkill(req.Skill, registry)
	if err != nil {
		return "", nil, noop, err
	}
	injectBaselineMessaging(h.full, scoped, h.opts.Config, disallowed)
	// The skill's instructions replace the agent prompt, so the protocol block
	// is appended to the skill-activated prompt instead of the resolved one.
	// This keeps the child-side messaging contract in-context exactly once.
	return withMessagingProtocol(prompt) + schemaBlock, scoped, closeActivation, nil
}

// resolveOutputSchema returns the output schema that will actually be enforced
// for this invocation: task-level overrides skill, skill overrides the agent
// definition. Only nil means "no schema": an empty object {} is a real schema
// and must be enforced as declared.
func (h *agentTaskHandler) resolveOutputSchema(req runtime.Request) map[string]any {
	out := req.OutputSchema
	if out == nil && req.Skill != "" && h.opts.SkillReg != nil {
		if sk, ok := h.opts.SkillReg.Get(req.Skill); ok && sk.OutputSchema != nil {
			out = sk.OutputSchema
		}
	}
	if out == nil {
		out = h.definition.OutputSchema
	}
	return out
}

// schemaSystemAppendix is the deterministic system-prompt block stating the
// output contract. It mirrors the user-turn PromptAppendix wording so both
// surfaces demand the same shape. Both renderers show the model-facing
// contract (meta-keywords stripped, never-echo instruction, compact example),
// never the raw schema document: a verbatim document invites the model to
// echo it back as its answer. A nil schema (no contract) emits nothing:
// json.Marshal of a nil map would otherwise produce a bogus "null" block.
func schemaSystemAppendix(schema map[string]any) string {
	if schema == nil {
		return ""
	}
	contract := jschema.ModelSchemaContract(schema)
	if contract == "" {
		return ""
	}
	return "\n\nReturn ONLY valid JSON matching this required output schema (no prose, no markdown fences):\n" + contract
}

func messagingDisallowed(names []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, name := range names {
		out[name] = struct{}{}
	}
	return out
}

// withMessagingProtocol appends the shared child-side messaging protocol block
// to a tool-bearing subagent's system prompt. Every surface that can call
// post_message must carry the kinds/question/answer semantics, the no_answer
// contract, and the <parent-message> anti-injection rule, so the block is
// appended exactly once per invocation after prompt resolution and before the
// prompt reaches the loop.
func withMessagingProtocol(prompt string) string {
	return prompt + "\n\n" + subagents.MessagingProtocolPrompt
}

func (h *agentTaskHandler) newMultiStepHandler(binding agentBinding, registry *tools.Registry, systemPrompt string, req runtime.Request) *subagents.MultiStepHandler {
	limits := h.effectiveWorkLimits(binding, req)
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
	if limits.MaxTurns > 0 && (maxSteps <= 0 || limits.MaxTurns < maxSteps) {
		maxSteps = limits.MaxTurns
	}
	maxTokens := binding.maxTokens
	if limit := limits.MaxOutputPerCall; limit > 0 && (maxTokens <= 0 || maxTokens > limit) {
		maxTokens = limit
	}
	outSchema := h.resolveOutputSchema(req)
	// Steer watchdog (plan 54 §4.5): the [subagents.messaging]
	// steer_watchdog_seconds knob bounds how long a pending steer may wait
	// before the loop soft-interrupts the in-flight LLM call. nil means the
	// 300s default; an explicit 0 disables the watchdog (unbounded).
	return &subagents.MultiStepHandler{
		Completer: binding.completer, FullRegistry: registry,
		Dispatcher: h.dispatcher, Model: binding.model, Reasoning: binding.reasoning,
		ReasoningFunc: binding.effectiveReasoning,
		SystemPrompt:  systemPrompt, MaxSteps: maxSteps,
		WorkLimits: limits, DisableProviderReplay: req.DisableProviderReplay,
		ToolTimeout: time.Duration(h.opts.Config.DefaultTimeout) * time.Second,
		MaxTokens:   maxTokens, MaxContextTokens: binding.contextBudget(),
		MaxContextTokensFunc: binding.contextBudget, MaxToolResultChars: h.opts.ToolResultCapBytes,
		BatchResultBudgetBytes: h.opts.BatchResultBudgetBytes,
		RemainderSpool:         RemainderSpoolFromRegistry(registry),
		OutputSchema:           outSchema, SchemaRetryMax: h.opts.Config.SchemaRetryMax,
		RequestTimeout:            requestTimeout(h.opts.Config.DefaultRequestTimeoutSec, h.opts.Config.DefaultTimeout),
		SteerWatchdog:             time.Duration(h.opts.Config.Messaging.SteerWatchdogSecondsResolved()) * time.Second,
		ContextPreparationManager: h.opts.ContextPreparationManager,
		ContextPreparationInput:   h.opts.ContextPreparationInput,
		OnEvent: OnEventForMultiStep(func(e agent.Event) {
			e.Identity = identity
			e.Origin.TaskID = instanceID
			emitSubagentProgress(e)
		}),
	}
}

// effectiveWorkLimits takes the tightest positive limit from every source
// available to a nested agent invocation.
func (h *agentTaskHandler) effectiveWorkLimits(binding agentBinding, req runtime.Request) runtime.WorkLimits {
	agentLimits := runtime.WorkLimits{}
	if h.definition.MaxTurns != nil {
		agentLimits.MaxTurns = *h.definition.MaxTurns
	}
	if h.definition.MaxTokens != nil {
		agentLimits.MaxOutputPerCall = *h.definition.MaxTokens
	}
	modelLimits := runtime.WorkLimits{MaxOutputPerCall: binding.maxTokens}
	return runtime.LowestPositiveWorkLimits(agentLimits, h.opts.WorkLimits, modelLimits, req.WorkLimits)
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
	if snapshots := h.opts.WorkflowSkillSnapshots; snapshots != nil {
		pinned, ok := snapshots[name]
		if !ok {
			return nil, "", noop, fmt.Errorf("workflow skill %q is not admitted", name)
		}
		current, err := workflowSkillBytes(skill)
		if err != nil {
			return nil, "", noop, err
		}
		if pinned.Digest != digestBytes(current) || string(pinned.Bytes) != string(current) {
			return nil, "", noop, fmt.Errorf("workflow skill %q changed after admission", name)
		}
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

func (h *agentTaskHandler) bindingForRequest(req runtime.Request) (agentBinding, error) {
	if h.bindingErr != nil {
		return agentBinding{}, h.bindingErr
	}
	if (req.ProviderName == "") != (req.Model == "") {
		return agentBinding{}, fmt.Errorf("agent %q has an incomplete provider/model binding", h.definition.Name)
	}
	if req.ProviderName == "" && req.Model == "" {
		// Legacy snapshots predate binding metadata. They retain the old
		// session-following behavior, while all new snapshots carry a pair.
		return h.binding, nil
	}
	if declaredBinding(h.definition) {
		if req.ProviderName != h.binding.providerName || req.Model != h.binding.model {
			return agentBinding{}, fmt.Errorf("agent %q persisted provider/model %s/%s does not match the current definition binding %s/%s", h.definition.Name, req.ProviderName, req.Model, h.binding.providerName, h.binding.model)
		}
		return h.binding, nil
	}
	if req.ProviderName == h.binding.providerName && req.Model == h.binding.model {
		// The current registration already authorized the live session pair.
		// This keeps test/minimal sessions without a catalog compatible while a
		// changed session still takes the strict pinned re-authorization path.
		return h.binding, nil
	}
	return resolvePinnedAgentBinding(h.definition, h.opts, req.ProviderName, req.Model)
}

func (h *agentTaskHandler) validateRequest(req runtime.Request) (agentBinding, error) {
	if req.Name != h.definition.Name || req.AgentName != h.definition.Name {
		return agentBinding{}, fmt.Errorf("agent routing snapshot mismatch for %q", h.definition.Name)
	}
	if req.AgentDigest != h.digest {
		// Naming both digests matters on the resume path: the same mismatch is
		// produced by an edited agent file and by a mivia version that changed
		// the definition schema, and the operator cannot tell which without it.
		return agentBinding{}, fmt.Errorf("agent routing snapshot mismatch for %q: work was recorded against definition %s but this session resolved %s (the agent definition or the mivia version changed since the run started)",
			h.definition.Name, req.AgentDigest, h.digest)
	}
	binding, err := h.bindingForRequest(req)
	if err != nil {
		return agentBinding{}, err
	}
	if req.Skill == "" {
		return binding, nil
	}
	if h.opts.SkillReg == nil {
		return agentBinding{}, fmt.Errorf("agent %q may not invoke skill %q", h.definition.Name, req.Skill)
	}
	skill, ok := h.opts.SkillReg.Get(req.Skill)
	if !ok {
		return agentBinding{}, fmt.Errorf("unknown skill %q", req.Skill)
	}
	if err := skillScopeFromAgentAndRegistry(&h.definition, h.full).checkSkillDefinition(skill); err != nil {
		return agentBinding{}, err
	}
	return binding, nil
}

func (h *agentTaskHandler) ValidateRequest(req runtime.Request) error {
	_, err := h.validateRequest(req)
	return err
}

var _ runtime.Handler = (*agentTaskHandler)(nil)
