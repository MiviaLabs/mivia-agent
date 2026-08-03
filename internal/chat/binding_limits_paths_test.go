package chat

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func limitsSession() *Session {
	return NewSession(&config.Resolved{
		ProviderName:  "zai",
		Model:         "m",
		ModelProfiles: []config.ModelSpec{{Name: "m", ContextWindowTokens: 100000}},
	}, &requestCaptureCompleter{})
}

// A negative cap is a typo, not a request for "unlimited": accepting one would
// silently hand the model a budget nobody asked for.
func TestPromptBudgetRejectsNegativeAndOversizedCaps(t *testing.T) {
	s := limitsSession()
	base := s.PromptBudget()
	if base <= 0 {
		t.Fatalf("base budget = %d", base)
	}
	if err := s.SetPromptBudget(-1); err == nil || !strings.Contains(err.Error(), "negative") {
		t.Fatalf("error = %v, want a negative-cap refusal", err)
	}
	if err := s.SetPromptBudget(base + 1); err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("error = %v, want a capacity refusal", err)
	}
	if got := s.PromptBudget(); got != base {
		t.Fatalf("a refused cap changed the budget to %d", got)
	}
}

func TestPromptBudgetAppliesAndClears(t *testing.T) {
	s := limitsSession()
	base := s.PromptBudget()
	if err := s.SetPromptBudget(1024); err != nil {
		t.Fatal(err)
	}
	if got := s.PromptBudget(); got != 1024 {
		t.Fatalf("budget = %d, want 1024", got)
	}
	if got := s.MaxContextTokens; got != 1024 {
		t.Fatalf("MaxContextTokens = %d, want the applied cap", got)
	}
	// Zero clears rather than setting a zero-token budget.
	if err := s.SetPromptBudget(0); err != nil {
		t.Fatal(err)
	}
	if got := s.PromptBudget(); got != base {
		t.Fatalf("budget = %d after clearing, want the model capacity %d", got, base)
	}
}

func TestMaxStepsRejectsNegativeAndAcceptsUnlimited(t *testing.T) {
	s := limitsSession()
	if err := s.SetMaxSteps(-1); err == nil || !strings.Contains(err.Error(), "negative") {
		t.Fatalf("error = %v, want a negative-steps refusal", err)
	}
	if err := s.SetMaxSteps(7); err != nil {
		t.Fatal(err)
	}
	if got := s.MaxStepsValue(); got != 7 {
		t.Fatalf("steps = %d, want 7", got)
	}
	// Zero is the documented "unlimited", not an unset that falls back.
	if err := s.SetMaxSteps(0); err != nil {
		t.Fatal(err)
	}
	if got := s.MaxStepsValue(); got != 0 {
		t.Fatalf("steps = %d, want 0 (unlimited)", got)
	}
}

func TestPromptBudgetForAppliesSessionCapsToACandidateProfile(t *testing.T) {
	s := limitsSession()
	small := config.ModelSpec{Name: "small", ContextWindowTokens: 4096}
	if got := s.PromptBudgetFor(small); got <= 0 || got > 4096 {
		t.Fatalf("candidate budget = %d, want it bounded by the candidate window", got)
	}
}
