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

// ReasoningSetting is the whole dial the next request will carry: the
// effective level paired with the active model's dialect. Callers outside the
// session that must send what the session sends take the pair from here, so a
// level and a dialect resolved at different moments cannot drift apart.
func (s *Session) ReasoningSetting() reasoning.Setting {
	s.mu.RLock()
	defer s.mu.RUnlock()
	profile := s.binding.Profile
	profile.Reasoning = s.effectiveReasoningLocked()
	return config.ModelReasoning(profile)
}

// ReasoningDefault is the active model's configured default, independent of
// any /effort choice. The picker labels it so the user can tell what they are
// departing from.
func (s *Session) ReasoningDefault() reasoning.Level {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.binding.Profile.Reasoning
}

// SetReasoningEffort applies a /effort choice for the active model, or clears
// the choice back to the model's configured default when the level is unset.
//
// An active level must be one the model declared: the declared set is the
// contract, and sending a level outside it would earn a provider 400 the user
// cannot diagnose from the picker they were shown. A refusal leaves the
// previous effort in force rather than clearing it.
//
// The clear is spelled as the empty level rather than as its own method
// because the empty level already means "unset" everywhere in
// internal/reasoning, and a model that declares efforts with no configured
// default ships in exactly that state. A second verb would be a second
// vocabulary for a value the dial already holds.
func (s *Session) SetReasoningEffort(level reasoning.Level) error {
	// The dial belongs to the binding, and background orchestration outlives
	// the turn that spawned it while its nested handlers read the dial live on
	// every step. So whatever forbids replacing the binding forbids re-grading
	// the work already running against it. The guard is a caller-owned callback
	// and must not be invoked under s.mu.
	if err := s.CheckSwitchAllowed(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Same rule as /model: an in-flight turn already captured its binding, so
	// changing the dial now would report a change the running request did not
	// get.
	if s.activeTurns > 0 {
		return fmt.Errorf("reasoning effort cannot change while work is active")
	}
	if !level.Active() {
		s.reasoningEffort = ""
		s.invalidateLocked()
		return nil
	}
	profile := s.binding.Profile
	if !config.ModelOffersReasoning(profile) {
		return fmt.Errorf("model %q declares no reasoning efforts", s.binding.Model)
	}
	if !slices.Contains(profile.ReasoningEfforts, level) {
		return fmt.Errorf("model %q does not offer reasoning effort %q (offers %s)",
			s.binding.Model, level, reasoning.FormatLevels(profile.ReasoningEfforts))
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
