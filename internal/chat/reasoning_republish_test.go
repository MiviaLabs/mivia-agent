package chat

import (
	"context"
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// Republishing the SAME provider/model is not a model change. The picker's
// active row states the level in force, so committing that row must leave the
// dial where the row said it was.
func TestEffortSurvivesRepublishingTheSameModel(t *testing.T) {
	comp := &requestCaptureCompleter{}
	s := effortSession(t, comp, reasoningModel)
	if err := s.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatal(err)
	}
	if err := s.SwitchBinding(s.PublishedBinding()); err != nil {
		t.Fatalf("SwitchBinding: %v", err)
	}
	if got := s.ReasoningEffort(); got != reasoning.Low {
		t.Fatalf("effort = %q after republishing the same model, want low", got)
	}
	if _, err := s.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := lastRequestLevel(t, comp); got != reasoning.Low {
		t.Fatalf("request carried %q after republishing the same model", got)
	}
}

// A same-name publication is not always the same PROFILE. A held level the
// incoming profile no longer declares would ride out paired with that
// profile's dialect, which is the hazard the reset exists for.
func TestEffortDiesWhenTheRepublishedProfileNoLongerOffersIt(t *testing.T) {
	s := effortSession(t, &requestCaptureCompleter{}, reasoningModel)
	if err := s.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatal(err)
	}
	binding := s.PublishedBinding()
	binding.Profile.ReasoningEfforts = []reasoning.Level{reasoning.High, reasoning.Max}
	binding.Profile.Reasoning = reasoning.High
	if err := s.SwitchBinding(binding); err != nil {
		t.Fatalf("SwitchBinding: %v", err)
	}
	if got := s.ReasoningEffort(); got != reasoning.High {
		t.Fatalf("effort = %q, want the incoming profile's default high", got)
	}
}

// The same model name on a different provider is a different model: the level
// was chosen against the outgoing provider's declared set.
func TestEffortDiesWhenTheProviderChangesUnderTheSameModelName(t *testing.T) {
	s := effortSession(t, &requestCaptureCompleter{}, reasoningModel)
	if err := s.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatal(err)
	}
	binding := s.PublishedBinding()
	binding.ProviderName = "openrouter"
	if err := s.SwitchBinding(binding); err != nil {
		t.Fatalf("SwitchBinding: %v", err)
	}
	if got := s.ReasoningEffort(); got != reasoning.High {
		t.Fatalf("effort = %q, want the model default high", got)
	}
}
