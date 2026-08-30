package cliagents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// Phase 2 of the agent model routing plan: a routed agent executes against a
// completer bound to its own resolved provider. Previously an agent could
// select a model but still inherited the session's completer, so a
// provider-qualified binding was silently provider-local.

// AgentBinding is one routed agent's immutable execution target. It is
// resolved once, at dispatcher construction, and never mutated afterwards -
// concurrent Invokes on the shared handler only read it.
type AgentBinding struct {
	ProviderName string
	Model        string
	Completer    provider.Completer
	// contextWindow is the routed model's own capacity. Zero means the agent
	// follows the session (no declared binding), in which case sessionBudget
	// alone applies.
	contextWindow int
	// sessionBudget reports the live session prompt budget (/budget). The
	// effective budget is the tighter of the two, so an operator cap still
	// binds a routed agent and a small routed model still binds a large
	// session.
	sessionBudget func() int
	staticBudget  int
	// maxTokens caps one provider response. It is the tighter of the agent's
	// own ceiling and the operator's session cap, so an agent file can lower
	// the ceiling but never raise it above what the operator allowed.
	MaxTokens int
	// wallClock bounds one whole routed invocation. Zero means the agent
	// declares none and only the caller's per-task timeout applies.
	wallClock time.Duration
	// reasoning is the routed MODEL's dial, not the session's. An agent
	// pinned to another model must think at the depth that model declares;
	// inheriting the session's would send one model's wire fields to another.
	Reasoning reasoning.Setting
	// liveSessionDial reports the session's effective dial (/effort). It is
	// set only when this agent resolved to the session's own provider and
	// model, because a runtime effort choice is scoped to the model it was
	// chosen for: an agent pinned elsewhere keeps its own model's dial.
	liveSessionDial func() reasoning.Setting
}

// ErrAgentWallClockExceeded is the typed cause attached when a routed agent
// exhausts its own wall-clock ceiling. It distinguishes an agent-policy
// timeout from a caller-imposed task timeout or an operator cancel, all of
// which otherwise surface as context.DeadlineExceeded.
var ErrAgentWallClockExceeded = errors.New("agent wall-clock ceiling exceeded")

// resolveCeilings computes the per-agent resource ceilings. They are
// deliberately independent of MaxTurns: max_turns = 0 means unlimited
// iterations, not unlimited spend or unlimited time, so these still bind.
func (b *AgentBinding) resolveCeilings(definition agents.ResolvedAgent, opts SessionDispatcherOpts, modelMaxTokens int) {
	if opts.MaxTokens != nil && *opts.MaxTokens > 0 {
		b.MaxTokens = *opts.MaxTokens
	}
	if definition.MaxTokens != nil && *definition.MaxTokens > 0 {
		// Tighter wins: an agent may lower the operator's cap, never raise it.
		if b.MaxTokens <= 0 || *definition.MaxTokens < b.MaxTokens {
			b.MaxTokens = *definition.MaxTokens
		}
	}
	if modelMaxTokens > 0 && (b.MaxTokens <= 0 || modelMaxTokens < b.MaxTokens) {
		b.MaxTokens = modelMaxTokens
	}
	if definition.TimeoutSeconds != nil && *definition.TimeoutSeconds > 0 {
		b.wallClock = config.SaturatingSeconds(*definition.TimeoutSeconds)
	}
}

// WithWallClock applies the agent's wall-clock ceiling as a parent of whatever
// bound the caller already imposed. Layering it rather than replacing the
// caller's timeout matches the handler contract: MultiStepHandler and
// OneShotHandler clamp a handler-level TotalTimeout against the per-task
// timeout and keep the tighter of the two, so this parent context composes
// with the handler bound instead of fighting it - the tightest deadline
// always wins.
//
// The cause is a fresh per-invocation error value, not the shared sentinel, so
// ownership is unambiguous: an ancestor that breached ITS ceiling propagates
// its own cause down, and comparing by identity keeps this agent from
// reporting a breach it never had. It still wraps ErrAgentWallClockExceeded so
// callers can match the class with errors.Is.
func (b AgentBinding) WithWallClock(ctx context.Context, agentName string) (context.Context, context.CancelFunc, error) {
	if b.wallClock <= 0 {
		return ctx, func() {}, nil
	}
	cause := fmt.Errorf("agent %q exceeded its %s ceiling: %w", agentName, b.wallClock, ErrAgentWallClockExceeded)
	ctx, cancel := context.WithTimeoutCause(ctx, b.wallClock, cause)
	return ctx, cancel, cause
}

// EffectiveReasoning is the dial this agent's requests carry.
func (b AgentBinding) EffectiveReasoning() reasoning.Setting {
	if b.liveSessionDial != nil {
		return b.liveSessionDial()
	}
	return b.Reasoning
}

// runsOnSessionModel reports whether this binding targets the exact model the
// session itself runs on, which is the only case where a session-scoped effort
// choice also applies to the agent.
func (b AgentBinding) runsOnSessionModel(opts SessionDispatcherOpts) bool {
	return b.ProviderName == strings.ToLower(strings.TrimSpace(opts.ProviderName)) &&
		b.Model == strings.TrimSpace(opts.Model)
}

// ContextBudget is the prompt budget this agent may actually use.
func (b AgentBinding) ContextBudget() int {
	session := b.staticBudget
	if b.sessionBudget != nil {
		if live := b.sessionBudget(); live > 0 {
			session = live
		}
	}
	if b.contextWindow <= 0 {
		return session
	}
	if session <= 0 || b.contextWindow < session {
		return b.contextWindow
	}
	return session
}

