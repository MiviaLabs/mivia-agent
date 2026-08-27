package clichat

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/jschema"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
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
// A positive configured value (default_request_timeout_seconds) is the
// deadline; otherwise DefaultSubagentRequestTimeoutSec (1800s, 30 minutes)
// applies. That 1800s is the per-request context deadline. The 15-minute
// http.Client.Timeout stays the hard per-attempt wire wall, so a single hung
// provider call still cannot block the entire subagent.
//
// Behavior change: the old code fed default_timeout_seconds into every
// subagent request through EffectiveTimeoutSec, so an unset
// default_request_timeout_seconds inherited the 12-hour orchestration
// default. Operators who relied on that must now set
// default_request_timeout_seconds explicitly. This mirrors
// registerMultiStepHandler in dispatcher_handlers.go.
func requestTimeout(configured int) time.Duration {
	if configured > 0 {
		return time.Duration(configured) * time.Second
	}
	return config.DefaultSubagentRequestTimeoutSec * time.Second
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
		h := newAgentTaskHandler(definition, digest, opts.Authority(), d, opts)
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
	h.binding, h.bindingErr = cliagents.ResolveAgentBinding(definition, opts)
	return h
}

func (h *agentTaskHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	binding, err := h.validateRequest(req)
	if err != nil {
		return nil, err
	}
	systemPrompt, memoryContext, registry, closeAct, err := h.prepareInvokeSurface(req)
	if err != nil {
		return nil, err
	}
	defer closeAct()
	handler := h.newMultiStepHandler(binding, registry, systemPrompt, memoryContext, req)
	// The agent's own wall-clock ceiling layers over the caller's task timeout
	// rather than replacing it, so unlimited turns still cannot produce an
	// unbounded run and a generous agent policy cannot loosen a tight task
	// deadline. Exhaustion carries a typed cause.
	ctx, cancel, ceilingCause := binding.WithWallClock(ctx, h.definition.Name)
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

// prepareInvokeSurface returns the system prompt, the rendered core-memory
// context frame (empty when there is none), the scoped registry, and an
// activation closer. The memory frame is returned separately - never composed
// into the prompt - so the subagent loop can deliver it as its own user-role
// message right after the system message. That keeps the system prompt
// byte-stable across memory promotions, so a memory change no longer
// invalidates the provider's cached prompt prefix (system + tools); it only
// changes the message at index 1, which is stable within one invocation
// anyway.
func (h *agentTaskHandler) prepareInvokeSurface(req runtime.Request) (string, string, *tools.Registry, func(), error) {
	if h.opts.EnsureMCPTools != nil {
		if err := h.opts.EnsureMCPTools(h.definition.EffectiveMCPServers); err != nil {
			return "", "", nil, func() {}, fmt.Errorf("MCP tools: %w", err)
		}
	}
	systemPrompt := h.definition.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = h.opts.Config.SystemPrompt
	}
	if systemPrompt == "" {
		systemPrompt = subagents.MultiStepSystemPrompt
	}
	registry := tools.ScopedRegistry(h.full, tools.ScopeOptions{
		Mode: tools.ScopeSpawned, Allowlist: agents.AllowlistSet(cliagents.AuthorizedAgentTools(&h.definition, h.full)),
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
	// The core-memory block rides in its own message (D1c's ordering
	// concern - keeping the messaging-protocol/schema tail closest to the
	// prompt's end - is moot now that the block never enters the prompt).
	memoryContext := chat.MemoryContextContent(cliagents.CoreMemoryBlockForOpts(h.opts))
	if req.Skill == "" {
		return withMessagingProtocol(systemPrompt) + schemaBlock, memoryContext, registry, noop, nil
	}
	scoped, prompt, closeActivation, err := h.activateSkill(req.Skill, registry)
	if err != nil {
		return "", "", nil, noop, err
	}
	injectBaselineMessaging(h.full, scoped, h.opts.Config, disallowed)
	// The skill's instructions replace the agent prompt, so the protocol block
	// is appended to the skill-activated prompt instead of the resolved one.
	// This keeps the child-side messaging contract in-context exactly once.
	return withMessagingProtocol(prompt) + schemaBlock, memoryContext, scoped, closeActivation, nil
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
	return jschema.EnvelopeAppendixBody(contract)
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

func (h *agentTaskHandler) newMultiStepHandler(binding agentBinding, registry *tools.Registry, systemPrompt, memoryContext string, req runtime.Request) *subagents.MultiStepHandler {
	limits := h.effectiveWorkLimits(binding, req)
	instanceID := runtime.NewSessionID()
	generation := h.opts.ModelGeneration
	if h.opts.ModelGenerationFunc != nil {
		generation = h.opts.ModelGenerationFunc()
	}
	if generation == 0 {
		generation = 1
	}
	identity := cliagents.RoutedIdentity(h.definition, instanceID, generation)
	maxSteps := h.opts.Config.NestedSteps
	if h.definition.MaxTurns != nil {
		maxSteps = *h.definition.MaxTurns
	}
	if limits.MaxTurns > 0 && (maxSteps <= 0 || limits.MaxTurns < maxSteps) {
		maxSteps = limits.MaxTurns
	}
	maxTokens := binding.MaxTokens
	if limit := limits.MaxOutputPerCall; limit > 0 && (maxTokens <= 0 || maxTokens > limit) {
		maxTokens = limit
	}
	outSchema := h.resolveOutputSchema(req)
	// Steer watchdog (plan 54 §4.5): the [subagents.messaging]
	// steer_watchdog_seconds knob bounds how long a pending steer may wait
	// before the loop soft-interrupts the in-flight LLM call. nil means the
	// 300s default; an explicit 0 disables the watchdog (unbounded).
	return &subagents.MultiStepHandler{
		Completer: binding.Completer, FullRegistry: registry,
		Dispatcher: h.dispatcher, Model: binding.Model, Reasoning: binding.Reasoning,
		ReasoningFunc: binding.EffectiveReasoning,
		SystemPrompt:  systemPrompt, MemoryContext: memoryContext, MaxSteps: maxSteps,
		WorkLimits: limits, DisableProviderReplay: req.DisableProviderReplay,
		ToolTimeout:    time.Duration(h.opts.Config.DefaultTimeout) * time.Second,
		ToolRunTimeout: h.opts.ToolRunTimeout,
		MaxTokens:      maxTokens, MaxContextTokens: binding.ContextBudget(),
		MaxContextTokensFunc: binding.ContextBudget, MaxToolResultChars: h.opts.ToolResultCapBytes,
		BatchResultBudgetBytes: h.opts.BatchResultBudgetBytes,
		RefOnlyTools:           h.opts.RefOnlyTools,
		RemainderSpool:         cliagents.RemainderSpoolFromRegistryVar(registry),
		OutputSchema:           outSchema, SchemaRetryMax: h.opts.Config.SchemaRetryMax,
		RequestTimeout:            requestTimeout(h.opts.Config.DefaultRequestTimeoutSec),
		SteerWatchdog:             time.Duration(h.opts.Config.Messaging.SteerWatchdogSecondsResolved()) * time.Second,
		ContextPreparationManager: h.opts.ContextPreparationManager,
		ContextPreparationInput:   h.opts.ContextPreparationInput,
		OnEvent:                   OnEventForMultiStep(stampRoutedOrigin(identity, instanceID, emitSubagentProgress)),
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
	modelLimits := runtime.WorkLimits{MaxOutputPerCall: binding.MaxTokens}
	return runtime.LowestPositiveWorkLimits(agentLimits, h.opts.WorkLimits, modelLimits, req.WorkLimits)
}

// activateSkill checks that this agent may invoke the named skill and derives
// the skill's prompt and (when it declares resources) a registry carrying the
// scoped resource reader. The returned closer releases the activation and must
// be deferred by the caller for the lifetime of the run.
//
// When the invocation runs under a workflow admission (WorkflowSkillSnapshots
// is set), the EXECUTED content comes from the pinned admission bytes, never
// from the live skill source: the definition is hydrated from the pin and its
// resources are served from the pinned snapshots in memory (R1). The registry
// definition is still resolved, because admission (is this skill pinned for
// this run?) and authorization (may this agent invoke it?) are live host-side
// policy checks; only the executed bytes are pinned.
func workflowSkillResumeErrorf(name, detail string) error {
	return fmt.Errorf("workflow skill %q %s; recover with: restore the skill to its admitted content, pass --accept-skill-change, or start a fresh run", name, detail)
}

func (h *agentTaskHandler) activateSkill(name string, registry *tools.Registry) (*tools.Registry, string, func(), error) {
	noop := func() {}
	if h.opts.SkillReg == nil {
		if h.opts.WorkflowSkillSnapshots != nil {
			return nil, "", noop, workflowSkillResumeErrorf(name, fmt.Sprintf("is not authorized for agent %q", h.definition.Name))
		}
		return nil, "", noop, fmt.Errorf("agent %q may not invoke skill %q", h.definition.Name, name)
	}
	skill, ok := h.opts.SkillReg.Get(name)
	if !ok {
		if h.opts.WorkflowSkillSnapshots != nil {
			return nil, "", noop, workflowSkillResumeErrorf(name, "is not declared")
		}
		return nil, "", noop, fmt.Errorf("unknown skill %q", name)
	}
	exec := skill
	var pinnedResources []skills.ResourceSnapshot
	pinnedRun := false
	if snapshots := h.opts.WorkflowSkillSnapshots; snapshots != nil {
		pinned, ok := snapshots[name]
		if !ok {
			return nil, "", noop, workflowSkillResumeErrorf(name, "is not admitted")
		}
		hydrated, resources, err := cliworkflow.HydrateWorkflowSkillSnapshot(name, pinned)
		if err != nil {
			return nil, "", noop, err
		}
		exec, pinnedResources, pinnedRun = hydrated, resources, true
	}
	if err := cliagents.SkillScopeFromAgentAndRegistry(&h.definition, h.full).CheckSkillDefinition(skill); err != nil {
		return nil, "", noop, err
	}
	systemPrompt := exec.Instructions
	closeActivation := noop
	if len(exec.Resources) > 0 {
		var activation *skills.SkillActivation
		var err error
		if pinnedRun {
			activation, err = skills.ActivateSnapshot(exec, pinnedResources)
		} else {
			activation, err = skill.Activate()
		}
		if err != nil {
			return nil, "", noop, err
		}
		closeActivation = func() { activation.Close() }
		registry, err = InjectSkillResourceTool(registry, activation)
		if err != nil {
			closeActivation()
			return nil, "", noop, err
		}
		systemPrompt = activation.Prompt(true)
	}
	if strings.TrimSpace(exec.Description) != "" {
		systemPrompt = exec.Description + "\n\n" + systemPrompt
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
	if cliagents.DeclaredBinding(h.definition) {
		if req.ProviderName != h.binding.ProviderName || req.Model != h.binding.Model {
			return agentBinding{}, fmt.Errorf("agent %q persisted provider/model %s/%s does not match the current definition binding %s/%s", h.definition.Name, req.ProviderName, req.Model, h.binding.ProviderName, h.binding.Model)
		}
		return h.binding, nil
	}
	if req.ProviderName == h.binding.ProviderName && req.Model == h.binding.Model {
		// The current registration already authorized the live session pair.
		// This keeps test/minimal sessions without a catalog compatible while a
		// changed session still takes the strict pinned re-authorization path.
		return h.binding, nil
	}
	return cliagents.ResolvePinnedAgentBinding(h.definition, h.opts, req.ProviderName, req.Model)
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
		if h.opts.WorkflowSkillSnapshots != nil {
			return agentBinding{}, workflowSkillResumeErrorf(req.Skill, fmt.Sprintf("is not authorized for agent %q", h.definition.Name))
		}
		return agentBinding{}, fmt.Errorf("agent %q may not invoke skill %q", h.definition.Name, req.Skill)
	}
	skill, ok := h.opts.SkillReg.Get(req.Skill)
	if !ok {
		if h.opts.WorkflowSkillSnapshots != nil {
			return agentBinding{}, workflowSkillResumeErrorf(req.Skill, "is not declared")
		}
		return agentBinding{}, fmt.Errorf("unknown skill %q", req.Skill)
	}
	if err := cliagents.SkillScopeFromAgentAndRegistry(&h.definition, h.full).CheckSkillDefinition(skill); err != nil {
		return agentBinding{}, err
	}
	return binding, nil
}

func (h *agentTaskHandler) ValidateRequest(req runtime.Request) error {
	_, err := h.validateRequest(req)
	return err
}

var _ runtime.Handler = (*agentTaskHandler)(nil)

// stampRoutedOrigin decorates a routed agent's event sink with the routed
// invocation's identity, and supplies instanceID as the origin task id ONLY
// when the event does not already carry one.
//
// An event that reaches here from a coordinator-dispatched task already
// carries the coordinator's canonical task id, stamped by
// subagents.MultiStepHandler from runtime.TaskIdentityFrom(ctx) - the
// model-authored dispatch_tasks task id (or a workflow's wft-... attempt id).
// That id is the CORRELATION KEY every downstream consumer looks work up by:
// uiadapter.SubagentThreads files the live thread under it, the TUI sidebar
// row is keyed by it, controller.NoteStepHeartbeat counts liveness against
// it, and the event bus/NDJSON writer attribute to it. Overwriting it with a
// freshly minted opaque runtime.NewSessionID() filed every live thread under
// a key nothing could look up, so the subagent dialog never resolved a thread
// and always fell back to an empty step log.
//
// instanceID remains the fallback for a non-coordinator invocation, which has
// no correlation id of its own, and the invocation identity itself is
// unaffected either way: it travels on e.Identity.
func stampRoutedOrigin(identity *events.Identity, instanceID string, sink func(agent.Event)) func(agent.Event) {
	return func(e agent.Event) {
		e.Identity = identity
		if e.Origin.TaskID == "" {
			e.Origin.TaskID = instanceID
		}
		sink(e)
	}
}
