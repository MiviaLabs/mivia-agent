package chat

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
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

// Restoring a saved selection renames the binding without resolving a new
// profile, so it owes the same reset as every other rename path: the reasoning
// surface left on the profile describes the model being renamed away from.
func TestLoadingAnotherModelsSessionClearsTheReasoningSurface(t *testing.T) {
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	saved := effortSession(t, &requestCaptureCompleter{}, plainModel)
	bindContextSession(t, saved, store)
	saved.Messages = []provider.Message{{Role: provider.RoleUser, Content: "hi"}}
	if err := saved.Save("plain"); err != nil {
		t.Fatalf("save: %v", err)
	}

	s := effortSession(t, &requestCaptureCompleter{}, reasoningModel)
	bindContextSession(t, s, store)
	if err := s.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatal(err)
	}
	if err := s.Load("plain"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := s.CurrentModel(); got != plainModel {
		t.Fatalf("model = %q, want %q", got, plainModel)
	}
	if got := s.ReasoningEffort(); got.Active() {
		t.Fatalf("the override chosen for %s survived the restore: %q", reasoningModel, got)
	}
	if got := s.ReasoningChoices(); len(got) != 0 {
		t.Fatalf("restored model offers %v, want the declared set cleared", got)
	}
	binding := s.CurrentBinding()
	if binding.Profile.Name != plainModel {
		t.Fatalf("profile name = %q, want %q", binding.Profile.Name, plainModel)
	}
	if binding.Profile.Reasoning != "" || binding.Profile.ReasoningDialect != "" {
		t.Fatalf("restored model inherited %q/%q",
			binding.Profile.Reasoning, binding.Profile.ReasoningDialect)
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

// An in-flight turn already captured its binding, so accepting a change now
// would report an effort the running request never got.
func TestEffortCannotChangeWhileWorkIsActive(t *testing.T) {
	s := effortSession(t, &requestCaptureCompleter{}, reasoningModel)
	s.mu.Lock()
	s.activeTurns = 1
	s.mu.Unlock()
	err := s.SetReasoningEffort(reasoning.Low)
	if err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("error = %v, want a refusal naming active work", err)
	}
	if got := s.ReasoningEffort(); got != reasoning.High {
		t.Fatalf("a refused change altered the effort to %q", got)
	}
}

// A model that offers efforts with no configured default ships sending no
// reasoning field at all. Choosing a level must not be one-way: clearing the
// override is the only route back to that shipped state.
func TestClearingTheEffortReturnsToAModelThatShipsWithNoDefault(t *testing.T) {
	comp := &requestCaptureCompleter{}
	s := NewSession(&config.Resolved{
		ProviderName: "zai", Model: reasoningModel,
		ModelProfiles: []config.ModelSpec{{
			Name: reasoningModel, ContextWindowTokens: 100000,
			ReasoningEfforts: []reasoning.Level{reasoning.Low, reasoning.High},
			ReasoningDialect: reasoning.DialectThinkingEffort,
		}},
	}, comp)
	if err := s.SetReasoningEffort(reasoning.High); err != nil {
		t.Fatal(err)
	}
	if err := s.SetReasoningEffort(""); err != nil {
		t.Fatalf("clearing back to the model default: %v", err)
	}
	if got := s.ReasoningEffort(); got.Active() {
		t.Fatalf("effort = %q, want the unset state this model ships with", got)
	}
	if _, err := s.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := lastRequestLevel(t, comp); got.Active() {
		t.Fatalf("request carried %q after the override was cleared", got)
	}
}

// Clearing restores the CONFIGURED default when there is one, rather than
// leaving the model with no reasoning field it never asked to drop.
func TestClearingTheEffortRestoresTheConfiguredDefault(t *testing.T) {
	s := effortSession(t, &requestCaptureCompleter{}, reasoningModel)
	if err := s.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatal(err)
	}
	if err := s.SetReasoningEffort(""); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if got := s.ReasoningEffort(); got != reasoning.High {
		t.Fatalf("effort = %q, want the configured default high", got)
	}
}

func TestReasoningDefaultIgnoresTheOverride(t *testing.T) {
	s := effortSession(t, &requestCaptureCompleter{}, reasoningModel)
	if err := s.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatal(err)
	}
	if got := s.ReasoningDefault(); got != reasoning.High {
		t.Fatalf("default = %q, want the model's configured high", got)
	}
	if got := s.ReasoningEffort(); got != reasoning.Low {
		t.Fatalf("effective = %q, want the override low", got)
	}
}

// ReasoningChoices hands out a copy: a caller that sorts or truncates the
// picker list must not rewrite the model's declared configuration.
func TestReasoningChoicesAreNotAliasedToTheProfile(t *testing.T) {
	s := effortSession(t, &requestCaptureCompleter{}, reasoningModel)
	choices := s.ReasoningChoices()
	if len(choices) == 0 {
		t.Fatal("expected choices")
	}
	choices[0] = reasoning.Max
	if again := s.ReasoningChoices(); again[0] != reasoning.Low {
		t.Fatalf("mutating the returned slice changed the profile: %v", again)
	}
}

// A choice that coincides with the model's configured default is still a
// choice. Nothing downstream can tell it apart from an untouched dial by
// comparing levels, which is why the session reports the fact instead.
func TestReasoningOverrideDistinguishesAChoiceFromTheDefault(t *testing.T) {
	s := effortSession(t, &requestCaptureCompleter{}, reasoningModel)
	if level, ok := s.ReasoningOverride(); ok || level.Active() {
		t.Fatalf("untouched dial reported override %q/%v", level, ok)
	}
	if err := s.SetReasoningEffort(reasoning.High); err != nil {
		t.Fatal(err)
	}
	level, ok := s.ReasoningOverride()
	if !ok || level != reasoning.High {
		t.Fatalf("override = %q/%v, want high chosen", level, ok)
	}
	if err := s.SetReasoningEffort(""); err != nil {
		t.Fatal(err)
	}
	if level, ok := s.ReasoningOverride(); ok || level.Active() {
		t.Fatalf("cleared dial reported override %q/%v", level, ok)
	}
}
