package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// Phase 2 of the agent model routing plan: a routed agent executes against a
// completer bound to its own resolved provider. Previously an agent could
// select a model but still inherited the session's completer, so a
// provider-qualified binding was silently provider-local.

// agentBinding is one routed agent's immutable execution target. It is
// resolved once, at dispatcher construction, and never mutated afterwards -
// concurrent Invokes on the shared handler only read it.
type agentBinding struct {
	providerName string
	model        string
	completer    provider.Completer
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
	maxTokens int
	// wallClock bounds one whole routed invocation. Zero means the agent
	// declares none and only the caller's per-task timeout applies.
	wallClock time.Duration
}

// ErrAgentWallClockExceeded is the typed cause attached when a routed agent
// exhausts its own wall-clock ceiling. It distinguishes an agent-policy
// timeout from a caller-imposed task timeout or an operator cancel, all of
// which otherwise surface as context.DeadlineExceeded.
var ErrAgentWallClockExceeded = errors.New("agent wall-clock ceiling exceeded")

// resolveCeilings computes the per-agent resource ceilings. They are
// deliberately independent of MaxTurns: max_turns = 0 means unlimited
// iterations, not unlimited spend or unlimited time, so these still bind.
func (b *agentBinding) resolveCeilings(definition agents.ResolvedAgent, opts SessionDispatcherOpts) {
	if opts.MaxTokens != nil && *opts.MaxTokens > 0 {
		b.maxTokens = *opts.MaxTokens
	}
	if definition.MaxTokens != nil && *definition.MaxTokens > 0 {
		// Tighter wins: an agent may lower the operator's cap, never raise it.
		if b.maxTokens <= 0 || *definition.MaxTokens < b.maxTokens {
			b.maxTokens = *definition.MaxTokens
		}
	}
	if definition.TimeoutSeconds != nil && *definition.TimeoutSeconds > 0 {
		b.wallClock = time.Duration(*definition.TimeoutSeconds) * time.Second
	}
}

// withWallClock applies the agent's wall-clock ceiling as a parent of whatever
// bound the caller already imposed. Layering it rather than replacing the
// caller's timeout matters: MultiStepHandler treats a handler-level
// TotalTimeout as superseding the per-task timeout, so setting that field
// would let a generous agent ceiling silently loosen a tight task deadline.
// As a parent context the tighter of the two always wins.
//
// The cause is a fresh per-invocation error value, not the shared sentinel, so
// ownership is unambiguous: an ancestor that breached ITS ceiling propagates
// its own cause down, and comparing by identity keeps this agent from
// reporting a breach it never had. It still wraps ErrAgentWallClockExceeded so
// callers can match the class with errors.Is.
func (b agentBinding) withWallClock(ctx context.Context, agentName string) (context.Context, context.CancelFunc, error) {
	if b.wallClock <= 0 {
		return ctx, func() {}, nil
	}
	cause := fmt.Errorf("agent %q exceeded its %s ceiling: %w", agentName, b.wallClock, ErrAgentWallClockExceeded)
	ctx, cancel := context.WithTimeoutCause(ctx, b.wallClock, cause)
	return ctx, cancel, cause
}

// contextBudget is the prompt budget this agent may actually use.
func (b agentBinding) contextBudget() int {
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
func declaredBinding(definition agents.ResolvedAgent) bool {
	return strings.TrimSpace(definition.Provider) != "" || strings.TrimSpace(definition.Model) != ""
}

// resolveAgentBinding resolves and validates one agent's execution target.
//
// It is deliberately eager: registerAgentHandlers calls it at dispatcher
// construction so a mistyped provider or model surfaces at startup rather than
// twenty minutes into a run. A failure is stored on the handler and returned
// on invoke, so one bad definition cannot take down an otherwise usable
// session.
func resolveAgentBinding(definition agents.ResolvedAgent, opts SessionDispatcherOpts) (agentBinding, error) {
	binding := agentBinding{
		providerName:  strings.TrimSpace(opts.ProviderName),
		model:         opts.Model,
		completer:     opts.Completer,
		sessionBudget: opts.Budget,
		staticBudget:  opts.MaxContextTokens,
	}
	binding.resolveCeilings(definition, opts)
	if !declaredBinding(definition) {
		// No declared binding: follow the session exactly as before. Nothing
		// new is validated here, because the session's own pair was already
		// authorized when the session binding was published.
		return binding, nil
	}
	if strings.TrimSpace(definition.Provider) != "" {
		binding.providerName = strings.ToLower(strings.TrimSpace(definition.Provider))
	}
	if strings.TrimSpace(definition.Model) != "" {
		binding.model = definition.Model
	}

	// An empty catalog is not authorization. It means nothing can vouch for
	// the declared pair, so a declared binding fails rather than passing by
	// default - the permissive branch exists only for the session-following
	// case handled above.
	profile, ok := selectableModel(opts.ModelCatalog, binding.providerName, binding.model)
	if !ok {
		if len(opts.ModelCatalog) == 0 {
			return agentBinding{}, fmt.Errorf(
				"agent %q declares provider/model %s/%s but no model catalog is available to authorize it",
				definition.Name, binding.providerName, binding.model)
		}
		return agentBinding{}, fmt.Errorf(
			"agent %q model %q is not selectable for provider %q",
			definition.Name, binding.model, binding.providerName)
	}
	// A model's context window is capacity for the prompt AND the response, so
	// it is not a prompt budget. Reserve the response allowance the same way
	// the session's own budget does (config.EffectivePromptTokens); using the
	// raw window would let the loop prune to the full window and then request
	// output on top of it, overflowing at the provider mid-run after tools had
	// already executed.
	var reserve *int
	if binding.maxTokens > 0 {
		reserve = &binding.maxTokens
	}
	binding.contextWindow = config.EffectivePromptTokens(profile, reserve, 0, 0)

	// Completers are provider-scoped: adapters take the model from each
	// request, not from construction. A model-only override therefore needs no
	// new completer, and building one per invoke would allocate an HTTP client
	// for nothing.
	if binding.providerName == strings.TrimSpace(opts.ProviderName) {
		return binding, nil
	}
	if opts.CompleterFactory == nil {
		return agentBinding{}, fmt.Errorf(
			"agent %q requires provider %q but this session cannot construct one (session provider is %q)",
			definition.Name, binding.providerName, opts.ProviderName)
	}
	completer, err := opts.CompleterFactory(binding.providerName, binding.model)
	if err != nil {
		return agentBinding{}, fmt.Errorf("agent %q provider %q: %w", definition.Name, binding.providerName, err)
	}
	if completer == nil {
		return agentBinding{}, fmt.Errorf("agent %q provider %q: completer factory returned nothing", definition.Name, binding.providerName)
	}
	binding.completer = completer
	return binding, nil
}

// selectableModel returns the catalog profile for one provider-qualified
// model. Unlike the previous modelInCatalog helper it reports the profile, so
// the caller can bind the routed model's own context window instead of
// inheriting the session model's.
func selectableModel(catalog []config.ProviderModelGroup, providerName, model string) (config.ModelSpec, bool) {
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
