package cli

import (
	"fmt"
	"strings"

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
	binding.contextWindow = profile.ContextWindowTokens

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
