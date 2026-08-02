package chat

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func effortSession(t *testing.T, comp *requestCaptureCompleter, model string) *Session {
	t.Helper()
	return NewSession(&config.Resolved{
		ProviderName: "zai",
		Model:        model,
		Models:       []string{reasoningModel, plainModel},
		ModelProfiles: []config.ModelSpec{
			{
				Name: reasoningModel, ContextWindowTokens: 100000,
				ReasoningEfforts: []reasoning.Level{reasoning.Low, reasoning.High, reasoning.Max},
				Reasoning:        reasoning.High,
				ReasoningDialect: reasoning.DialectThinkingEffort,
			},
			{Name: plainModel, ContextWindowTokens: 100000},
		},
	}, comp)
}

func lastRequestLevel(t *testing.T, comp *requestCaptureCompleter) reasoning.Level {
	t.Helper()
	if len(comp.requests) == 0 {
		t.Fatal("no provider request was made")
	}
	return comp.requests[len(comp.requests)-1].ReasoningLevel
}

func TestReasoningChoicesComeFromTheActiveModel(t *testing.T) {
	s := effortSession(t, &requestCaptureCompleter{}, reasoningModel)
	want := []reasoning.Level{reasoning.Low, reasoning.High, reasoning.Max}
	if got := s.ReasoningChoices(); !slices.Equal(got, want) {
		t.Fatalf("choices = %v, want %v in config order", got, want)
	}
	if got := s.ReasoningEffort(); got != reasoning.High {
		t.Fatalf("effective effort = %q, want the model default high", got)
	}
}

func TestReasoningChoicesAreEmptyForAModelThatOffersNothing(t *testing.T) {
	s := effortSession(t, &requestCaptureCompleter{}, plainModel)
	if got := s.ReasoningChoices(); len(got) != 0 {
		t.Fatalf("choices = %v, want none", got)
	}
	if got := s.ReasoningEffort(); got.Active() {
		t.Fatalf("effective effort = %q, want unset", got)
	}
}

// The override must reach the wire, not just the accessor.
func TestIntegrationEffortOverrideReachesThePlainRequest(t *testing.T) {
	comp := &requestCaptureCompleter{}
	s := effortSession(t, comp, reasoningModel)
	if err := s.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatalf("SetReasoningEffort: %v", err)
	}
	if _, err := s.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := lastRequestLevel(t, comp); got != reasoning.Low {
		t.Fatalf("plain request carried %q, want the override low", got)
	}
	// The dialect still comes from the model, not the override.
	if got := comp.requests[0].ReasoningDialect; got != reasoning.DialectThinkingEffort {
		t.Fatalf("dialect = %q", got)
	}
}

func TestIntegrationEffortOverrideReachesTheAgentRequest(t *testing.T) {
	comp := &requestCaptureCompleter{}
	s := effortSession(t, comp, reasoningModel)
	s.Tools = tools.NewRegistry()
	s.UseTools = true
	if err := s.SetReasoningEffort(reasoning.Max); err != nil {
		t.Fatalf("SetReasoningEffort: %v", err)
	}
	if _, err := s.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := lastRequestLevel(t, comp); got != reasoning.Max {
		t.Fatalf("agent request carried %q, want the override max", got)
	}
}

// A level the model never offered must be refused, and refusing must leave the
// previous effort in force rather than clearing it.
func TestEffortOutsideTheDeclaredSetIsRefused(t *testing.T) {
	comp := &requestCaptureCompleter{}
	s := effortSession(t, comp, reasoningModel)
	err := s.SetReasoningEffort(reasoning.Minimal)
	if err == nil {
		t.Fatal("an undeclared effort must be refused")
	}
	for _, want := range []string{"minimal", "low, high, max"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must name %q, got: %v", want, err)
		}
	}
	if got := s.ReasoningEffort(); got != reasoning.High {
		t.Fatalf("a refused change altered the effort to %q", got)
	}
	if _, err := s.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := lastRequestLevel(t, comp); got != reasoning.High {
		t.Fatalf("request carried %q after a refused change", got)
	}
}

