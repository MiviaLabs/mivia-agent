package chat

import (
	"fmt"
	"slices"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// ReasoningChoices is the ordered set of efforts the active model offers, in
// the order its configuration lists them. An empty result means this model has
// no reasoning surface, which is what /effort reports instead of an empty
// picker.
func (s *Session) ReasoningChoices() []reasoning.Level {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.binding.Profile.ReasoningEfforts)
}

// ReasoningEffort is the level the next request will carry: the user's /effort
// choice when they made one, otherwise the model's configured default.
func (s *Session) ReasoningEffort() reasoning.Level {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.effectiveReasoningLocked()
}

// ReasoningDefault is the active model's configured default, independent of
// any /effort choice. The picker labels it so the user can tell what they are
// departing from.
func (s *Session) ReasoningDefault() reasoning.Level {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.binding.Profile.Reasoning
}

// SetReasoningEffort applies a /effort choice for the active model.
//
// The level must be one the model declared: the declared set is the contract,
// and sending a level outside it would earn a provider 400 the user cannot
// diagnose from the picker they were shown. A refusal leaves the previous
// effort in force rather than clearing it.
func (s *Session) SetReasoningEffort(level reasoning.Level) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Same rule as /model: an in-flight turn already captured its binding, so
	// changing the dial now would report a change the running request did not
	// get.
	if s.activeTurns > 0 {
		return fmt.Errorf("reasoning effort cannot change while work is active")
	}
	profile := s.binding.Profile
	if !config.ModelOffersReasoning(profile) {
		return fmt.Errorf("model %q declares no reasoning efforts", s.binding.Model)
	}
	if !slices.Contains(profile.ReasoningEfforts, level) {
		return fmt.Errorf("model %q does not offer reasoning effort %q (offers %s)",
			s.binding.Model, level, formatLevels(profile.ReasoningEfforts))
	}
	s.reasoningEffort = level
	s.invalidateLocked()
	return nil
}

// effectiveReasoningLocked resolves the override against the model default.
func (s *Session) effectiveReasoningLocked() reasoning.Level {
	if s.reasoningEffort.Active() {
		return s.reasoningEffort
	}
	return s.binding.Profile.Reasoning
}

// formatLevels renders a declared set for an error or a picker line.
func formatLevels(levels []reasoning.Level) string {
	out := ""
	for i, level := range levels {
		if i > 0 {
			out += ", "
		}
		out += string(level)
	}
	return out
}
