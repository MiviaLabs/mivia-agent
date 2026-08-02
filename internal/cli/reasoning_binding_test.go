package cli

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// The session model reasons; the routed model does not, and vice versa. Both
// directions matter: a routed agent that inherited the session's dial would
// send one model's wire fields to a different model.
func reasoningCatalog() []config.ProviderModelGroup {
	return []config.ProviderModelGroup{
		{Provider: "zai", Selectable: true, Active: true, Models: []config.ModelSpec{
			{
				Name: "glm-5.2", ContextWindowTokens: 200000,
				Reasoning: reasoning.High, ReasoningDialect: reasoning.DialectThinkingEffort,
			},
			{Name: "glm-5-turbo", ContextWindowTokens: 200000},
		}},
		{Provider: "openrouter", Selectable: true, Models: []config.ModelSpec{
			{Name: "openai/o5", ContextWindowTokens: 128000, Reasoning: reasoning.Low},
		}},
	}
}

func reasoningDispatcherOpts(model string) SessionDispatcherOpts {
	return SessionDispatcherOpts{
		Model: model, ProviderName: "zai",
		ModelCatalog:     reasoningCatalog(),
		MaxContextTokens: 100000,
		Completer:        &bindingProbeCompleter{name: "zai"},
		CompleterFactory: func(providerName, _ string) (provider.Completer, error) {
			return &bindingProbeCompleter{name: providerName}, nil
		},
	}
}

func TestSessionReasoningComesFromTheSelectedModel(t *testing.T) {
	want := reasoning.Setting{Level: reasoning.High, Dialect: reasoning.DialectThinkingEffort}
	if got := sessionReasoning(reasoningDispatcherOpts("glm-5.2")); got != want {
		t.Fatalf("sessionReasoning = %+v, want %+v", got, want)
	}
	if got := sessionReasoning(reasoningDispatcherOpts("glm-5-turbo")); got != (reasoning.Setting{}) {
		t.Fatalf("a non-reasoning model produced %+v", got)
	}
	// A model outside the catalog cannot vouch for any dial, so it sends
	// nothing rather than borrowing the previous answer.
	if got := sessionReasoning(reasoningDispatcherOpts("unknown-model")); got != (reasoning.Setting{}) {
		t.Fatalf("an uncatalogued model produced %+v", got)
	}
}

func TestRoutedAgentUsesItsOwnModelsReasoning(t *testing.T) {
	opts := reasoningDispatcherOpts("glm-5.2")
	binding, err := resolveAgentBinding(agents.ResolvedAgent{
		Name: "router", Provider: "openrouter", Model: "openai/o5",
	}, opts)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := reasoning.Setting{Level: reasoning.Low}
	if binding.reasoning != want {
		t.Fatalf("routed binding carried %+v, want the routed model's %+v", binding.reasoning, want)
	}
}

// An agent that follows the session still resolves against the session model's
// own catalog entry, so it agrees with the session rather than sending nothing.
func TestSessionFollowingAgentMatchesTheSessionModel(t *testing.T) {
	opts := reasoningDispatcherOpts("glm-5.2")
	binding, err := resolveAgentBinding(agents.ResolvedAgent{Name: "plain"}, opts)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if binding.reasoning != sessionReasoning(opts) {
		t.Fatalf("session-following agent carried %+v, session has %+v", binding.reasoning, sessionReasoning(opts))
	}
}

// A routed agent on a model with no dial must not inherit the reasoning
// session's, which is the failure this pairing is here to catch.
func TestRoutedAgentOnANonReasoningModelSendsNothing(t *testing.T) {
	opts := reasoningDispatcherOpts("glm-5.2")
	binding, err := resolveAgentBinding(agents.ResolvedAgent{
		Name: "quick", Model: "glm-5-turbo",
	}, opts)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if binding.reasoning != (reasoning.Setting{}) {
		t.Fatalf("routed agent inherited %+v from the session model", binding.reasoning)
	}
}