func TestEffortIsRefusedForAModelThatOffersNothing(t *testing.T) {
	s := effortSession(t, &requestCaptureCompleter{}, plainModel)
	err := s.SetReasoningEffort(reasoning.High)
	if err == nil {
		t.Fatal("a model that offers nothing must refuse an effort")
	}
	if !strings.Contains(err.Error(), plainModel) {
		t.Fatalf("error must name the model, got: %v", err)
	}
}

// The override is model-scoped: it dies with the binding it was chosen for.
// Carrying it across would send one model's chosen depth to another.
func TestEffortOverrideDiesWithTheBinding(t *testing.T) {
	comp := &requestCaptureCompleter{}
	s := effortSession(t, comp, reasoningModel)
	if err := s.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatal(err)
	}
	binding := s.CurrentBinding()
	binding.Model = plainModel
	binding.Profile = config.ModelSpec{Name: plainModel, ContextWindowTokens: 100000}
	if err := s.SwitchBinding(binding); err != nil {
		t.Fatalf("SwitchBinding: %v", err)
	}
	if got := s.ReasoningEffort(); got.Active() {
		t.Fatalf("the override survived a switch: %q", got)
	}
	if _, err := s.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := lastRequestLevel(t, comp); got.Active() {
		t.Fatalf("request carried %q after switching to a model that offers nothing", got)
	}
}

// Switching back to a reasoning model restores its DEFAULT, not the effort the
// user last chose for it.
func TestSwitchingBackRestoresTheModelDefault(t *testing.T) {
	comp := &requestCaptureCompleter{}
	s := effortSession(t, comp, reasoningModel)
	if err := s.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatal(err)
	}
	if !s.SelectModel(plainModel) {
		t.Fatal("SelectModel refused")
	}
	binding := s.CurrentBinding()
	binding.Model = reasoningModel
	binding.Profile = config.ModelSpec{
		Name: reasoningModel, ContextWindowTokens: 100000,
		ReasoningEfforts: []reasoning.Level{reasoning.Low, reasoning.High, reasoning.Max},
		Reasoning:        reasoning.High,
		ReasoningDialect: reasoning.DialectThinkingEffort,
	}
	if err := s.SwitchBinding(binding); err != nil {
		t.Fatalf("SwitchBinding: %v", err)
	}
	if got := s.ReasoningEffort(); got != reasoning.High {
		t.Fatalf("effort = %q, want the model default high", got)
	}
}

func TestSelectModelClearsTheEffortOverride(t *testing.T) {
	s := effortSession(t, &requestCaptureCompleter{}, reasoningModel)
	if err := s.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatal(err)
	}
	if !s.SelectModel(plainModel) {
		t.Fatal("SelectModel refused")
	}
	if got := s.ReasoningEffort(); got.Active() {
		t.Fatalf("the override survived SelectModel: %q", got)
	}
}

// A model may offer efforts while shipping with none active. Choosing one is
// then the only way it ever sends a reasoning field.
func TestEffortCanActivateAModelThatShipsWithReasoningOff(t *testing.T) {
	comp := &requestCaptureCompleter{}
	s := NewSession(&config.Resolved{
		ProviderName: "zai", Model: reasoningModel,
		ModelProfiles: []config.ModelSpec{{
			Name: reasoningModel, ContextWindowTokens: 100000,
			ReasoningEfforts: []reasoning.Level{reasoning.Low, reasoning.High},
			ReasoningDialect: reasoning.DialectThinking,
		}},
	}, comp)
	if got := s.ReasoningEffort(); got.Active() {
		t.Fatalf("effort = %q, want unset before the user opts in", got)
	}
	if err := s.SetReasoningEffort(reasoning.High); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := lastRequestLevel(t, comp); got != reasoning.High {
		t.Fatalf("request carried %q, want high", got)
	}
}
