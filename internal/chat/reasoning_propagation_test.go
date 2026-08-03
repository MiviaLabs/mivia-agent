package chat

import (
	"context"
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

const (
	reasoningModel = "thinker"
	plainModel     = "plain"
)

func reasoningSession(t *testing.T, comp *requestCaptureCompleter, model string) *Session {
	t.Helper()
	return NewSession(&config.Resolved{
		ProviderName: "zai",
		Model:        model,
		Models:       []string{reasoningModel, plainModel},
		ModelProfiles: []config.ModelSpec{
			{
				Name: reasoningModel, ContextWindowTokens: 100000,
				Reasoning: reasoning.High, ReasoningDialect: reasoning.DialectThinkingEffort,
			},
			{Name: plainModel, ContextWindowTokens: 100000},
		},
	}, comp)
}

func onlyRequestSetting(t *testing.T, comp *requestCaptureCompleter) reasoning.Setting {
	t.Helper()
	if len(comp.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(comp.requests))
	}
	req := comp.requests[0]
	return reasoning.Setting{Level: req.ReasoningLevel, Dialect: req.ReasoningDialect}
}

func TestIntegrationPlainTurnCarriesModelReasoning(t *testing.T) {
	comp := &requestCaptureCompleter{}
	s := reasoningSession(t, comp, reasoningModel)
	if _, err := s.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	got := onlyRequestSetting(t, comp)
	want := reasoning.Setting{Level: reasoning.High, Dialect: reasoning.DialectThinkingEffort}
	if got != want {
		t.Fatalf("plain turn carried %+v, want %+v", got, want)
	}
}

func TestIntegrationPlainTurnSendsNothingForANonReasoningModel(t *testing.T) {
	comp := &requestCaptureCompleter{}
	s := reasoningSession(t, comp, plainModel)
	if _, err := s.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := onlyRequestSetting(t, comp); got != (reasoning.Setting{}) {
		t.Fatalf("a model with no reasoning key carried %+v", got)
	}
}

func agentTurnEffortSession(t *testing.T, comp *requestCaptureCompleter, model string) *Session {
	t.Helper()
	s := reasoningSession(t, comp, model)
	s.Tools = tools.NewRegistry()
	s.UseTools = true
	return s
}

func TestIntegrationAgentTurnCarriesModelReasoning(t *testing.T) {
	comp := &requestCaptureCompleter{}
	s := agentTurnEffortSession(t, comp, reasoningModel)
	if !s.AgentTurnEnabled() {
		t.Fatal("this test must exercise the agent path")
	}
	if _, err := s.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	got := onlyRequestSetting(t, comp)
	want := reasoning.Setting{Level: reasoning.High, Dialect: reasoning.DialectThinkingEffort}
	if got != want {
		t.Fatalf("agent turn carried %+v, want %+v", got, want)
	}
}

// One binding must mean one wire shape. If the plain and agent paths disagreed,
// enabling tools would silently change how hard the model thinks.
func TestIntegrationPlainAndAgentTurnsAgreeForOneBinding(t *testing.T) {
	plainComp := &requestCaptureCompleter{}
	if _, err := reasoningSession(t, plainComp, reasoningModel).
		SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	agentComp := &requestCaptureCompleter{}
	if _, err := agentTurnEffortSession(t, agentComp, reasoningModel).
		SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	if plain, agentic := onlyRequestSetting(t, plainComp), onlyRequestSetting(t, agentComp); plain != agentic {
		t.Fatalf("plain turn sent %+v but agent turn sent %+v", plain, agentic)
	}
}

// The dial travels with the binding, so selecting a model that does not
// configure reasoning must turn it off rather than leave the previous model's
// setting in force.
func TestIntegrationSwitchingToANonReasoningModelClearsTheDial(t *testing.T) {
	comp := &requestCaptureCompleter{}
	s := reasoningSession(t, comp, reasoningModel)
	binding := s.CurrentBinding()
	binding.Model = plainModel
	binding.Profile = config.ModelSpec{Name: plainModel, ContextWindowTokens: 100000}
	if err := s.SwitchBinding(binding); err != nil {
		t.Fatalf("SwitchBinding: %v", err)
	}
	if _, err := s.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := onlyRequestSetting(t, comp); got != (reasoning.Setting{}) {
		t.Fatalf("the previous model's dial survived the switch: %+v", got)
	}
}

func TestIntegrationSwitchingToAReasoningModelActivatesTheDial(t *testing.T) {
	comp := &requestCaptureCompleter{}
	s := reasoningSession(t, comp, plainModel)
	binding := s.CurrentBinding()
	binding.Model = reasoningModel
	binding.Profile = config.ModelSpec{
		Name: reasoningModel, ContextWindowTokens: 100000,
		Reasoning: reasoning.Low, ReasoningDialect: reasoning.DialectOpenAI,
	}
	if err := s.SwitchBinding(binding); err != nil {
		t.Fatalf("SwitchBinding: %v", err)
	}
	if _, err := s.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	want := reasoning.Setting{Level: reasoning.Low, Dialect: reasoning.DialectOpenAI}
	if got := onlyRequestSetting(t, comp); got != want {
		t.Fatalf("switch carried %+v, want %+v", got, want)
	}
}

// SelectModel is an exported rename that does not resolve a new profile, so
// without an explicit clear the newly selected model would keep sending the
// previous model's dial and wire dialect.
func TestIntegrationSelectModelClearsThePreviousModelsReasoning(t *testing.T) {
	comp := &requestCaptureCompleter{}
	s := reasoningSession(t, comp, reasoningModel)
	if !s.SelectModel(plainModel) {
		t.Fatal("SelectModel refused an allowed model")
	}
	if _, err := s.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := onlyRequestSetting(t, comp); got != (reasoning.Setting{}) {
		t.Fatalf("the previous model's dial survived SelectModel: %+v", got)
	}
}