// declared reports whether the agent named its own provider or model rather
// than following the session.
func DeclaredBinding(definition agents.ResolvedAgent) bool {
	return strings.TrimSpace(definition.Provider) != "" || strings.TrimSpace(definition.Model) != ""
}

// ResolveAgentBinding resolves and validates one agent's execution target.
//
// It is deliberately eager: registerAgentHandlers calls it at dispatcher
// construction so a mistyped provider or model surfaces at startup rather than
// twenty minutes into a run. A failure is stored on the handler and returned
// on invoke, so one bad definition cannot take down an otherwise usable
// session.
func ResolveAgentBinding(definition agents.ResolvedAgent, opts SessionDispatcherOpts) (AgentBinding, error) {
	providerName := strings.TrimSpace(opts.ProviderName)
	model := opts.Model
	if strings.TrimSpace(definition.Provider) != "" {
		providerName = definition.Provider
	}
	if strings.TrimSpace(definition.Model) != "" {
		model = definition.Model
	}
	return resolveAgentBindingAt(definition, opts, providerName, model, DeclaredBinding(definition))
}

// ResolvePinnedAgentBinding re-authorizes a provider/model pair restored from
// a ledger row. The pair is descriptive metadata, never a handler selector:
// the current catalog and provider factory must authorize it again.
func ResolvePinnedAgentBinding(definition agents.ResolvedAgent, opts SessionDispatcherOpts, providerName, model string) (AgentBinding, error) {
	if strings.TrimSpace(providerName) == "" || strings.TrimSpace(model) == "" {
		return AgentBinding{}, fmt.Errorf("incomplete persisted provider/model binding")
	}
	return resolveAgentBindingAt(definition, opts, providerName, model, true)
}

func resolveAgentBindingAt(definition agents.ResolvedAgent, opts SessionDispatcherOpts, providerName, model string, requireCatalog bool) (AgentBinding, error) {
	binding := AgentBinding{
		ProviderName:  strings.ToLower(strings.TrimSpace(providerName)),
		Model:         strings.TrimSpace(model),
		Completer:     opts.Completer,
		sessionBudget: opts.Budget,
		staticBudget:  opts.MaxContextTokens,
	}
	profile, profileOK := SelectableModel(opts.ModelCatalog, binding.ProviderName, binding.Model)
	if requireCatalog && !profileOK {
		// An empty catalog is not authorization. It means nothing can vouch for
		// default - the session-following case may use legacy test/config paths.
		if len(opts.ModelCatalog) == 0 {
			return AgentBinding{}, fmt.Errorf(
				"agent %q provider/model %s/%s cannot be authorized because no model catalog is available",
				definition.Name, binding.ProviderName, binding.Model)
		}
		return AgentBinding{}, fmt.Errorf(
			"agent %q model %q is not selectable for provider %q",
			definition.Name, binding.Model, binding.ProviderName)
	}
	binding.resolveCeilings(definition, opts, profile.MaxOutputTokens)
	if binding.runsOnSessionModel(opts) {
		binding.liveSessionDial = opts.Reasoning
	}
	if profileOK {
		binding.Reasoning = config.ModelReasoning(profile)
		reserve := binding.MaxTokens
		if reserve > 0 {
			binding.contextWindow = config.EffectivePromptTokens(profile, &reserve, 0, 0)
		} else {
			binding.contextWindow = config.EffectivePromptTokens(profile, nil, 0, 0)
		}
	}
	if !requireCatalog && !DeclaredBinding(definition) {
		// No declared binding: follow the session exactly as before, while
		// still applying the selected session model's physical ceilings.
		return binding, nil
	}

	// A model's context window is capacity for the prompt AND the response, so
	// it is not a prompt budget. Reserve the response allowance the same way
	// the session's own budget does (config.EffectivePromptTokens); using the
	// raw window would let the loop prune to the full window and then request
	// output on top of it, overflowing at the provider mid-run after tools had
	// already executed.
	// Completers are provider-scoped: adapters take the model from each
	// request, not from construction. A model-only override therefore needs no
	// new completer, and building one per invoke would allocate an HTTP client
	// for nothing.
	if binding.ProviderName == strings.ToLower(strings.TrimSpace(opts.ProviderName)) {
		return binding, nil
	}
	if opts.CompleterFactory == nil {
		return AgentBinding{}, fmt.Errorf(
			"agent %q requires provider %q but this session cannot construct one (session provider is %q)",
			definition.Name, binding.ProviderName, opts.ProviderName)
	}
	completer, err := opts.CompleterFactory(binding.ProviderName, binding.Model)
	if err != nil {
		return AgentBinding{}, fmt.Errorf("agent %q provider %q is unavailable", definition.Name, binding.ProviderName)
	}
	if completer == nil {
		return AgentBinding{}, fmt.Errorf("agent %q provider %q: completer factory returned nothing", definition.Name, binding.ProviderName)
	}
	binding.Completer = completer
	return binding, nil
}

// SelectableModel returns the catalog profile for one provider-qualified
// model. Unlike the previous modelInCatalog helper it reports the profile, so
// the caller can bind the routed model's own context window instead of
// inheriting the session model's.
func SelectableModel(catalog []config.ProviderModelGroup, providerName, model string) (config.ModelSpec, bool) {
	for _, group := range catalog {
		if group.Provider != providerName || !group.Selectable {
			continue
		}
		for _, profile := range group.Models {
			if profile.Name == model {
				return profile, true
			}
		}
	}
	return config.ModelSpec{}, false
}
