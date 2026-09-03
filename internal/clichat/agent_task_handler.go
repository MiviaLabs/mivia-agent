package clichat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/agents"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
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
// A positive configured value (default_request_timeout_seconds) is the
// deadline; otherwise DefaultSubagentRequestTimeoutSec (1800s, 30 minutes)
// applies. The resolution lives in config.ResolvedSubagentRequestTimeout so
// the derived http.Client wall covers the same value this handler enforces:
// the wall stays above the budget plus a margin, and a spent budget reports
// as the terminal context deadline, not a transport fault.
//
// Behavior change: the old code fed default_timeout_seconds into every
// subagent request through EffectiveTimeoutSec, so an unset
// default_request_timeout_seconds inherited the 12-hour orchestration
// default. Operators who relied on that must now set
// default_request_timeout_seconds explicitly. This mirrors
// registerMultiStepHandler in dispatcher_handlers.go.
func requestTimeout(configured int) time.Duration {
	return config.ResolvedSubagentRequestTimeout(config.SubagentConfig{DefaultRequestTimeoutSec: configured})
}

// The whole-run counterpart of requestTimeout lives in subagent_budget.go:
// totalTaskTimeout resolves default_total_timeout_seconds into the
// MultiStepHandler TotalTimeout. This file sat at the 500-line structure
// soft cap, so the helper got its own small file instead of a berth here.

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
		// The operator's approval wiring reaches the nested loop, so a
		// delegated write tool faces the same gate the root path would.
		Approval:  h.opts.Approval,
		Completer: binding.Completer, FullRegistry: registry,
		Dispatcher: h.dispatcher, Model: binding.Model, Reasoning: binding.Reasoning,
		ReasoningFunc: binding.EffectiveReasoning,
		SystemPrompt:  systemPrompt, MemoryContext: memoryContext, MaxSteps: maxSteps,
		WorkLimits: limits, DisableProviderReplay: req.DisableProviderReplay,
		ToolTimeout:    config.SaturatingSeconds(h.opts.Config.DefaultTimeout),
		ToolRunTimeout: h.opts.ToolRunTimeout,
		MaxTokens:      maxTokens, MaxContextTokens: binding.ContextBudget(),
		MaxContextTokensFunc: binding.ContextBudget, MaxToolResultChars: h.opts.ToolResultCapBytes,
		BatchResultBudgetBytes: h.opts.BatchResultBudgetBytes,
		RefOnlyTools:           h.opts.RefOnlyTools,
		RemainderSpool:         cliagents.RemainderSpoolFromRegistryVar(registry),
		OutputSchema:           outSchema, SchemaRetryMax: h.opts.Config.SchemaRetryMax,
		RequestTimeout: requestTimeout(h.opts.Config.DefaultRequestTimeoutSec),
		// Same [subagents] wire_stream knob as the other handler surfaces.
		WireStreamTransport: h.opts.Config.WireStreamResolved(),
		// Total budget from default_total_timeout_seconds: the incident gap
		// was exactly this construction running with no TotalTimeout, so a
		// trickling provider pinned the run past every idle watchdog.
		TotalTimeout:              totalTaskTimeout(h.opts.Config.DefaultTotalTimeoutSec),
		SteerWatchdog:             config.SaturatingSeconds(h.opts.Config.Messaging.SteerWatchdogSecondsResolved()),
		ContextPreparationManager: h.opts.ContextPreparationManager,
		ContextPreparationInput:   h.opts.ContextPreparationInput,
		OnEvent:                   OnEventForMultiStep(stampRoutedOrigin(identity, instanceID, emitSubagentProgress)),
		OnToolCancelReady:         h.opts.OnToolCancelReady,
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

func workflowSkillResumeErrorf(name, detail string) error {
	return fmt.Errorf("workflow skill %q %s; recover with: restore the skill to its admitted content, pass --accept-skill-change, or start a fresh run", name, detail)
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
		// The ONE zero-origin event this chain can legitimately carry is the
		// approval prompt: the multi-step wrapper strips its origin on
		// purpose, because its destination is the ROOT approval queue and
		// re-stamping would route it into the subagent dialog that cannot
		// answer it. Everything else keeps the fallback below.
		if e.Kind == agent.EventToolPending && e.Origin.IsZero() {
			sink(e)
			return
		}
		e.Identity = identity
		if e.Origin.TaskID == "" {
			e.Origin.TaskID = instanceID
		}
		sink(e)
	}
}
