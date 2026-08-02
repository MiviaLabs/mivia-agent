package cli

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
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

// The no-runtime switch path rewrites the profile in place instead of
// resolving a new one. Anything model-specific left behind belongs to the
// PREVIOUS model, so the new model would inherit its dial and wire dialect.
func TestLegacySwitchPathClearsThePreviousModelsReasoning(t *testing.T) {
	res := &config.Resolved{ProviderName: "p", Model: "A", Models: []string{"A", "B"}}
	sess := chat.NewSession(res, welcomeStubCompleter{})
	binding := sess.CurrentBinding()
	binding.Profile.Reasoning = reasoning.High
	binding.Profile.ReasoningDialect = reasoning.DialectThinkingEffort
	binding.Profile.ReasoningEfforts = []reasoning.Level{reasoning.Low, reasoning.High}
	if err := sess.SwitchBinding(binding); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if err := switchModelCommand(sess, res, "p", "B"); err != nil {
		t.Fatal(err)
	}
	switched := sess.CurrentBinding()
	if switched.Model != "B" {
		t.Fatalf("model = %q, want B", switched.Model)
	}
	if switched.Profile.Name != "B" {
		t.Fatalf("profile name = %q, want B", switched.Profile.Name)
	}
	if switched.Profile.Reasoning != "" || switched.Profile.ReasoningDialect != "" {
		t.Fatalf("model B inherited %q/%q from model A",
			switched.Profile.Reasoning, switched.Profile.ReasoningDialect)
	}
	// The declared set is what /effort validates against. Leaving model A's set
	// behind makes SetReasoningEffort accept a level for a model that declared
	// none, and the request then carries a level with an empty dialect - which
	// the provider client resolves against its own default rather than dropping.
	if got := sess.ReasoningChoices(); len(got) != 0 {
		t.Fatalf("model B offers %v, want none of model A's", got)
	}
	if err := sess.SetReasoningEffort(reasoning.High); err == nil {
		t.Fatal("SetReasoningEffort accepted a level for a model that declares none")
	}
}
